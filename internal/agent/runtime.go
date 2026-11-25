package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/model/openaimodel"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/genai"

	"github.com/zhimoe/aglet/internal/config"
	"github.com/zhimoe/aglet/internal/mcp"
	"github.com/zhimoe/aglet/internal/permissions"
	sessionstore "github.com/zhimoe/aglet/internal/session"
	"github.com/zhimoe/aglet/internal/tools"
)

type Options struct {
	Profile   string
	Model     string
	Workspace string
	SessionID string
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
	sessionID string
	userID    string
	appName   string
	runner    *runner.Runner
	store     *sessionstore.Store
	mcp       *mcp.Manager
	log       *slog.Logger
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
	localTools, err := tools.NewLocalTools(tools.Deps{
		Workspace: workspace,
		Policy:    opts.Policy,
		Approver:  opts.Approver,
	})
	if err != nil {
		return nil, err
	}
	mcpManager, err := mcp.New(ctx, cfg.MCPServers, opts.Policy, opts.Approver, cfg.StrictMCP, logger)
	if err != nil {
		return nil, err
	}
	prompt, err := BuildSystemPrompt(cfg, workspace)
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
	sessionID, err := store.Ensure(ctx, opts.SessionID)
	if err != nil {
		_ = store.Close()
		_ = mcpManager.Close()
		return nil, err
	}
	r, err := runner.New(runner.Config{
		AppName:        config.AppName,
		Agent:          a,
		SessionService: store.Service,
	})
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
		log:       logger,
	}, nil
}

func (r *Runtime) SessionID() string   { return r.sessionID }
func (r *Runtime) ModelName() string   { return r.modelName }
func (r *Runtime) ProfileName() string { return r.profile }

// Run sends one user turn to the agent and streams normalized events.
func (r *Runtime) Run(ctx context.Context, prompt string, emit Emitter) error {
	if strings.TrimSpace(prompt) == "" {
		return fmt.Errorf("empty prompt")
	}
	if emit == nil {
		emit = func(Event) {}
	}
	maxCalls := r.cfg.Agent.MaxLLMCalls
	if maxCalls == 0 {
		maxCalls = 50
	}
	msg := genai.NewContentFromText(prompt, genai.RoleUser)
	mapper := &Mapper{}
	for ev, err := range r.runner.Run(ctx, r.userID, r.sessionID, msg, adkagent.RunConfig{
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
	sess, err := r.store.Get(ctx, r.sessionID)
	if err != nil {
		return nil, err
	}
	var out []Event
	for ev := range sess.Events().All() {
		out = append(out, MapHistory(ev)...)
	}
	return out, nil
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
