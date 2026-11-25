package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"

	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/model/openaimodel"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session/compaction"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/genai"

	"github.com/zhimoe/aglet/internal/config"
	"github.com/zhimoe/aglet/internal/instructions"
	"github.com/zhimoe/aglet/internal/mcp"
	"github.com/zhimoe/aglet/internal/permissions"
	"github.com/zhimoe/aglet/internal/plan"
	sessionstore "github.com/zhimoe/aglet/internal/session"
	"github.com/zhimoe/aglet/internal/skills"
	"github.com/zhimoe/aglet/internal/tools"
)

type Options struct {
	Profile   string
	Model     string
	Workspace string
	SessionID string
	Continue  bool
	Policy    *permissions.Policy
	Approver  permissions.Approver
	Logger    *slog.Logger

	// ModelLLM is a test/integration hook. When set, it replaces the
	// openaimodel instance (and skips the API key requirement).
	ModelLLM model.LLM
}

// Runtime owns the ADK agent, runner, tools and session store.
type Runtime struct {
	cfg       *config.Config
	profile   string
	modelName string
	userID    string
	appName   string
	runner    *runner.Runner
	store     *sessionstore.Store
	mcp       *mcp.Manager
	plan      *plan.Board
	skillList []skills.Skill
	toolDeps  tools.Deps
	log       *slog.Logger

	mu        sync.RWMutex
	sessionID string
}

func New(ctx context.Context, cfg *config.Config, opts Options) (*Runtime, error) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	profileName, profile, err := cfg.ResolveProfile(opts.Profile, opts.Model)
	if err != nil {
		return nil, err
	}
	var llm model.LLM
	if opts.ModelLLM != nil {
		llm = opts.ModelLLM
	} else {
		apiKey := os.Getenv(profile.APIKeyEnv)
		if apiKey == "" {
			return nil, fmt.Errorf("environment variable %s is empty (required for profile %s)", profile.APIKeyEnv, profileName)
		}
		api := openaimodel.APIChatCompletions
		if profile.API == "responses" {
			api = openaimodel.APIResponses
		}
		var err error
		llm, err = openaimodel.NewModel(ctx, profile.Model, &openaimodel.ClientConfig{
			APIKey:  apiKey,
			BaseURL: profile.BaseURL,
			API:     api,
		})
		if err != nil {
			return nil, fmt.Errorf("create model: %w", err)
		}
	}
	workspace := opts.Workspace
	if workspace == "" {
		workspace = cfg.Workspace
	}

	instr, err := instructions.Load(workspace, cfg.Context.InstructionFiles, cfg.Context.MaxInstructionBytes)
	if err != nil {
		return nil, fmt.Errorf("load instructions: %w", err)
	}
	var skillList []skills.Skill
	if cfg.Context.SkillsEnabledValue() {
		var warnings []string
		skillList, warnings, err = skills.Discover(workspace, cfg.Context.SkillsDirs, cfg.Context.MaxSkillBytes)
		if err != nil {
			return nil, fmt.Errorf("load skills: %w", err)
		}
		for _, warning := range warnings {
			logger.Warn("skill warning", "detail", warning)
		}
	}
	board := plan.New()
	toolDeps := tools.Deps{
		Workspace: workspace,
		Policy:    opts.Policy,
		Approver:  opts.Approver,
		Plan:      board,
		Skills:    skillList,
	}
	localTools, err := tools.NewLocalTools(toolDeps)
	if err != nil {
		return nil, err
	}
	mcpManager, err := mcp.New(ctx, cfg.MCPServers, opts.Policy, opts.Approver, cfg.StrictMCP, logger)
	if err != nil {
		return nil, err
	}
	prompt, err := BuildSystemPrompt(cfg, workspace, instr, skillList)
	if err != nil {
		_ = mcpManager.Close()
		return nil, err
	}
	comp, err := buildCompaction(cfg.Agent.Compaction)
	if err != nil {
		_ = mcpManager.Close()
		return nil, err
	}
	adkTools := make([]tool.Tool, 0, len(localTools)+len(mcpManager.Tools()))
	adkTools = append(adkTools, localTools...)
	adkTools = append(adkTools, mcpManager.Tools()...)

	genCfg := &genai.GenerateContentConfig{}
	if cfg.Agent.Temperature > 0 {
		t := cfg.Agent.Temperature
		genCfg.Temperature = &t
	}
	a, err := llmagent.New(llmagent.Config{
		Name:        "aglet",
		Description: "A terminal coding agent with filesystem, shell and MCP tools.",
		Model:       llm,
		InstructionProvider: func(adkagent.ReadonlyContext) (string, error) {
			return prompt, nil
		},
		Tools:                 adkTools,
		GenerateContentConfig: genCfg,
	})
	if err != nil {
		_ = mcpManager.Close()
		return nil, fmt.Errorf("create agent: %w", err)
	}
	store, err := sessionstore.Open(cfg.SessionDB, config.AppName, "local")
	if err != nil {
		_ = mcpManager.Close()
		return nil, err
	}
	desired := opts.SessionID
	if desired == "" && opts.Continue {
		infos, err := store.List(ctx)
		if err != nil {
			_ = store.Close()
			_ = mcpManager.Close()
			return nil, err
		}
		if len(infos) > 0 {
			desired = infos[len(infos)-1].ID
		}
	}
	sessionID, err := store.Ensure(ctx, desired)
	if err != nil {
		_ = store.Close()
		_ = mcpManager.Close()
		return nil, err
	}
	runnerCfg := runner.Config{
		AppName:        config.AppName,
		Agent:          a,
		SessionService: store.Service,
		Compaction:     comp,
	}
	r, err := runner.New(runnerCfg)
	if err != nil {
		_ = store.Close()
		_ = mcpManager.Close()
		return nil, fmt.Errorf("create runner: %w", err)
	}
	return &Runtime{
		cfg:       cfg,
		profile:   profileName,
		modelName: profile.Model,
		sessionID: sessionID,
		userID:    "local",
		appName:   config.AppName,
		runner:    r,
		store:     store,
		mcp:       mcpManager,
		plan:      board,
		skillList: skillList,
		toolDeps:  toolDeps,
		log:       logger,
	}, nil
}

func buildCompaction(c config.CompactionConfig) (*compaction.Config, error) {
	if c.TokenThreshold == 0 && c.CompactionInterval == 0 {
		return nil, nil
	}
	cc := &compaction.Config{
		TokenThreshold:     c.TokenThreshold,
		EventRetentionSize: c.EventRetentionSize,
		CompactionInterval: c.CompactionInterval,
		OverlapSize:        c.OverlapSize,
	}
	if err := cc.Validate(); err != nil {
		return nil, fmt.Errorf("agent.compaction: %w", err)
	}
	return cc, nil
}

func (r *Runtime) SessionID() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.sessionID
}

func (r *Runtime) ModelName() string   { return r.modelName }
func (r *Runtime) ProfileName() string { return r.profile }

func (r *Runtime) ApprovalMode() string {
	if r.toolDeps.Policy == nil {
		return config.ApprovalAsk
	}
	return r.toolDeps.Policy.Mode()
}

func (r *Runtime) SetApprovalMode(mode string) {
	if r.toolDeps.Policy != nil {
		r.toolDeps.Policy.SetMode(mode)
	}
}

func (r *Runtime) MCPTools() []string {
	if r.mcp == nil {
		return nil
	}
	tools := r.mcp.Tools()
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Name())
	}
	return out
}

func (r *Runtime) Skills() []skills.Skill {
	out := make([]skills.Skill, len(r.skillList))
	copy(out, r.skillList)
	return out
}

// SetSession switches the active session. Call only while idle.
func (r *Runtime) SetSession(ctx context.Context, id string) (string, error) {
	ensured, err := r.store.Ensure(ctx, id)
	if err != nil {
		return "", err
	}
	r.mu.Lock()
	r.sessionID = ensured
	r.mu.Unlock()
	return ensured, nil
}

func (r *Runtime) ListSessions(ctx context.Context) ([]sessionstore.Info, error) {
	return r.store.List(ctx)
}

func (r *Runtime) ContinueLatest(ctx context.Context) (string, error) {
	infos, err := r.store.List(ctx)
	if err != nil {
		return "", err
	}
	if len(infos) == 0 {
		return "", nil
	}
	return infos[len(infos)-1].ID, nil
}

// Run sends one user turn to the agent and streams normalized events.
func (r *Runtime) Run(ctx context.Context, prompt string, emit Emitter) error {
	if strings.TrimSpace(prompt) == "" {
		return fmt.Errorf("empty prompt")
	}
	if emit == nil {
		emit = func(Event) {}
	}
	sessionID := r.SessionID()
	maxCalls := r.cfg.Agent.MaxLLMCalls
	if maxCalls == 0 {
		maxCalls = 50
	}
	r.plan.SetEmitter(func(steps []plan.Step) {
		emit(Event{Type: EventPlan, Plan: append([]plan.Step(nil), steps...)})
	})
	defer r.plan.SetEmitter(nil)

	msg := genai.NewContentFromText(prompt, genai.RoleUser)
	mapper := &Mapper{}
	for ev, err := range r.runner.Run(ctx, r.userID, sessionID, msg, adkagent.RunConfig{
		StreamingMode: adkagent.StreamingModeSSE,
		MaxLLMCalls:   maxCalls,
	}) {
		if err != nil {
			if ctx.Err() != nil {
				emit(Event{Type: EventDone})
				return ctx.Err()
			}
			emit(Event{Type: EventError, Err: err})
			emit(Event{Type: EventDone})
			return err
		}
		for _, e := range mapper.Map(ev) {
			emit(e)
		}
	}
	emit(Event{Type: EventDone})
	return nil
}

// History replays the stored session events for UI display.
func (r *Runtime) History(ctx context.Context) ([]Event, error) {
	return r.HistoryFor(ctx, r.SessionID())
}

func (r *Runtime) HistoryFor(ctx context.Context, sessionID string) ([]Event, error) {
	sess, err := r.store.Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	var out []Event
	for ev := range sess.Events().All() {
		out = append(out, MapHistory(ev)...)
	}
	return out, nil
}

// RunShell executes a user-initiated shell escape through the normal approval
// policy.
func (r *Runtime) RunShell(ctx context.Context, command string) (tools.ShellResult, error) {
	return tools.RunShell(ctx, r.toolDeps, tools.ShellRequest{Command: command})
}

func (r *Runtime) Close() error {
	var errs []error
	if r.mcp != nil {
		errs = append(errs, r.mcp.Close())
	}
	if r.store != nil {
		errs = append(errs, r.store.Close())
	}
	return errors.Join(errs...)
}
