package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
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
			b.WriteString(it.text)
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
		}
		b.WriteString("\n\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m *Model) renderHeader() string {
	sid := m.cfg.SessionID
	if len(sid) > 8 {
		sid = sid[:8]
	}
	s := fmt.Sprintf("aglet  %s/%s  session %s  %s", m.cfg.Profile, m.cfg.ModelName, sid, m.cfg.Workspace)
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
	if m.usage.Total > 0 {
		s += fmt.Sprintf(" · tokens %d/%d/%d", m.usage.Prompt, m.usage.Completion, m.usage.Total)
	}
	if m.width > 0 {
		s = truncatePlain(s, m.width-1)
	}
	return statusStyle.Render(s)
}

func (m *Model) renderApproval() string {
	if m.pending == nil {
		return ""
	}
	req := m.pending.req
	var b strings.Builder
	fmt.Fprintf(&b, "Approval required: %s", req.ToolName)
	if req.Server != "" {
		fmt.Fprintf(&b, "  (server: %s)", req.Server)
	}
	if req.Summary != "" {
		b.WriteString("\n" + req.Summary)
	}
	if len(req.Args) > 0 {
		b.WriteString("\n" + prettyArgs(req.Args))
	}
	b.WriteString("\n[y] allow once   [a] always this session   [n]/esc deny")
	width := 60
	if m.width > 4 {
		width = m.width - 2
	}
	return approvalStyle.Width(width).Render(truncateLines(b.String(), max(20, width-4)))
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
