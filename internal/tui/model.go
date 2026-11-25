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

	"github.com/zhimoe/aglet/internal/agent"
	"github.com/zhimoe/aglet/internal/permissions"
)

// Config is the immutable setup passed to the TUI model.
type Config struct {
	Runtime      *agent.Runtime
	Profile      string
	ModelName    string
	SessionID    string
	Workspace    string
	ShowThoughts bool
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
)

type item struct {
	kind       itemKind
	text       string
	streaming  bool
	toolID     string
	toolName   string
	toolArgs   string
	toolStatus string
	toolOutput string
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

	items   []*item
	usage   agent.Usage
	pending *approvalState
	queue   []*approvalState

	busy   bool
	runID  int
	ch     <-chan agent.Event
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

	m := &Model{
		cfg: cfg,
		vp:  viewport.New(viewport.WithWidth(80), viewport.WithHeight(20)),
		ta:  ta,
		sp:  sp,
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
	switch strings.TrimSpace(text) {
	case "/quit", "/exit":
		m.denyAll()
		if m.cancel != nil {
			m.cancel()
		}
		return m, tea.Quit
	case "/clear":
		m.items = nil
		m.refresh()
		return m, nil
	case "/help":
		m.items = append(m.items, &item{kind: itemInfo, text: "/help  /clear  /quit\nEnter sends, Ctrl+J inserts a newline, Esc cancels a running turn."})
		m.refresh()
		return m, nil
	default:
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
