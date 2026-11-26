package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"

	"github.com/zhimoe/aglet/internal/agent"
	"github.com/zhimoe/aglet/internal/commands"
	"github.com/zhimoe/aglet/internal/config"
	"github.com/zhimoe/aglet/internal/permissions"
	"github.com/zhimoe/aglet/internal/plan"
)

// Config is the immutable setup passed to the TUI model.
type Config struct {
	Runtime      *agent.Runtime
	Profile      string
	ModelName    string
	SessionID    string
	Workspace    string
	ShowThoughts bool
	ApprovalMode string
	History      []agent.Event
}

type itemKind int

const (
	itemUser itemKind = iota
	itemAssistant
	itemThought
	itemTool
	itemError
	itemInfo
	itemPlan
)

type item struct {
	kind        itemKind
	text        string
	streaming   bool
	toolID      string
	toolName    string
	toolArgs    string
	toolStatus  string
	toolOutput  string
	rendered    string
	renderedKey string
}

type approvalState struct {
	req  permissions.ApprovalRequest
	resp chan permissions.Decision
}

// Model is the Bubble Tea model.
type Model struct {
	cfg Config

	vp viewport.Model
	ta textarea.Model
	sp spinner.Model

	width  int
	height int

	items        []*item
	usage        agent.Usage
	planSteps    []plan.Step
	pending      *approvalState
	queue        []*approvalState
	approvalMode string
	sessionID    string

	mdRenderer *glamour.TermRenderer
	mdWidth    int

	busy   bool
	runID  int
	ch     <-chan agent.Event
	shell  <-chan shellEvent
	cancel context.CancelFunc
	err    error
}

type eventMsg struct {
	run int
	ev  agent.Event
}

type streamClosedMsg struct{ run int }

func NewModel(cfg Config) *Model {
	ta := textarea.New()
	ta.Placeholder = "Ask aglet to do something… (Enter to send, Ctrl+J for newline)"
	ta.Prompt = "┃ "
	ta.ShowLineNumbers = false
	ta.SetVirtualCursor(false)
	ta.SetHeight(3)
	ta.KeyMap.InsertNewline.SetEnabled(false)
	s := ta.Styles()
	s.Focused.CursorLine = lipglossNoStyle()
	ta.SetStyles(s)
	ta.Focus()

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = statusStyle

	sessionID := cfg.SessionID
	if sessionID == "" && cfg.Runtime != nil {
		sessionID = cfg.Runtime.SessionID()
	}
	approvalMode := cfg.ApprovalMode
	if approvalMode == "" && cfg.Runtime != nil {
		approvalMode = cfg.Runtime.ApprovalMode()
	}
	m := &Model{
		cfg:          cfg,
		vp:           viewport.New(viewport.WithWidth(80), viewport.WithHeight(20)),
		ta:           ta,
		sp:           sp,
		sessionID:    sessionID,
		approvalMode: approvalMode,
	}
	for _, ev := range cfg.History {
		m.applyEvent(ev)
	}
	if len(m.items) == 0 {
		m.items = append(m.items, &item{
			kind: itemInfo,
			text: "aglet ready. Type a request, or /help for commands.",
		})
	}
	m.layout()
	m.refresh()
	return m
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.ta.Focus(), textarea.Blink, m.sp.Tick)
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	case tea.PasteMsg:
		var cmd tea.Cmd
		m.ta, cmd = m.ta.Update(msg)
		return m, cmd
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.sp, cmd = m.sp.Update(msg)
		return m, cmd
	case approvalRequestMsg:
		st := &approvalState{req: msg.req, resp: msg.resp}
		if m.pending == nil {
			m.pending = st
		} else {
			m.queue = append(m.queue, st)
		}
		m.refresh()
		return m, nil
	case eventMsg:
		if msg.run != m.runID {
			return m, nil
		}
		m.applyEvent(msg.ev)
		if msg.ev.Type == agent.EventDone {
			m.busy = false
			if m.cancel != nil {
				m.cancel()
			}
			m.cancel = nil
			m.markNotStreaming()
			m.refresh()
			return m, nil
		}
		m.refresh()
		return m, waitForEvent(m.ch, msg.run)
	case streamClosedMsg:
		if msg.run == m.runID {
			m.busy = false
			m.cancel = nil
			m.markNotStreaming()
			m.refresh()
		}
		return m, nil
	case shellEventMsg:
		if msg.event.run != m.runID {
			return m, nil
		}
		m.busy = false
		m.cancel = nil
		ev := msg.event
		status := "ok"
		if ev.err != nil || ev.timedOut || ev.exitCode != 0 {
			status = "error"
		}
		output := ev.output
		if ev.err != nil {
			output += "\n" + ev.err.Error()
		}
		if ev.timedOut {
			output += "\n[command timed out]"
		}
		m.items = append(m.items, &item{
			kind: itemTool, toolName: "run_command", toolArgs: ev.command,
			toolStatus: status, toolOutput: output,
		})
		m.markNotStreaming()
		m.refresh()
		return m, nil
	case sessionsMsg:
		if msg.err != nil {
			m.items = append(m.items, &item{kind: itemError, text: msg.err.Error()})
		} else if len(msg.infos) == 0 {
			m.items = append(m.items, &item{kind: itemInfo, text: "no sessions"})
		} else {
			var b strings.Builder
			b.WriteString("Sessions (newest first):\n")
			for i := len(msg.infos) - 1; i >= 0; i-- {
				in := msg.infos[i]
				fmt.Fprintf(&b, "  %s  %s  %d events  %s\n", in.ID, in.UpdatedAt.Format("2006-01-02 15:04"), in.Events, in.Preview)
			}
			b.WriteString("Use /resume <id> to switch.")
			m.items = append(m.items, &item{kind: itemInfo, text: b.String()})
		}
		m.refresh()
		return m, nil
	case resumedMsg:
		if msg.err != nil {
			m.items = append(m.items, &item{kind: itemError, text: msg.err.Error()})
			m.refresh()
			return m, nil
		}
		m.sessionID = msg.id
		m.cfg.SessionID = msg.id
		m.items = nil
		m.planSteps = nil
		m.usage = agent.Usage{}
		m.mdRenderer = nil
		m.mdWidth = 0
		m.busy = false
		m.cancel = nil
		if msg.fresh {
			m.items = append(m.items, &item{kind: itemInfo, text: "new session " + msg.id})
		} else {
			for _, ev := range msg.history {
				m.applyEvent(ev)
			}
			m.items = append(m.items, &item{kind: itemInfo, text: "resumed session " + msg.id})
		}
		m.refresh()
		return m, nil
	case diffMsg:
		if msg.err != nil {
			m.items = append(m.items, &item{kind: itemError, text: msg.err.Error()})
		} else if strings.TrimSpace(msg.text) == "" {
			m.items = append(m.items, &item{kind: itemInfo, text: "no uncommitted changes"})
		} else {
			m.items = append(m.items, &item{kind: itemInfo, text: msg.text})
		}
		m.refresh()
		return m, nil
	}
	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg)
	return m, cmd
}

func (m *Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.pending != nil {
		switch msg.String() {
		case "y", "Y":
			m.respondPending(permissions.DecisionAllowOnce)
		case "a", "A":
			m.respondPending(permissions.DecisionAllowSession)
		case "n", "N", "esc":
			m.respondPending(permissions.DecisionDeny)
		case "ctrl+c":
			m.respondPending(permissions.DecisionDeny)
			m.cancelRun()
		}
		return m, nil
	}

	switch msg.String() {
	case "ctrl+c":
		if m.busy {
			m.cancelRun()
			return m, nil
		}
		m.denyAll()
		return m, tea.Quit
	case "esc":
		if m.busy {
			m.cancelRun()
		} else {
			m.ta.Reset()
		}
		return m, nil
	case "enter":
		text := strings.TrimSpace(m.ta.Value())
		if text == "" {
			return m, nil
		}
		if strings.HasPrefix(text, "!") {
			if m.busy {
				return m, nil
			}
			command := strings.TrimSpace(text[1:])
			if command == "" {
				return m, nil
			}
			m.ta.Reset()
			m.items = append(m.items, &item{kind: itemUser, text: "!" + command})
			m.busy = true
			m.markNotStreaming()
			m.refresh()
			return m, m.startShell(command)
		}
		if strings.HasPrefix(text, "/") {
			m.ta.Reset()
			return m.handleSlash(text)
		}
		if m.busy {
			return m, nil
		}
		m.ta.Reset()
		m.items = append(m.items, &item{kind: itemUser, text: text})
		m.busy = true
		m.markNotStreaming()
		m.refresh()
		return m, m.startRun(text)
	case "pgup", "pgdown":
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		return m, cmd
	}
	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg)
	return m, cmd
}

func (m *Model) handleSlash(text string) (tea.Model, tea.Cmd) {
	text = strings.TrimSpace(text)
	command, args, _ := strings.Cut(text, " ")
	args = strings.TrimSpace(args)
	switch command {
	case "/quit", "/exit":
		m.denyAll()
		if m.cancel != nil {
			m.cancel()
		}
		return m, tea.Quit
	case "/clear":
		m.items = nil
		m.planSteps = nil
		m.usage = agent.Usage{}
		m.refresh()
		return m, nil
	case "/help":
		m.items = append(m.items, &item{kind: itemInfo, text: `/help  /clear  /quit
/sessions  /resume <id>  /new
/skills  /agents  /commands  /status  /permissions  /mode <name>
/plan [task]  /mcp  /diff  /review  /model
/name args runs a custom command from .aglet/commands.
!command runs a shell command; Enter sends; Ctrl+J newline; Esc cancels.`})
		m.refresh()
		return m, nil
	case "/sessions":
		m.items = append(m.items, &item{kind: itemInfo, text: "loading sessions..."})
		m.refresh()
		return m, loadSessionsCmd(m.cfg.Runtime)
	case "/resume":
		if m.busy {
			m.items = append(m.items, &item{kind: itemInfo, text: "cancel the current run before switching sessions"})
			m.refresh()
			return m, nil
		}
		if args == "" {
			m.items = append(m.items, &item{kind: itemInfo, text: "usage: /resume <session-id>"})
			m.refresh()
			return m, nil
		}
		m.items = append(m.items, &item{kind: itemInfo, text: "resuming " + args + "..."})
		m.refresh()
		return m, resumeSessionCmd(m.cfg.Runtime, args)
	case "/new":
		if m.busy {
			m.items = append(m.items, &item{kind: itemInfo, text: "cancel the current run before creating a session"})
			m.refresh()
			return m, nil
		}
		m.items = append(m.items, &item{kind: itemInfo, text: "creating new session..."})
		m.refresh()
		return m, newSessionCmd(m.cfg.Runtime)
	case "/skills":
		list := m.cfg.Runtime.Skills()
		if len(list) == 0 {
			m.items = append(m.items, &item{kind: itemInfo, text: "no skills discovered"})
		} else {
			var b strings.Builder
			b.WriteString("Skills:\n")
			for _, s := range list {
				fmt.Fprintf(&b, "  %s — %s\n", s.Name, s.Description)
			}
			m.items = append(m.items, &item{kind: itemInfo, text: strings.TrimRight(b.String(), "\n")})
		}
		m.refresh()
		return m, nil
	case "/agents":
		list := m.cfg.Runtime.Subagents()
		if len(list) == 0 {
			m.items = append(m.items, &item{kind: itemInfo, text: "no subagents"})
		} else {
			var b strings.Builder
			b.WriteString("Subagents:\n")
			for _, def := range list {
				tools := "all tools"
				if len(def.Tools) > 0 {
					tools = strings.Join(def.Tools, ", ")
				}
				fmt.Fprintf(&b, "  %s — %s (tools: %s)\n", def.Name, def.Description, tools)
			}
			b.WriteString("The model invokes these through the task tool.")
			m.items = append(m.items, &item{kind: itemInfo, text: b.String()})
		}
		m.refresh()
		return m, nil
	case "/commands":
		list := m.cfg.Runtime.Commands()
		if len(list) == 0 {
			m.items = append(m.items, &item{kind: itemInfo, text: "no custom commands (add .aglet/commands/*.md)"})
		} else {
			var b strings.Builder
			b.WriteString("Commands:\n")
			for _, cmd := range list {
				hint := ""
				if cmd.ArgumentHint != "" {
					hint = " " + cmd.ArgumentHint
				}
				fmt.Fprintf(&b, "  /%s%s — %s\n", cmd.Name, hint, cmd.Description)
			}
			m.items = append(m.items, &item{kind: itemInfo, text: strings.TrimRight(b.String(), "\n")})
		}
		m.refresh()
		return m, nil
	case "/status":
		m.items = append(m.items, &item{kind: itemInfo, text: fmt.Sprintf(
			"profile: %s\nmodel: %s\nsession: %s\napproval mode: %s\ntokens: %d/%d/%d\nworkspace: %s",
			m.cfg.Profile, m.cfg.ModelName, m.sessionID, m.approvalMode,
			m.usage.Prompt, m.usage.Completion, m.usage.Total, m.cfg.Workspace)})
		m.refresh()
		return m, nil
	case "/permissions":
		m.items = append(m.items, &item{kind: itemInfo, text: "approval mode: " + m.approvalMode +
			"\nuse /mode ask|read-only|auto-edit|full-auto"})
		m.refresh()
		return m, nil
	case "/mode":
		if m.busy {
			m.items = append(m.items, &item{kind: itemInfo, text: "cancel the current run before changing approval mode"})
			m.refresh()
			return m, nil
		}
		if args == "" {
			m.items = append(m.items, &item{kind: itemInfo, text: "approval mode: " + m.approvalMode})
			m.refresh()
			return m, nil
		}
		mode := config.NormalizeApprovalMode(args)
		switch mode {
		case config.ApprovalAsk, config.ApprovalReadOnly, config.ApprovalAutoEdit, config.ApprovalFullAuto:
			m.approvalMode = mode
			m.cfg.Runtime.SetApprovalMode(mode)
			m.items = append(m.items, &item{kind: itemInfo, text: "approval mode: " + mode})
		default:
			m.items = append(m.items, &item{kind: itemError, text: "invalid mode: " + args})
		}
		m.refresh()
		return m, nil
	case "/plan":
		if m.busy {
			m.items = append(m.items, &item{kind: itemInfo, text: "cancel the current run before entering plan mode"})
			m.refresh()
			return m, nil
		}
		m.approvalMode = config.ApprovalReadOnly
		m.cfg.Runtime.SetApprovalMode(config.ApprovalReadOnly)
		if args == "" {
			m.items = append(m.items, &item{kind: itemInfo, text: "plan mode: read-only. Describe a task, or use /plan <task>."})
			m.refresh()
			return m, nil
		}
		prompt := "Create a plan for the following task. Use update_plan and do not edit files.\n\n" + args
		m.items = append(m.items, &item{kind: itemUser, text: "/plan " + args})
		m.busy = true
		m.markNotStreaming()
		m.refresh()
		return m, m.startRun(prompt)
	case "/mcp":
		list := m.cfg.Runtime.MCPTools()
		if len(list) == 0 {
			m.items = append(m.items, &item{kind: itemInfo, text: "no MCP tools"})
		} else {
			m.items = append(m.items, &item{kind: itemInfo, text: "MCP tools:\n  " + strings.Join(list, "\n  ")})
		}
		m.refresh()
		return m, nil
	case "/diff":
		return m, gitDiffCmd(m.cfg.Workspace)
	case "/review":
		if m.busy {
			return m, nil
		}
		m.approvalMode = config.ApprovalReadOnly
		m.cfg.Runtime.SetApprovalMode(config.ApprovalReadOnly)
		prompt := "Review the current uncommitted changes with git_diff. Report correctness issues, risks, and missing tests. Do not edit files."
		m.items = append(m.items, &item{kind: itemUser, text: "/review"})
		m.busy = true
		m.markNotStreaming()
		m.refresh()
		return m, m.startRun(prompt)
	case "/model":
		m.items = append(m.items, &item{kind: itemInfo, text: "profile: " + m.cfg.Profile + "\nmodel: " + m.cfg.ModelName + "\nedit aglet.json to change models"})
		m.refresh()
		return m, nil
	default:
		if cmd, ok := commands.Find(m.cfg.Runtime.Commands(), strings.TrimPrefix(command, "/")); ok {
			if m.busy {
				m.items = append(m.items, &item{kind: itemInfo, text: "cancel the current run before running a command"})
				m.refresh()
				return m, nil
			}
			expanded := commands.Expand(cmd, args)
			m.items = append(m.items, &item{kind: itemUser, text: text})
			m.busy = true
			m.markNotStreaming()
			m.refresh()
			return m, m.startRun(expanded)
		}
		m.items = append(m.items, &item{kind: itemInfo, text: "unknown command: " + text})
		m.refresh()
		return m, nil
	}
}

func (m *Model) startRun(prompt string) tea.Cmd {
	m.runID++
	run := m.runID
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	ch := make(chan agent.Event, 256)
	m.ch = ch

	go func() {
		defer close(ch)
		_ = m.cfg.Runtime.Run(ctx, prompt, func(ev agent.Event) {
			select {
			case ch <- ev:
			case <-ctx.Done():
			}
		})
	}()
	return waitForEvent(ch, run)
}

func (m *Model) startShell(command string) tea.Cmd {
	m.runID++
	run := m.runID
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.shell = startShellRun(m.cfg.Runtime, command, run, ctx)
	return waitShell(m.shell, run)
}

func waitForEvent(ch <-chan agent.Event, run int) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return streamClosedMsg{run: run}
		}
		return eventMsg{run: run, ev: ev}
	}
}

func (m *Model) cancelRun() {
	m.denyAll()
	if m.cancel != nil {
		m.cancel()
	}
	m.cancel = nil
	m.busy = false
	m.runID++
	m.markNotStreaming()
	m.items = append(m.items, &item{kind: itemInfo, text: "cancelled"})
	m.refresh()
}

func (m *Model) respondPending(d permissions.Decision) {
	if m.pending == nil {
		return
	}
	select {
	case m.pending.resp <- d:
	default:
	}
	m.pending = nil
	if len(m.queue) > 0 {
		m.pending = m.queue[0]
		m.queue = m.queue[1:]
	}
	m.refresh()
}

func (m *Model) denyAll() {
	if m.pending != nil {
		select {
		case m.pending.resp <- permissions.DecisionDeny:
		default:
		}
		m.pending = nil
	}
	for _, q := range m.queue {
		select {
		case q.resp <- permissions.DecisionDeny:
		default:
		}
	}
	m.queue = nil
}

func (m *Model) applyEvent(ev agent.Event) {
	switch ev.Type {
	case agent.EventTextDelta:
		if ev.Author == "user" {
			m.items = append(m.items, &item{kind: itemUser, text: ev.Text})
			return
		}
		idx := m.ensureStreaming(itemAssistant)
		m.items[idx].text += ev.Text
	case agent.EventThought:
		if !m.cfg.ShowThoughts {
			return
		}
		idx := m.ensureStreaming(itemThought)
		m.items[idx].text += ev.Text
	case agent.EventToolCall:
		if ev.Call == nil {
			return
		}
		m.markNotStreaming()
		m.items = append(m.items, &item{
			kind:       itemTool,
			toolID:     ev.Call.ID,
			toolName:   ev.Call.Name,
			toolArgs:   compactArgs(ev.Call.Args),
			toolStatus: "running",
		})
	case agent.EventToolResult:
		if ev.Result == nil {
			return
		}
		for _, it := range m.items {
			if it.kind == itemTool && it.toolID == ev.Result.ID {
				it.toolOutput = ev.Result.Text
				if ev.Result.IsError {
					it.toolStatus = "error"
				} else {
					it.toolStatus = "ok"
				}
				break
			}
		}
		m.markNotStreaming()
	case agent.EventUsage:
		if ev.Usage != nil {
			m.usage.Prompt += ev.Usage.Prompt
			m.usage.Completion += ev.Usage.Completion
			m.usage.Total += ev.Usage.Total
		}
	case agent.EventError:
		if ev.Err != nil {
			m.err = ev.Err
			m.items = append(m.items, &item{kind: itemError, text: ev.Err.Error()})
			m.markNotStreaming()
		}
	case agent.EventPlan:
		m.planSteps = append([]plan.Step(nil), ev.Plan...)
		m.updatePlanItem()
		m.markNotStreaming()
	case agent.EventNotice:
		if strings.TrimSpace(ev.Text) != "" {
			m.markNotStreaming()
			m.items = append(m.items, &item{kind: itemInfo, text: ev.Text})
		}
	}
}

func (m *Model) updatePlanItem() {
	for i, it := range m.items {
		if it.kind == itemPlan {
			m.items = append(m.items[:i], m.items[i+1:]...)
			break
		}
	}
	if len(m.planSteps) > 0 {
		m.items = append(m.items, &item{kind: itemPlan})
	}
}

func (m *Model) ensureStreaming(kind itemKind) int {
	if n := len(m.items); n > 0 {
		last := m.items[n-1]
		if last.kind == kind && last.streaming {
			return n - 1
		}
	}
	m.items = append(m.items, &item{kind: kind, streaming: true})
	return len(m.items) - 1
}

func (m *Model) markNotStreaming() {
	for _, it := range m.items {
		it.streaming = false
	}
}

func (m *Model) layout() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	inputH := m.ta.Height()
	if inputH <= 0 {
		inputH = 3
	}
	m.ta.SetWidth(max(20, m.width))
	vpHeight := m.height - inputH - 5
	if vpHeight < 3 {
		vpHeight = 3
	}
	m.vp.SetWidth(max(20, m.width))
	m.vp.SetHeight(vpHeight)
	m.mdRenderer = nil
	m.mdWidth = 0
	for _, it := range m.items {
		it.rendered = ""
		it.renderedKey = ""
	}
	m.refresh()
}

func (m *Model) refresh() {
	m.vp.SetContent(m.renderTranscript())
	m.vp.GotoBottom()
}

func (m *Model) View() tea.View {
	header := m.renderHeader()
	vpView := m.vp.View()
	status := m.renderStatus()
	bottom := m.ta.View()
	if m.pending != nil {
		bottom = m.renderApproval()
	}
	content := strings.Join([]string{header, vpView, status, bottom}, "\n")
	v := tea.NewView(content)
	v.AltScreen = true
	if m.pending == nil {
		if c := m.ta.Cursor(); c != nil {
			c.Y += lipglossHeight(header) + lipglossHeight(vpView) + lipglossHeight(status)
			v.Cursor = c
		}
	}
	return v
}

func compactArgs(v map[string]any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	s := string(b)
	if len(s) > 300 {
		s = s[:300] + "..."
	}
	return s
}
