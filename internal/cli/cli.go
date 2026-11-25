package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"golang.org/x/term"

	"github.com/zhimoe/aglet/internal/agent"
	"github.com/zhimoe/aglet/internal/config"
	"github.com/zhimoe/aglet/internal/headless"
	mcpmanager "github.com/zhimoe/aglet/internal/mcp"
	"github.com/zhimoe/aglet/internal/permissions"
	"github.com/zhimoe/aglet/internal/tui"
	"github.com/zhimoe/aglet/internal/version"
)

func Run(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}
	switch args[0] {
	case "run":
		return runCmd(args[1:])
	case "tui":
		return tuiCmd(args[1:])
	case "config":
		return configCmd(args[1:])
	case "mcp":
		return mcpCmd(args[1:])
	case "version", "--version", "-v":
		fmt.Println(version.Version)
		return 0
	case "help", "--help", "-h":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", args[0])
		usage()
		return 2
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `aglet — a small ADK v2 + Bubble Tea CLI agent

Usage:
  aglet run [flags] [prompt]     one-shot / headless turn ("-" or pipe for stdin)
  aglet tui [flags]              interactive TUI
  aglet config init [--path P]   write an example config
  aglet config show              print resolved config (secrets redacted)
  aglet mcp list                 connect MCP servers and list tools
  aglet version

Common flags:
  --config PATH      config file (default: ./aglet.json or ~/.config/aglet/config.json)
  --profile NAME     profile from config (default: config.default_profile)
  --model NAME       override the profile model
  --workspace PATH   override workspace
  --session ID       resume/create a session
  --show-thoughts    display thought events when the provider emits them

Run flags:
  --json             emit JSONL events instead of text
  --yes              auto-approve tools that require approval
  --deny             deny all tools that require approval
`)
}

type commonFlags struct {
	configPath string
	profile    string
	model      string
	workspace  string
	session    string
	maxCalls   int
}

func (c *commonFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&c.configPath, "config", "", "config file path")
	fs.StringVar(&c.profile, "profile", "", "model profile")
	fs.StringVar(&c.model, "model", "", "model override")
	fs.StringVar(&c.workspace, "workspace", "", "workspace directory")
	fs.StringVar(&c.session, "session", "", "session id")
	fs.IntVar(&c.maxCalls, "max-llm-calls", 0, "max LLM calls (0 = config default)")
}

func (c *commonFlags) apply(cfg *config.Config) {
	if c.workspace != "" {
		cfg.Workspace = c.workspace
	}
	if c.maxCalls > 0 {
		cfg.Agent.MaxLLMCalls = c.maxCalls
	}
}

func loadConfig(c *commonFlags) (*config.Config, error) {
	cfg, _, err := config.Load(c.configPath)
	if err != nil {
		return nil, err
	}
	c.apply(cfg)
	return cfg, nil
}

func newLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

func runCmd(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var cf commonFlags
	cf.register(fs)
	var jsonOut, yes, deny, showThoughts bool
	fs.BoolVar(&jsonOut, "json", false, "emit JSONL events")
	fs.BoolVar(&yes, "yes", false, "auto-approve tools")
	fs.BoolVar(&deny, "deny", false, "deny tools requiring approval")
	fs.BoolVar(&showThoughts, "show-thoughts", false, "show thought events")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if yes && deny {
		fmt.Fprintln(os.Stderr, "--yes and --deny are mutually exclusive")
		return 2
	}
	cfg, err := loadConfig(&cf)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		return 2
	}
	if !flagWasSet(fs, "show-thoughts") {
		showThoughts = cfg.UI.ShowThoughtsEnabled()
	}
	tty := term.IsTerminal(int(os.Stdin.Fd()))
	prompt := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if prompt == "" {
		if tty {
			fmt.Fprintln(os.Stderr, "prompt required (or pipe text into stdin)")
			return 2
		}
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read stdin: %v\n", err)
			return 2
		}
		prompt = strings.TrimSpace(string(b))
	}
	if prompt == "" {
		fmt.Fprintln(os.Stderr, "empty prompt")
		return 2
	}

	opts := headless.Options{
		Out: os.Stdout, Err: os.Stderr, In: os.Stdin,
		JSON: jsonOut, ShowThoughts: showThoughts, TTY: tty, Yes: yes, Deny: deny,
	}
	approver := headless.NewApprover(opts)
	policy := permissions.NewPolicy(cfg.Permissions)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	rt, err := agent.New(ctx, cfg, agent.Options{
		Profile: cf.profile, Model: cf.model, Workspace: cfg.Workspace,
		SessionID: cf.session, Policy: policy, Approver: approver, Logger: newLogger(),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "start: %v\n", err)
		return 1
	}
	defer rt.Close()

	if err := headless.Run(ctx, rt, prompt, opts); err != nil {
		if ctx.Err() != nil {
			return 130
		}
		fmt.Fprintf(os.Stderr, "run: %v\n", err)
		return 1
	}
	return 0
}

func tuiCmd(args []string) int {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var cf commonFlags
	cf.register(fs)
	var showThoughts bool
	fs.BoolVar(&showThoughts, "show-thoughts", false, "show thought events")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		fmt.Fprintln(os.Stderr, "tui requires a TTY; use `aglet run` instead")
		return 2
	}
	cfg, err := loadConfig(&cf)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		return 2
	}
	if !flagWasSet(fs, "show-thoughts") {
		showThoughts = cfg.UI.ShowThoughtsEnabled()
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	policy := permissions.NewPolicy(cfg.Permissions)
	holder := &tui.ProgramHolder{}
	approver := tui.NewApprover(holder)
	rt, err := agent.New(ctx, cfg, agent.Options{
		Profile: cf.profile, Model: cf.model, Workspace: cfg.Workspace,
		SessionID: cf.session, Policy: policy, Approver: approver, Logger: newLogger(),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "start: %v\n", err)
		return 1
	}
	defer rt.Close()

	history, err := rt.History(ctx)
	if err != nil {
		newLogger().Warn("load history", "error", err)
	}
	workspace := config.ExpandPath(cfg.Workspace)
	if abs, err := filepath.Abs(workspace); err == nil {
		workspace = abs
	}
	m := tui.NewModel(tui.Config{
		Runtime: rt, Profile: rt.ProfileName(), ModelName: rt.ModelName(),
		SessionID: rt.SessionID(), Workspace: workspace, ShowThoughts: showThoughts, History: history,
	})
	p := tea.NewProgram(m)
	holder.Program = p
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "tui: %v\n", err)
		return 1
	}
	return 0
}

func configCmd(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: aglet config init|show")
		return 2
	}
	switch args[0] {
	case "init":
		fs := flag.NewFlagSet("config init", flag.ContinueOnError)
		fs.SetOutput(os.Stderr)
		path := ""
		force := false
		fs.StringVar(&path, "path", "", "config path")
		fs.BoolVar(&force, "force", false, "overwrite existing file")
		if err := fs.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		if path == "" {
			path = config.DefaultPath()
		}
		path = config.ExpandPath(path)
		if _, err := os.Stat(path); err == nil && !force {
			fmt.Fprintf(os.Stderr, "refusing to overwrite %s (use --force)\n", path)
			return 2
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "create config dir: %v\n", err)
			return 1
		}
		if err := os.WriteFile(path, []byte(config.ExampleJSON), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write config: %v\n", err)
			return 1
		}
		fmt.Println(path)
		return 0
	case "show":
		fs := flag.NewFlagSet("config show", flag.ContinueOnError)
		fs.SetOutput(os.Stderr)
		var cf commonFlags
		cf.register(fs)
		if err := fs.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		cfg, err := loadConfig(&cf)
		if err != nil {
			fmt.Fprintf(os.Stderr, "config: %v\n", err)
			return 2
		}
		b, err := cfg.RedactedJSON()
		if err != nil {
			fmt.Fprintf(os.Stderr, "marshal config: %v\n", err)
			return 1
		}
		fmt.Println(string(b))
		return 0
	default:
		fmt.Fprintln(os.Stderr, "usage: aglet config init|show")
		return 2
	}
}

func mcpCmd(args []string) int {
	if len(args) == 0 || args[0] != "list" {
		fmt.Fprintln(os.Stderr, "usage: aglet mcp list")
		return 2
	}
	fs := flag.NewFlagSet("mcp list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var cf commonFlags
	cf.register(fs)
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	cfg, err := loadConfig(&cf)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		return 2
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	policy := permissions.NewPolicy(cfg.Permissions)
	mgr, err := mcpmanager.New(ctx, cfg.MCPServers, policy, nil, cfg.StrictMCP, newLogger())
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp: %v\n", err)
		return 1
	}
	defer mgr.Close()
	tools := mgr.Tools()
	if len(tools) == 0 {
		fmt.Println("no MCP tools discovered (check mcp_servers in config)")
		return 0
	}
	for _, t := range tools {
		fmt.Printf("%s\t%s\n", t.Name(), t.Description())
	}
	return 0
}

func flagWasSet(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}
