package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"

	"github.com/zhimoe/aglet/internal/plan"
)

var (
	headerStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	userStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("4"))
	thoughtStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Italic(true)
	toolStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
	dimStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	errorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	infoStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	statusStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	approvalStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("3")).
			Padding(0, 1)
)

func lipglossNoStyle() lipgloss.Style { return lipgloss.NewStyle() }

func lipglossHeight(s string) int { return lipgloss.Height(s) }

func (m *Model) renderTranscript() string {
	var b strings.Builder
	for _, it := range m.items {
		switch it.kind {
		case itemUser:
			b.WriteString(userStyle.Render("you: ") + it.text)
		case itemAssistant:
			if it.streaming {
				b.WriteString(it.text)
			} else {
				b.WriteString(m.renderMarkdown(it))
			}
		case itemThought:
			if !m.cfg.ShowThoughts {
				continue
			}
			b.WriteString(thoughtStyle.Render("thinking: " + it.text))
		case itemTool:
			status := it.toolStatus
			if status == "" {
				status = "pending"
			}
			b.WriteString(toolStyle.Render(fmt.Sprintf("⚙ %s [%s] %s", it.toolName, status, it.toolArgs)))
			if it.toolOutput != "" {
				b.WriteString("\n" + dimStyle.Render(truncatePlain(it.toolOutput, 4000)))
			}
		case itemError:
			b.WriteString(errorStyle.Render("error: " + it.text))
		case itemInfo:
			b.WriteString(infoStyle.Render(it.text))
		case itemPlan:
			b.WriteString(m.renderPlan())
		}
		b.WriteString("\n\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m *Model) renderHeader() string {
	sid := m.sessionID
	if len(sid) > 8 {
		sid = sid[:8]
	}
	s := fmt.Sprintf("aglet  %s/%s  session %s  mode %s  %s", m.cfg.Profile, m.cfg.ModelName, sid, m.approvalMode, m.cfg.Workspace)
	if m.width > 0 {
		s = truncatePlain(s, m.width-1)
	}
	return headerStyle.Render(s)
}

func (m *Model) renderStatus() string {
	var s string
	switch {
	case m.pending != nil:
		s = "approval required · y allow once · a always · n/esc deny"
	case m.busy:
		s = m.sp.View() + " working · esc cancel"
	default:
		s = "enter send · ctrl+j newline · esc clear · ctrl+c quit"
	}
	if n := len(m.planSteps); n > 0 {
		done := 0
		for _, st := range m.planSteps {
			if st.Status == plan.StatusCompleted {
				done++
			}
		}
		s += fmt.Sprintf(" · plan %d/%d", done, n)
	}
	if m.usage.Total > 0 {
		s += fmt.Sprintf(" · tokens %d/%d/%d", m.usage.Prompt, m.usage.Completion, m.usage.Total)
	}
	if m.width > 0 {
		s = truncatePlain(s, m.width-1)
	}
	return statusStyle.Render(s)
}

func (m *Model) renderMarkdown(it *item) string {
	if strings.TrimSpace(it.text) == "" {
		return ""
	}
	width := max(20, m.width-4)
	key := fmt.Sprintf("%d:%s", width, it.text)
	if it.renderedKey == key && it.rendered != "" {
		return it.rendered
	}
	if m.mdRenderer == nil || m.mdWidth != width {
		r, err := glamour.NewTermRenderer(
			glamour.WithStylePath("dark"),
			glamour.WithWordWrap(width),
		)
		if err != nil {
			return it.text
		}
		m.mdRenderer = r
		m.mdWidth = width
	}
	out, err := m.mdRenderer.Render(it.text)
	if err != nil {
		return it.text
	}
	out = strings.TrimRight(out, "\n")
	it.rendered = out
	it.renderedKey = key
	return out
}

func (m *Model) renderPlan() string {
	if len(m.planSteps) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Plan:\n")
	for i, step := range m.planSteps {
		mark := "[ ]"
		style := statusStyle
		switch step.Status {
		case plan.StatusInProgress:
			mark = "[~]"
			style = infoStyle
		case plan.StatusCompleted:
			mark = "[x]"
			style = dimStyle
		}
		b.WriteString(style.Render(fmt.Sprintf("  %s %d. %s", mark, i+1, step.Step)))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m *Model) renderApproval() string {
	if m.pending == nil {
		return ""
	}
	req := m.pending.req
	var head strings.Builder
	fmt.Fprintf(&head, "Approval required: %s", req.ToolName)
	if req.Server != "" {
		fmt.Fprintf(&head, "  (server: %s)", req.Server)
	}
	if req.Agent != "" && req.Agent != "aglet" {
		fmt.Fprintf(&head, "  (from %s)", req.Agent)
	}
	if req.Summary != "" {
		head.WriteString("\n" + req.Summary)
	}
	if len(req.Args) > 0 {
		head.WriteString("\n" + prettyArgs(req.Args))
	}
	width := 60
	if m.width > 4 {
		width = m.width - 2
	}
	innerWidth := max(20, width-4)
	lines := strings.Split(head.String(), "\n")
	for i := range lines {
		lines[i] = truncatePlain(lines[i], innerWidth)
	}
	if req.Diff != "" {
		lines = append(lines, colorDiff(req.Diff, innerWidth))
	}
	lines = append(lines, "[y] allow once   [a] always this session   [n]/esc deny")
	return approvalStyle.Width(width).Render(strings.Join(lines, "\n"))
}

func colorDiff(diff string, width int) string {
	lines := strings.Split(strings.TrimRight(diff, "\n"), "\n")
	if len(lines) > 80 {
		lines = append(lines[:80], "... (diff truncated)")
	}
	var b strings.Builder
	b.WriteString("diff:\n")
	for _, line := range lines {
		line = truncatePlain(line, width)
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
			b.WriteString(headerStyle.Render(line))
		case strings.HasPrefix(line, "@@"):
			b.WriteString(infoStyle.Render(line))
		case strings.HasPrefix(line, "+"):
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Render(line))
		case strings.HasPrefix(line, "-"):
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Render(line))
		default:
			b.WriteString(dimStyle.Render(line))
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func prettyArgs(v map[string]any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprint(v)
	}
	s := string(b)
	if len(s) > 1200 {
		s = s[:1200] + "..."
	}
	return s
}

func truncatePlain(s string, n int) string {
	if n <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

func truncateLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = truncatePlain(lines[i], n)
	}
	return strings.Join(lines, "\n")
}
