package permissions

import (
	"context"
	"fmt"
	"path"
	"strings"
	"sync"

	"github.com/zhimoe/aglet/internal/config"
)

// Decision is the user's answer to an approval request.
type Decision int

const (
	DecisionDeny Decision = iota
	DecisionAllowOnce
	DecisionAllowSession
)

// ApprovalRequest describes a tool call that needs human approval.
type ApprovalRequest struct {
	ToolName string
	Server   string // MCP server name; empty for local tools
	Agent    string // agent or subagent requesting the call; empty when unknown
	Summary  string
	Args     map[string]any
	Diff     string
}

// Approver asks the frontend (headless stdin or TUI modal) for a decision.
type Approver interface {
	Approve(ctx context.Context, req ApprovalRequest) (Decision, error)
}

type class int

const (
	classAllow class = iota
	classAsk
	classDeny
)

// Policy applies allow/ask/deny rules, an approval mode, and session grants.
type Policy struct {
	auto    []string
	require []string
	deny    []string
	mode    string

	mu             sync.Mutex
	sessionAllowed map[string]bool
}

func NewPolicy(cfg config.PermissionsConfig, modes ...string) *Policy {
	mode := config.ApprovalAsk
	if len(modes) > 0 && modes[0] != "" {
		mode = config.NormalizeApprovalMode(modes[0])
	}
	return &Policy{
		auto:           append([]string(nil), cfg.AutoApprove...),
		require:        append([]string(nil), cfg.RequireApproval...),
		deny:           append([]string(nil), cfg.Deny...),
		mode:           mode,
		sessionAllowed: map[string]bool{},
	}
}

func (p *Policy) Mode() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.mode
}

func (p *Policy) SetMode(mode string) {
	mode = config.NormalizeApprovalMode(mode)
	p.mu.Lock()
	p.mode = mode
	p.mu.Unlock()
}

func matchAny(patterns []string, name string) bool {
	for _, p := range patterns {
		ok, err := path.Match(p, name)
		if err == nil && ok {
			return true
		}
		if p == name {
			return true
		}
	}
	return false
}

var readOnlyTools = map[string]bool{
	"read_file":   true,
	"list_dir":    true,
	"glob":        true,
	"grep":        true,
	"git_status":  true,
	"git_diff":    true,
	"git_log":     true,
	"load_skill":  true,
	"update_plan": true,
	"task":        true,
	"web_fetch":   true,
	"web_search":  true,
	"bash_output": true,
	"kill_shell":  true,
}

func (p *Policy) decide(req ApprovalRequest) class {
	if matchAny(p.deny, req.ToolName) {
		return classDeny
	}
	switch p.Mode() {
	case config.ApprovalReadOnly:
		if readOnlyTools[req.ToolName] {
			return classAllow
		}
		return classDeny
	case config.ApprovalAutoEdit:
		if readOnlyTools[req.ToolName] || req.ToolName == "write_file" || req.ToolName == "edit_file" {
			return classAllow
		}
		return classAsk
	case config.ApprovalFullAuto:
		return classAllow
	}
	if matchAny(p.require, req.ToolName) {
		return classAsk
	}
	if matchAny(p.auto, req.ToolName) {
		return classAllow
	}
	if strings.HasPrefix(req.ToolName, "mcp_") || strings.HasPrefix(req.ToolName, "mcp:") {
		return classAsk
	}
	return classAsk
}

// Authorize enforces the policy. For classAsk it calls approver and records a
// session grant when the user chooses "always".
func (p *Policy) Authorize(ctx context.Context, req ApprovalRequest, approver Approver) error {
	switch p.decide(req) {
	case classDeny:
		return fmt.Errorf("tool %q denied by policy (mode %s)", req.ToolName, p.Mode())
	case classAllow:
		return nil
	}

	p.mu.Lock()
	allowed := p.sessionAllowed[req.ToolName]
	p.mu.Unlock()
	if allowed {
		return nil
	}
	if approver == nil {
		return fmt.Errorf("tool %q requires approval but no approver is configured", req.ToolName)
	}
	d, err := approver.Approve(ctx, req)
	if err != nil {
		return err
	}
	switch d {
	case DecisionAllowOnce:
		return nil
	case DecisionAllowSession:
		p.mu.Lock()
		p.sessionAllowed[req.ToolName] = true
		p.mu.Unlock()
		return nil
	default:
		return fmt.Errorf("tool %q rejected by user", req.ToolName)
	}
}

// AllowSession grants a tool for this policy instance.
func (p *Policy) AllowSession(tool string) {
	p.mu.Lock()
	p.sessionAllowed[tool] = true
	p.mu.Unlock()
}

// AutoApprover is useful for tests and explicit --yes mode.
type AutoApprover struct{}

func (AutoApprover) Approve(context.Context, ApprovalRequest) (Decision, error) {
	return DecisionAllowOnce, nil
}
