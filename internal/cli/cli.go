package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	tea "charm.land/bubbletea/v2"
	"golang.org/x/term"

	"github.com/zhimoe/aglet/internal/agent"
	"github.com/zhimoe/aglet/internal/config"
	"github.com/zhimoe/aglet/internal/headless"
	mcpmanager "github.com/zhimoe/aglet/internal/mcp"
	"github.com/zhimoe/aglet/internal/permissions"
	sessionstore "github.com/zhimoe/aglet/internal/session"
	"github.com/zhimoe/aglet/internal/tui"
	"github.com/zhimoe/aglet/internal/version"
)

func Run(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}
	switch args[0] {
	case "run", "exec":
		return runCmd(args[1:])
	case "tui":
		return tuiCmd(args[1:])
	case "resume":
		return resumeCmd(args[1:])
	case "sessions":
		return sessionsCmd(args[1:])
	case "review":
		return reviewCmd(args[1:])
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
  aglet run|exec [flags] [prompt]  one-shot / headless turn ("-" or pipe for stdin)
  aglet tui [flags]                interactive TUI
  aglet resume [ID] --last         open the TUI for a previous session
  aglet sessions list              list stored sessions (no model key needed)
  aglet review [--base REF]        review a git diff
  aglet config init [--path P]     write an example config
  aglet config show                print resolved config (secrets redacted)
  aglet mcp list                   connect MCP servers and list tools
  aglet version

Common flags:
  --config PATH      config file (default: ./aglet.json or ~/.config/aglet/config.json)
  --profile NAME     profile from config (default: config.default_profile)
  --model NAME       override the profile model
  --workspace PATH   override workspace
  --session ID       resume/create a session
  --continue         continue the most recent session
  --mode NAME        ask | read-only | auto-edit | full-auto
  --show-thoughts    display thought events when the provider emits them

Aliases: --read-only, --auto-edit, --full-auto, --plan

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
	cont       bool
	maxCalls   int
}

func (c *commonFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&c.configPath, "config", "", "config file path")
	fs.StringVar(&c.profile, "profile", "", "model profile")
	fs.StringVar(&c.model, "model", "", "model override")
	fs.StringVar(&c.workspace, "workspace", "", "workspace directory")
	fs.StringVar(&c.session, "session", "", "session id")
	fs.BoolVar(&c.cont, "continue", false, "continue the most recent session")
	fs.IntVar(&c.maxCalls, "max-llm-calls", 0, "max LLM calls (0 = config default)")
}

type modeFlags struct {
	mode     string
	readOnly bool
	autoEdit bool
	fullAuto bool
}

func (m *modeFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&m.mode, "mode", "", "approval mode: ask, read-only, auto-edit, full-auto")
	fs.BoolVar(&m.readOnly, "read-only", false, "alias for --mode read-only")
	fs.BoolVar(&m.readOnly, "plan", false, "alias for --mode read-only")
	fs.BoolVar(&m.autoEdit, "auto-edit", false, "alias for --mode auto-edit")
	fs.BoolVar(&m.fullAuto, "full-auto", false, "alias for --mode full-auto")
	fs.BoolVar(&m.fullAuto, "yolo", false, "alias for --mode full-auto")
}

func (m *modeFlags) resolve(cfg *config.Config) (string, error) {
	mode := ""
	count := 0
	if m.mode != "" {
		mode = config.NormalizeApprovalMode(m.mode)
		count++
	}
	if m.readOnly {
		mode = config.ApprovalReadOnly
		count++
	}
	if m.autoEdit {
		mode = config.ApprovalAutoEdit
		count++
	}
	if m.fullAuto {
		mode = config.ApprovalFullAuto
		count++
	}
	if count > 1 {
		return "", fmt.Errorf("approval mode flags are mutually exclusive")
	}
	if mode == "" {
		mode = config.NormalizeApprovalMode(cfg.ApprovalMode)
	}
	switch mode {
	case config.ApprovalAsk, config.ApprovalReadOnly, config.ApprovalAutoEdit, config.ApprovalFullAuto:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid approval mode %q", mode)
	}
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
	var mf modeFlags
	mf.register(fs)
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
	if cf.session != "" && cf.cont {
		fmt.Fprintln(os.Stderr, "--session and --continue are mutually exclusive")
		return 2
	}
	cfg, err := loadConfig(&cf)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		return 2
	}
	mode, err := mf.resolve(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mode: %v\n", err)
		return 2
	}
	if !flagWasSet(fs, "show-thoughts") {
		showThoughts = cfg.UI.ShowThoughtsEnabled()
	}
	tty := term.IsTerminal(int(os.Stdin.Fd()))
	prompt := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if prompt == "" || prompt == "-" {
		if prompt == "-" && tty {
			fmt.Fprintln(os.Stderr, "prompt '-' requires piped stdin")
			return 2
		}
		if prompt == "" && tty {
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
	policy := permissions.NewPolicy(cfg.Permissions, mode)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	rt, err := agent.New(ctx, cfg, agent.Options{
		Profile: cf.profile, Model: cf.model, Workspace: cfg.Workspace,
		SessionID: cf.session, Continue: cf.cont,
		Policy: policy, Approver: approver, Logger: newLogger(),
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
	var mf modeFlags
	mf.register(fs)
	var showThoughts bool
	fs.BoolVar(&showThoughts, "show-thoughts", false, "show thought events")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if cf.session != "" && cf.cont {
		fmt.Fprintln(os.Stderr, "--session and --continue are mutually exclusive")
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
	mode, err := mf.resolve(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mode: %v\n", err)
		return 2
	}
	if !flagWasSet(fs, "show-thoughts") {
		showThoughts = cfg.UI.ShowThoughtsEnabled()
	}
	return startTUI(cfg, cf, mode, showThoughts)
}

func startTUI(cfg *config.Config, cf commonFlags, mode string, showThoughts bool) int {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	policy := permissions.NewPolicy(cfg.Permissions, mode)
	holder := &tui.ProgramHolder{}
	approver := tui.NewApprover(holder)
	rt, err := agent.New(ctx, cfg, agent.Options{
		Profile: cf.profile, Model: cf.model, Workspace: cfg.Workspace,
		SessionID: cf.session, Continue: cf.cont,
		Policy: policy, Approver: approver, Logger: newLogger(),
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
		SessionID: rt.SessionID(), Workspace: workspace, ShowThoughts: showThoughts,
		ApprovalMode: mode, History: history,
	})
	p := tea.NewProgram(m)
	holder.Program = p
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "tui: %v\n", err)
		return 1
	}
	return 0
}

func resumeCmd(args []string) int {
	fs := flag.NewFlagSet("resume", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var cf commonFlags
	cf.register(fs)
	var mf modeFlags
	mf.register(fs)
	var showThoughts, last bool
	fs.BoolVar(&showThoughts, "show-thoughts", false, "show thought events")
	fs.BoolVar(&last, "last", false, "resume the most recent session")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		fmt.Fprintln(os.Stderr, "resume requires a TTY; use `aglet run --session ID` instead")
		return 2
	}
	id := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if last && id != "" {
		fmt.Fprintln(os.Stderr, "--last and an explicit session ID are mutually exclusive")
		return 2
	}
	if id != "" {
		cf.session = id
	} else {
		cf.cont = true
	}
	cfg, err := loadConfig(&cf)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		return 2
	}
	mode, err := mf.resolve(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mode: %v\n", err)
		return 2
	}
	if !flagWasSet(fs, "show-thoughts") {
		showThoughts = cfg.UI.ShowThoughtsEnabled()
	}
	return startTUI(cfg, cf, mode, showThoughts)
}

func sessionsCmd(args []string) int {
	if len(args) == 0 || args[0] != "list" {
		fmt.Fprintln(os.Stderr, "usage: aglet sessions list")
		return 2
	}
	fs := flag.NewFlagSet("sessions list", flag.ContinueOnError)
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
	store, err := sessionstore.Open(cfg.SessionDB, config.AppName, "local")
	if err != nil {
		fmt.Fprintf(os.Stderr, "sessions: %v\n", err)
		return 1
	}
	defer store.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	infos, err := store.List(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sessions: %v\n", err)
		return 1
	}
	if len(infos) == 0 {
		fmt.Println("no sessions")
		return 0
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tUPDATED\tEVENTS\tPREVIEW")
	for i := len(infos) - 1; i >= 0; i-- {
		in := infos[i]
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n", in.ID, in.UpdatedAt.Format("2006-01-02 15:04"), in.Events, in.Preview)
	}
	_ = tw.Flush()
	return 0
}

func reviewCmd(args []string) int {
	fs := flag.NewFlagSet("review", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var cf commonFlags
	cf.register(fs)
	var base string
	var jsonOut bool
	fs.StringVar(&base, "base", "", "diff against a git revision instead of the working tree")
	fs.BoolVar(&jsonOut, "json", false, "emit JSONL events")
	if err := fs.Parse(args); err != nil {
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
	workspace := config.ExpandPath(cfg.Workspace)
	if abs, err := filepath.Abs(workspace); err == nil {
		workspace = abs
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	diffText, err := gitDiffText(ctx, workspace, base)
	if err != nil {
		fmt.Fprintf(os.Stderr, "review: %v\n", err)
		return 1
	}
	if strings.TrimSpace(diffText) == "" {
		fmt.Println("no changes to review")
		return 0
	}
	diffText, _ = truncateText(diffText, 64*1024)
	prompt := "Review the following git diff for correctness, risks, regressions, and missing tests. Be concise and cite file paths.\n\n```diff\n" + diffText + "\n```\n"
	opts := headless.Options{Out: os.Stdout, Err: os.Stderr, In: os.Stdin, JSON: jsonOut, TTY: term.IsTerminal(int(os.Stdin.Fd()))}
	approver := headless.NewApprover(opts)
	policy := permissions.NewPolicy(cfg.Permissions, config.ApprovalReadOnly)
	rt, err := agent.New(ctx, cfg, agent.Options{
		Profile: cf.profile, Model: cf.model, Workspace: workspace,
		SessionID: cf.session, Continue: cf.cont,
		Policy: policy, Approver: approver, Logger: newLogger(),
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
		fmt.Fprintf(os.Stderr, "review: %v\n", err)
		return 1
	}
	return 0
}

func gitDiffText(ctx context.Context, workspace, base string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	args := []string{"diff", "--no-color"}
	if base != "" {
		args = append(args, base)
	}
	cmd := exec.CommandContext(cctx, "git", args...)
	cmd.Dir = workspace
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git diff: %s", strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func truncateText(s string, max int) (string, bool) {
	if len(s) <= max {
		return s, false
	}
	return s[:max] + "\n...[truncated]", true
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
