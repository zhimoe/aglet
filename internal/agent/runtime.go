package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/plugin"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session/compaction"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/genai"

	"github.com/zhimoe/aglet/internal/commands"
	"github.com/zhimoe/aglet/internal/config"
	"github.com/zhimoe/aglet/internal/hooks"
	"github.com/zhimoe/aglet/internal/instructions"
	mcpmanager "github.com/zhimoe/aglet/internal/mcp"
	"github.com/zhimoe/aglet/internal/permissions"
	"github.com/zhimoe/aglet/internal/plan"
	sessionstore "github.com/zhimoe/aglet/internal/session"
	"github.com/zhimoe/aglet/internal/skills"
	"github.com/zhimoe/aglet/internal/subagents"
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
	llm       model.LLM
	testLLM   model.LLM
	userID    string
	appName   string
	runner    *runner.Runner
	store     *sessionstore.Store
	mcp       *mcpmanager.Manager
	plan      *plan.Board
	jobs      *tools.JobManager
	log       *slog.Logger

	hookRunner     *hooks.Runner
	hookBridge     *hookBridge
	hookPlugin     *plugin.Plugin
	toolHookPlugin *plugin.Plugin
	hub            *emitterHub
	runMu          sync.Mutex

	subagentList []subagents.Definition
	commandList  []commands.Command
	skillList    []skills.Skill
	instr        []instructions.Section
	toolDeps     tools.Deps
	genConfig    *genai.GenerateContentConfig
	subagentSem  chan struct{}

	modelMu sync.Mutex
	models  map[string]model.LLM

	closeOnce sync.Once
	closeErr  error

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
	var rootLLM model.LLM
	var testLLM model.LLM
	if opts.ModelLLM != nil {
		rootLLM = opts.ModelLLM
		testLLM = opts.ModelLLM
	} else {
		rootLLM, err = buildModel(profile)
		if err != nil {
			return nil, err
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
		logWarnings(logger, "skill", warnings)
	}
	subagentList, warnings, err := subagents.Discover(workspace, cfg.Context.AgentsDirs, cfg.Context.MaxSkillBytes)
	if err != nil {
		return nil, fmt.Errorf("load subagents: %w", err)
	}
	logWarnings(logger, "subagent", warnings)
	commandList, warnings, err := commands.Discover(workspace, cfg.Context.CommandsDirs, cfg.Context.MaxSkillBytes)
	if err != nil {
		return nil, fmt.Errorf("load commands: %w", err)
	}
	logWarnings(logger, "command", warnings)

	board := plan.New()
	jobs := tools.NewJobManager()
	toolDeps := tools.Deps{
		Workspace: workspace,
		Policy:    opts.Policy,
		Approver:  opts.Approver,
		Plan:      board,
		Skills:    skillList,
		Jobs:      jobs,
		Web:       cfg.Web,
	}

	var store *sessionstore.Store
	var mcpManager *mcpmanager.Manager
	fail := func(err error) (*Runtime, error) {
		if mcpManager != nil {
			_ = mcpManager.Close()
		}
		_ = jobs.Close()
		if store != nil {
			_ = store.Close()
		}
		return nil, err
	}

	store, err = sessionstore.Open(cfg.SessionDB, config.AppName, "local")
	if err != nil {
		return fail(err)
	}
	desired := opts.SessionID
	if desired == "" && opts.Continue {
		infos, err := store.List(ctx)
		if err != nil {
			return fail(err)
		}
		if len(infos) > 0 {
			desired = infos[len(infos)-1].ID
		}
	}
	source := "startup"
	if desired != "" {
		if _, err := store.Get(ctx, desired); err == nil {
			source = "resume"
		}
	}
	sessionID, err := store.Ensure(ctx, desired)
	if err != nil {
		return fail(err)
	}

	rt := &Runtime{
		cfg:          cfg,
		profile:      profileName,
		modelName:    profile.Model,
		llm:          rootLLM,
		testLLM:      testLLM,
		userID:       "local",
		appName:      config.AppName,
		store:        store,
		plan:         board,
		jobs:         jobs,
		log:          logger,
		hub:          newEmitterHub(),
		subagentList: subagentList,
		commandList:  commandList,
		skillList:    skillList,
		instr:        instr,
		toolDeps:     toolDeps,
		subagentSem:  make(chan struct{}, 4),
		models:       map[string]model.LLM{},
		sessionID:    sessionID,
	}
	rt.hookRunner = hooks.New(cfg.Hooks, hooks.Options{
		Workspace:      workspace,
		SessionID:      rt.SessionID,
		PermissionMode: rt.ApprovalMode,
		Logger:         logger,
	})
	rt.hookBridge = newHookBridge(rt.hookRunner, rt.hub)
	start := rt.hookRunner.Run(ctx, config.HookSessionStart, hooks.Input{Source: source})
	for _, msg := range start.SystemMessages {
		rt.hub.Notice(msg)
	}
	for _, msg := range start.Warnings {
		logger.Warn("hook warning", "detail", msg)
	}

	prompt, err := BuildSystemPrompt(cfg, workspace, instr, skillList, subagentList, start.AdditionalContext)
	if err != nil {
		return fail(err)
	}
	comp, err := buildCompaction(cfg.Agent.Compaction)
	if err != nil {
		return fail(err)
	}
	genCfg := &genai.GenerateContentConfig{}
	if cfg.Agent.Temperature > 0 {
		t := cfg.Agent.Temperature
		genCfg.Temperature = &t
	}
	rt.genConfig = genCfg

	mainPlugin, toolPlugin, err := rt.hookBridge.plugins()
	if err != nil {
		return fail(err)
	}
	rt.hookPlugin = mainPlugin
	rt.toolHookPlugin = toolPlugin

	localTools, err := tools.NewLocalTools(toolDeps)
	if err != nil {
		return fail(err)
	}
	mcpManager, err = mcpmanager.New(ctx, cfg.MCPServers, opts.Policy, opts.Approver, cfg.StrictMCP, logger)
	if err != nil {
		return fail(err)
	}
	rt.mcp = mcpManager
	adkTools := make([]tool.Tool, 0, len(localTools)+len(mcpManager.Tools())+1)
	adkTools = append(adkTools, localTools...)
	adkTools = append(adkTools, mcpManager.Tools()...)
	taskTool, err := newTaskTool(rt)
	if err != nil {
		return fail(err)
	}
	adkTools = append(adkTools, taskTool)

	a, err := llmagent.New(llmagent.Config{
		Name:        rootAgentName,
		Description: "A terminal coding agent with filesystem, shell, MCP, web and subagent tools.",
		Model:       rootLLM,
		InstructionProvider: func(adkagent.ReadonlyContext) (string, error) {
			return prompt, nil
		},
		Tools:                 adkTools,
		GenerateContentConfig: genCfg,
	})
	if err != nil {
		return fail(fmt.Errorf("create agent: %w", err))
	}
	r, err := runner.New(runner.Config{
		AppName:        config.AppName,
		Agent:          a,
		SessionService: store.Service,
		Compaction:     comp,
		PluginConfig:   runner.PluginConfig{Plugins: pluginList(mainPlugin)},
	})
	if err != nil {
		return fail(fmt.Errorf("create runner: %w", err))
	}
	rt.runner = r
	return rt, nil
}

func logWarnings(logger *slog.Logger, kind string, warnings []string) {
	for _, warning := range warnings {
		logger.Warn(kind+" warning", "detail", warning)
	}
}

func pluginList(p *plugin.Plugin) []*plugin.Plugin {
	if p == nil {
		return nil
	}
	return []*plugin.Plugin{p}
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
	list := r.mcp.Tools()
	out := make([]string, 0, len(list))
	for _, t := range list {
		out = append(out, t.Name())
	}
	return out
}

func (r *Runtime) Skills() []skills.Skill {
	out := make([]skills.Skill, len(r.skillList))
	copy(out, r.skillList)
	return out
}

// Subagents returns the discovered subagent definitions.
func (r *Runtime) Subagents() []subagents.Definition {
	out := make([]subagents.Definition, len(r.subagentList))
	copy(out, r.subagentList)
	return out
}

// Commands returns the discovered custom slash commands.
func (r *Runtime) Commands() []commands.Command {
	out := make([]commands.Command, len(r.commandList))
	copy(out, r.commandList)
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
	r.runMu.Lock()
	defer r.runMu.Unlock()
	if r.runner == nil {
		return fmt.Errorf("runtime is not ready")
	}
	r.hub.SetSink(emit)
	defer r.hub.SetSink(nil)

	sessionID := r.SessionID()
	maxCalls := r.cfg.Agent.MaxLLMCalls
	if maxCalls == 0 {
		maxCalls = 50
	}
	r.plan.SetEmitter(func(steps []plan.Step) {
		r.hub.Emit(Event{Type: EventPlan, Plan: append([]plan.Step(nil), steps...)})
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
				r.hub.Emit(Event{Type: EventDone})
				return ctx.Err()
			}
			r.hub.Emit(Event{Type: EventError, Err: err})
			r.hub.Emit(Event{Type: EventDone})
			return err
		}
		for _, e := range mapper.Map(ev) {
			r.hub.Emit(e)
		}
	}
	r.hub.Emit(Event{Type: EventDone})
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
// policy. It does not run hooks; use RunShellWithHooks for the TUI.
func (r *Runtime) RunShell(ctx context.Context, command string) (tools.ShellResult, error) {
	return tools.RunShell(ctx, r.toolDeps, tools.ShellRequest{Command: command})
}

// RunShellWithHooks runs a shell escape and fires PreToolUse/PostToolUse hooks
// exactly once around the command.
func (r *Runtime) RunShellWithHooks(ctx context.Context, command string) (tools.ShellResult, error) {
	r.runMu.Lock()
	defer r.runMu.Unlock()
	var notices []string
	addNotices := func(res hooks.Result) {
		notices = append(notices, res.SystemMessages...)
		notices = append(notices, res.Warnings...)
	}
	if r.hookRunner != nil {
		res := r.hookRunner.Run(ctx, config.HookPreToolUse, hooks.Input{
			ToolName:  "run_command",
			ToolInput: map[string]any{"command": command},
			ToolUseID: "shell-escape",
		})
		addNotices(res)
		if res.Blocked {
			return tools.ShellResult{
				Command: command, Stdout: strings.Join(notices, "\n"),
				ExitCode: -1, Status: tools.JobKilled,
			}, fmt.Errorf("blocked by PreToolUse hook: %s", res.Reason)
		}
	}
	result, err := tools.RunShell(ctx, r.toolDeps, tools.ShellRequest{Command: command})
	if r.hookRunner != nil && result.Command != "" {
		toolErr := ""
		if err != nil {
			toolErr = err.Error()
		}
		res := r.hookRunner.Run(ctx, config.HookPostToolUse, hooks.Input{
			ToolName:  "run_command",
			ToolInput: map[string]any{"command": command},
			ToolUseID: "shell-escape",
			ToolResponse: map[string]any{
				"exit_code": result.ExitCode,
				"output":    result.Stdout,
			},
			ToolError: toolErr,
		})
		addNotices(res)
		if res.Blocked {
			notices = append(notices, "PostToolUse hook: "+res.Reason)
		}
	}
	if len(notices) > 0 {
		if result.Stdout != "" && !strings.HasSuffix(result.Stdout, "\n") {
			result.Stdout += "\n"
		}
		result.Stdout += "[hook] " + strings.Join(notices, "\n[hook] ")
	}
	return result, err
}

func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		if r.hookRunner != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			res := r.hookRunner.Run(ctx, config.HookSessionEnd, hooks.Input{})
			cancel()
			for _, msg := range res.SystemMessages {
				r.log.Info("hook", "detail", msg)
			}
			for _, msg := range res.Warnings {
				r.log.Warn("hook warning", "detail", msg)
			}
		}
		var errs []error
		if r.jobs != nil {
			errs = append(errs, r.jobs.Close())
		}
		if r.mcp != nil {
			errs = append(errs, r.mcp.Close())
		}
		if r.store != nil {
			errs = append(errs, r.store.Close())
		}
		r.closeErr = errors.Join(errs...)
	})
	return r.closeErr
}
