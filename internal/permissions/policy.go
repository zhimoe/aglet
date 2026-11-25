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
	Summary  string
	Args     map[string]any
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

// Policy applies allow/ask/deny rules and remembers session-level grants.
type Policy struct {
	auto    []string
	require []string
	deny    []string

	mu             sync.Mutex
	sessionAllowed map[string]bool
}

func NewPolicy(cfg config.PermissionsConfig) *Policy {
	return &Policy{
		auto:           append([]string(nil), cfg.AutoApprove...),
		require:        append([]string(nil), cfg.RequireApproval...),
		deny:           append([]string(nil), cfg.Deny...),
		sessionAllowed: map[string]bool{},
	}
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

func (p *Policy) decide(tool string) class {
	if matchAny(p.deny, tool) {
		return classDeny
	}
	if matchAny(p.require, tool) {
		return classAsk
	}
	if matchAny(p.auto, tool) {
		return classAllow
	}
	// MCP tools are sensitive by default; built-in tools are explicit in the
	// default config anyway. Anything unknown asks.
	if strings.HasPrefix(tool, "mcp_") || strings.HasPrefix(tool, "mcp:") {
		return classAsk
	}
	return classAsk
}

// Authorize enforces the policy. For classAsk it calls approver and records a
// session grant when the user chooses "always".
func (p *Policy) Authorize(ctx context.Context, tool, server, summary string, args map[string]any, approver Approver) error {
	switch p.decide(tool) {
	case classDeny:
		return fmt.Errorf("tool %q denied by policy", tool)
	case classAllow:
		return nil
	}

	p.mu.Lock()
	allowed := p.sessionAllowed[tool]
	p.mu.Unlock()
	if allowed {
		return nil
	}
	if approver == nil {
		return fmt.Errorf("tool %q requires approval but no approver is configured", tool)
	}
	d, err := approver.Approve(ctx, ApprovalRequest{
		ToolName: tool,
		Server:   server,
		Summary:  summary,
		Args:     args,
	})
	if err != nil {
		return err
	}
	switch d {
	case DecisionAllowOnce:
		return nil
	case DecisionAllowSession:
		p.mu.Lock()
		p.sessionAllowed[tool] = true
		p.mu.Unlock()
		return nil
	default:
		return fmt.Errorf("tool %q rejected by user", tool)
	}
}

// AllowSession grants a tool for this policy instance. Used by tests and by
// frontends that implement their own "always" handling.
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
