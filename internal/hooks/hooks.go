// Package hooks runs user-configured lifecycle hooks for aglet.
package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/zhimoe/aglet/internal/config"
)

// Input is the JSON payload written to a hook command's stdin.
type Input struct {
	HookEventName  string         `json:"hook_event_name"`
	SessionID      string         `json:"session_id"`
	Cwd            string         `json:"cwd"`
	PermissionMode string         `json:"permission_mode,omitempty"`
	Prompt         string         `json:"prompt,omitempty"`
	Source         string         `json:"source,omitempty"`
	ToolName       string         `json:"tool_name,omitempty"`
	ToolInput      map[string]any `json:"tool_input,omitempty"`
	ToolUseID      string         `json:"tool_use_id,omitempty"`
	ToolResponse   map[string]any `json:"tool_response,omitempty"`
	ToolError      string         `json:"tool_error,omitempty"`
	StopHookActive bool           `json:"stop_hook_active,omitempty"`
}

// Output is the optional JSON written to stdout by a hook command.
type Output struct {
	Continue           *bool  `json:"continue"`
	StopReason         string `json:"stopReason"`
	SystemMessage      string `json:"systemMessage"`
	Decision           string `json:"decision"`
	Reason             string `json:"reason"`
	HookSpecificOutput struct {
		HookEventName            string `json:"hookEventName"`
		PermissionDecision       string `json:"permissionDecision"`
		PermissionDecisionReason string `json:"permissionDecisionReason"`
		AdditionalContext        string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// Result aggregates one event's hook executions.
type Result struct {
	Blocked            bool
	Reason             string
	PermissionDecision string
	AdditionalContext  string
	SystemMessages     []string
	Warnings           []string
}

// Request is one hook command invocation.
type Request struct {
	Command   string
	Workspace string
	Env       []string
	Stdin     []byte
	Timeout   time.Duration
}

// Executor runs a hook command. Tests can replace the default implementation.
type Executor interface {
	Run(ctx context.Context, req Request) (stdout, stderr []byte, exitCode int, err error)
}

// Options configures a Runner.
type Options struct {
	Workspace      string
	SessionID      func() string
	PermissionMode func() string
	Logger         *slog.Logger
	Executor       Executor
}

type matcher struct {
	re    *regexp.Regexp
	hooks []config.HookCommandConfig
}

// Runner executes configured hooks. It is safe for concurrent use.
type Runner struct {
	matchers map[string][]matcher
	opts     Options
}

// New compiles the hook configuration. Invalid input is reported by
// config.Validate; New simply skips entries that cannot be compiled.
func New(cfg config.HooksConfig, opts Options) *Runner {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Executor == nil {
		opts.Executor = execExecutor{}
	}
	r := &Runner{matchers: map[string][]matcher{}, opts: opts}
	for name, groups := range cfg {
		event, ok := config.CanonicalHookEvent(name)
		if !ok {
			continue
		}
		for _, group := range groups {
			m := matcher{hooks: group.Hooks}
			if group.Matcher != "" {
				re, err := regexp.Compile("^(?:" + group.Matcher + ")$")
				if err != nil {
					opts.Logger.Warn("hook matcher skipped", "event", event, "error", err)
					continue
				}
				m.re = re
			}
			r.matchers[event] = append(r.matchers[event], m)
		}
	}
	return r
}

// Has reports whether an event has configured hooks.
func (r *Runner) Has(event string) bool {
	if r == nil {
		return false
	}
	canonical, ok := config.CanonicalHookEvent(event)
	if !ok {
		return false
	}
	return len(r.matchers[canonical]) > 0
}

// HasToolEvents reports whether PreToolUse or PostToolUse hooks exist.
func (r *Runner) HasToolEvents() bool {
	return r.Has(config.HookPreToolUse) || r.Has(config.HookPostToolUse)
}

// Run executes every matching hook for an event.
func (r *Runner) Run(ctx context.Context, event string, in Input) Result {
	var res Result
	if r == nil {
		return res
	}
	canonical, ok := config.CanonicalHookEvent(event)
	if !ok {
		return res
	}
	groups := r.matchers[canonical]
	if len(groups) == 0 {
		return res
	}
	in.HookEventName = canonical
	if in.SessionID == "" && r.opts.SessionID != nil {
		in.SessionID = r.opts.SessionID()
	}
	if in.Cwd == "" {
		in.Cwd = r.opts.Workspace
	}
	if in.PermissionMode == "" && r.opts.PermissionMode != nil {
		in.PermissionMode = r.opts.PermissionMode()
	}
	matchValue := ""
	switch canonical {
	case config.HookPreToolUse, config.HookPostToolUse:
		matchValue = in.ToolName
	case config.HookSessionStart:
		matchValue = in.Source
	}
	for _, group := range groups {
		if group.re != nil && !group.re.MatchString(matchValue) {
			continue
		}
		for _, hook := range group.hooks {
			if hook.Type != "" && hook.Type != "command" {
				res.Warnings = append(res.Warnings, fmt.Sprintf("unsupported hook type %q", hook.Type))
				continue
			}
			out, stderr, exitCode, err := r.runOne(ctx, canonical, hook, in)
			if err != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("%s hook failed: %v", canonical, err))
				continue
			}
			if exitCode == 2 {
				res.Blocked = true
				res.Reason = reasonText(stderr, "blocked by "+canonical+" hook")
				return res
			}
			if exitCode != 0 {
				res.Warnings = append(res.Warnings, fmt.Sprintf("%s hook exited %d", canonical, exitCode))
				continue
			}
			r.applyOutput(&res, canonical, out)
			if res.Blocked {
				return res
			}
		}
	}
	return res
}

func (r *Runner) runOne(ctx context.Context, event string, hook config.HookCommandConfig, in Input) (Output, []byte, int, error) {
	var out Output
	payload, err := marshalInput(in)
	if err != nil {
		return out, nil, 0, err
	}
	timeout := time.Duration(hook.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	req := Request{
		Command:   hook.Command,
		Workspace: r.opts.Workspace,
		Env: []string{
			"AGLET_HOOK_EVENT=" + event,
			"AGLET_SESSION_ID=" + in.SessionID,
			"AGLET_WORKSPACE=" + r.opts.Workspace,
			"AGLET_TOOL_NAME=" + in.ToolName,
		},
		Stdin:   payload,
		Timeout: timeout,
	}
	stdout, stderr, exitCode, err := r.opts.Executor.Run(ctx, req)
	if err != nil {
		return out, stderr, exitCode, err
	}
	trimmed := strings.TrimSpace(string(stdout))
	if trimmed != "" {
		if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
			r.opts.Logger.Warn("hook stdout is not JSON", "event", event, "error", err)
		}
	}
	return out, stderr, exitCode, nil
}

func (r *Runner) applyOutput(res *Result, event string, out Output) {
	if out.Continue != nil && !*out.Continue {
		res.Blocked = true
		res.Reason = reasonText([]byte(out.StopReason), "hook requested to stop")
		return
	}
	if strings.EqualFold(out.Decision, "block") {
		res.Blocked = true
		res.Reason = reasonText([]byte(out.Reason), "blocked by "+event+" hook")
		return
	}
	if msg := strings.TrimSpace(out.SystemMessage); msg != "" {
		res.SystemMessages = append(res.SystemMessages, truncateString(msg, 4096))
	}
	decision := strings.ToLower(strings.TrimSpace(out.HookSpecificOutput.PermissionDecision))
	if decision == "deny" {
		res.Blocked = true
		res.Reason = reasonText([]byte(out.HookSpecificOutput.PermissionDecisionReason), "denied by "+event+" hook")
		return
	}
	if decision != "" {
		res.PermissionDecision = decision
	}
	if ctx := strings.TrimSpace(out.HookSpecificOutput.AdditionalContext); ctx != "" {
		if res.AdditionalContext == "" {
			res.AdditionalContext = truncateString(ctx, 16*1024)
		} else if len(res.AdditionalContext) < 32*1024 {
			res.AdditionalContext += "\n" + truncateString(ctx, 16*1024)
			res.AdditionalContext = truncateString(res.AdditionalContext, 32*1024)
		}
	}
}

func marshalInput(in Input) ([]byte, error) {
	in.ToolInput = sanitizeMap(in.ToolInput, 0)
	in.ToolResponse = sanitizeMap(in.ToolResponse, 0)
	b, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	if len(b) > 64*1024 {
		in.ToolInput = map[string]any{"_truncated": true}
		in.ToolResponse = nil
		b, err = json.Marshal(in)
		if err != nil {
			return nil, err
		}
	}
	return append(b, '\n'), nil
}

func sanitizeMap(in map[string]any, depth int) map[string]any {
	if in == nil {
		return nil
	}
	if depth > 4 {
		return map[string]any{"_truncated": true}
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = sanitizeValue(v, depth+1)
	}
	return out
}

func sanitizeValue(v any, depth int) any {
	switch value := v.(type) {
	case string:
		return truncateString(value, 8*1024)
	case map[string]any:
		return sanitizeMap(value, depth)
	case []any:
		if depth > 4 {
			return []any{"_truncated"}
		}
		out := make([]any, 0, len(value))
		for _, item := range value {
			out = append(out, sanitizeValue(item, depth+1))
		}
		return out
	default:
		return v
	}
}

func reasonText(stderr []byte, fallback string) string {
	text := strings.TrimSpace(string(stderr))
	if text == "" {
		return fallback
	}
	return truncateString(text, 2048)
}

func truncateString(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
