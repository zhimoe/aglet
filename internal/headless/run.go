package headless

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/zhimoe/aglet/internal/agent"
	"github.com/zhimoe/aglet/internal/permissions"
)

type Options struct {
	Out          io.Writer
	Err          io.Writer
	In           io.Reader
	JSON         bool
	ShowThoughts bool
	TTY          bool
	Yes          bool
	Deny         bool
}

func (o *Options) normalize() {
	if o.Out == nil {
		o.Out = os.Stdout
	}
	if o.Err == nil {
		o.Err = os.Stderr
	}
	if o.In == nil {
		o.In = os.Stdin
	}
}

// Approver implements permissions.Approver for non-interactive and
// stdin-prompted use.
type Approver struct {
	opts Options
	rd   *bufio.Reader
}

func NewApprover(opts Options) *Approver {
	opts.normalize()
	return &Approver{opts: opts, rd: bufio.NewReader(opts.In)}
}

func (a *Approver) Approve(ctx context.Context, req permissions.ApprovalRequest) (permissions.Decision, error) {
	if a.opts.Yes {
		return permissions.DecisionAllowOnce, nil
	}
	if a.opts.Deny {
		return permissions.DecisionDeny, fmt.Errorf("denied by --deny")
	}
	if !a.opts.TTY {
		return permissions.DecisionDeny, fmt.Errorf("approval required for %s, but stdin is not a TTY (use --yes to auto-approve or --deny to reject)", req.ToolName)
	}
	if err := ctx.Err(); err != nil {
		return permissions.DecisionDeny, err
	}
	fmt.Fprintf(a.opts.Err, "\n[approval] %s", req.ToolName)
	if req.Server != "" {
		fmt.Fprintf(a.opts.Err, " (%s)", req.Server)
	}
	fmt.Fprintln(a.opts.Err)
	if req.Summary != "" {
		fmt.Fprintf(a.opts.Err, "  %s\n", req.Summary)
	}
	if len(req.Args) > 0 {
		if b, err := json.MarshalIndent(req.Args, "  ", "  "); err == nil {
			s := string(b)
			if len(s) > 4000 {
				s = s[:4000] + "..."
			}
			fmt.Fprintln(a.opts.Err, "  "+strings.ReplaceAll(s, "\n", "\n  "))
		}
	}
	fmt.Fprint(a.opts.Err, "Allow? [y]es once / [n]o / [a]lways this session: ")
	line, err := a.rd.ReadString('\n')
	if err != nil && len(line) == 0 {
		return permissions.DecisionDeny, fmt.Errorf("read approval: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return permissions.DecisionAllowOnce, nil
	case "a", "always":
		return permissions.DecisionAllowSession, nil
	default:
		return permissions.DecisionDeny, fmt.Errorf("tool %q rejected by user", req.ToolName)
	}
}

// Run streams normalized events to the configured writers.
func Run(ctx context.Context, rt *agent.Runtime, prompt string, opts Options) error {
	opts.normalize()
	if opts.JSON {
		emitJSON(opts.Out, map[string]any{"type": "session", "session_id": rt.SessionID()})
	} else {
		fmt.Fprintf(opts.Err, "session: %s\n", rt.SessionID())
	}
	wroteText := false
	endedWithNewline := true
	emit := func(ev agent.Event) {
		if opts.JSON {
			emitJSON(opts.Out, eventJSON(ev))
			return
		}
		switch ev.Type {
		case agent.EventTextDelta:
			fmt.Fprint(opts.Out, ev.Text)
			wroteText = true
			endedWithNewline = strings.HasSuffix(ev.Text, "\n")
		case agent.EventThought:
			if opts.ShowThoughts {
				fmt.Fprintf(opts.Err, "[thought] %s\n", ev.Text)
			}
		case agent.EventToolCall:
			fmt.Fprintf(opts.Err, "[tool] %s %s\n", ev.Call.Name, compactJSON(ev.Call.Args, 1200))
		case agent.EventToolResult:
			status := "ok"
			if ev.Result.IsError {
				status = "error"
			}
			fmt.Fprintf(opts.Err, "[tool:%s] %s\n", status, truncate(ev.Result.Text, 4000))
		case agent.EventUsage:
			if ev.Usage != nil {
				fmt.Fprintf(opts.Err, "[usage] prompt=%d completion=%d total=%d\n", ev.Usage.Prompt, ev.Usage.Completion, ev.Usage.Total)
			}
		case agent.EventError:
			if ev.Err != nil {
				fmt.Fprintf(opts.Err, "[error] %v\n", ev.Err)
			}
		}
	}
	err := rt.Run(ctx, prompt, emit)
	if wroteText && !endedWithNewline {
		fmt.Fprintln(opts.Out)
	}
	return err
}

func eventJSON(ev agent.Event) map[string]any {
	m := map[string]any{"type": string(ev.Type)}
	if ev.Author != "" {
		m["author"] = ev.Author
	}
	if ev.Text != "" {
		m["text"] = ev.Text
	}
	if ev.Call != nil {
		m["tool_call"] = map[string]any{"id": ev.Call.ID, "name": ev.Call.Name, "args": ev.Call.Args}
	}
	if ev.Result != nil {
		m["tool_result"] = map[string]any{
			"id": ev.Result.ID, "name": ev.Result.Name, "result": ev.Result.Result,
			"is_error": ev.Result.IsError, "text": ev.Result.Text,
		}
	}
	if ev.Usage != nil {
		m["usage"] = map[string]any{
			"prompt": ev.Usage.Prompt, "completion": ev.Usage.Completion, "total": ev.Usage.Total,
		}
	}
	if ev.Err != nil {
		m["error"] = ev.Err.Error()
	}
	return m
}

func emitJSON(w io.Writer, v any) {
	enc := json.NewEncoder(w)
	_ = enc.Encode(v)
}

func compactJSON(v any, max int) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return truncate(string(b), max)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n...[truncated]"
}
