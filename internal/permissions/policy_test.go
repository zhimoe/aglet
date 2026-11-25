package permissions

import (
	"context"
	"testing"

	"github.com/zhimoe/aglet/internal/config"
)

type fakeApprover struct {
	decision Decision
	calls    int
}

func (f *fakeApprover) Approve(context.Context, ApprovalRequest) (Decision, error) {
	f.calls++
	return f.decision, nil
}

func TestPolicyPrecedenceAndSessionGrant(t *testing.T) {
	p := NewPolicy(config.PermissionsConfig{
		AutoApprove:     []string{"read_file", "read_*"},
		RequireApproval: []string{"write_file", "mcp_*"},
		Deny:            []string{"read_secret"},
	})
	ctx := context.Background()

	// auto + no approver
	if err := p.Authorize(ctx, "read_file", "", "", nil, nil); err != nil {
		t.Fatalf("auto read: %v", err)
	}
	// pattern auto
	if err := p.Authorize(ctx, "read_dir", "", "", nil, nil); err != nil {
		t.Fatalf("pattern read: %v", err)
	}
	// deny wins over auto
	if err := p.Authorize(ctx, "read_secret", "", "", nil, nil); err == nil {
		t.Fatal("expected deny")
	}
	// require without approver
	if err := p.Authorize(ctx, "write_file", "", "", nil, nil); err == nil {
		t.Fatal("expected approval error")
	}
	// approver allow once
	a := &fakeApprover{decision: DecisionAllowOnce}
	if err := p.Authorize(ctx, "write_file", "", "", nil, a); err != nil {
		t.Fatal(err)
	}
	if a.calls != 1 {
		t.Fatalf("approver calls = %d", a.calls)
	}
	// second call asks again
	if err := p.Authorize(ctx, "write_file", "", "", nil, a); err != nil {
		t.Fatal(err)
	}
	if a.calls != 2 {
		t.Fatalf("approver calls = %d", a.calls)
	}
	// always allow
	a2 := &fakeApprover{decision: DecisionAllowSession}
	if err := p.Authorize(ctx, "edit_file", "", "", nil, a2); err != nil {
		t.Fatal(err)
	}
	if err := p.Authorize(ctx, "edit_file", "", "", nil, nil); err != nil {
		t.Fatalf("session grant should survive: %v", err)
	}
	// MCP default asks
	if err := p.Authorize(ctx, "mcp_x_y", "", "", nil, nil); err == nil {
		t.Fatal("expected MCP approval error")
	}
	// deny by approver
	a3 := &fakeApprover{decision: DecisionDeny}
	if err := p.Authorize(ctx, "run_command", "", "", nil, a3); err == nil {
		t.Fatal("expected rejection")
	}
}
