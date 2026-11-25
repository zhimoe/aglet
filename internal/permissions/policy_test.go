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

func req(tool string) ApprovalRequest { return ApprovalRequest{ToolName: tool} }

func TestPolicyPrecedenceAndSessionGrant(t *testing.T) {
	p := NewPolicy(config.PermissionsConfig{
		AutoApprove:     []string{"read_file", "read_*"},
		RequireApproval: []string{"write_file", "mcp_*"},
		Deny:            []string{"read_secret"},
	})
	ctx := context.Background()

	if err := p.Authorize(ctx, req("read_file"), nil); err != nil {
		t.Fatalf("auto read: %v", err)
	}
	if err := p.Authorize(ctx, req("read_dir"), nil); err != nil {
		t.Fatalf("pattern read: %v", err)
	}
	if err := p.Authorize(ctx, req("read_secret"), nil); err == nil {
		t.Fatal("expected deny")
	}
	if err := p.Authorize(ctx, req("write_file"), nil); err == nil {
		t.Fatal("expected approval error")
	}
	a := &fakeApprover{decision: DecisionAllowOnce}
	if err := p.Authorize(ctx, req("write_file"), a); err != nil {
		t.Fatal(err)
	}
	if a.calls != 1 {
		t.Fatalf("approver calls = %d", a.calls)
	}
	if err := p.Authorize(ctx, req("write_file"), a); err != nil {
		t.Fatal(err)
	}
	if a.calls != 2 {
		t.Fatalf("approver calls = %d", a.calls)
	}
	a2 := &fakeApprover{decision: DecisionAllowSession}
	if err := p.Authorize(ctx, req("edit_file"), a2); err != nil {
		t.Fatal(err)
	}
	if err := p.Authorize(ctx, req("edit_file"), nil); err != nil {
		t.Fatalf("session grant should survive: %v", err)
	}
	if err := p.Authorize(ctx, req("mcp_x_y"), nil); err == nil {
		t.Fatal("expected MCP approval error")
	}
	a3 := &fakeApprover{decision: DecisionDeny}
	if err := p.Authorize(ctx, req("run_command"), a3); err == nil {
		t.Fatal("expected rejection")
	}
}

func TestApprovalModes(t *testing.T) {
	cfg := config.PermissionsConfig{
		AutoApprove:     []string{"read_file"},
		RequireApproval: []string{"write_file", "run_command", "mcp_*"},
		Deny:            []string{"run_forbidden"},
	}
	ctx := context.Background()

	ro := NewPolicy(cfg, config.ApprovalReadOnly)
	if err := ro.Authorize(ctx, req("read_file"), nil); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"write_file", "run_command", "mcp_x_y"} {
		if err := ro.Authorize(ctx, req(tool), &fakeApprover{decision: DecisionAllowOnce}); err == nil {
			t.Fatalf("read-only should deny %s", tool)
		}
	}

	auto := NewPolicy(cfg, "accept-edits")
	if err := auto.Authorize(ctx, req("write_file"), nil); err != nil {
		t.Fatalf("auto-edit write: %v", err)
	}
	if err := auto.Authorize(ctx, req("run_command"), nil); err == nil {
		t.Fatal("auto-edit should ask for command")
	}

	full := NewPolicy(cfg, config.ApprovalFullAuto)
	for _, tool := range []string{"write_file", "run_command", "mcp_x_y"} {
		if err := full.Authorize(ctx, req(tool), nil); err != nil {
			t.Fatalf("full-auto %s: %v", tool, err)
		}
	}
	if err := full.Authorize(ctx, req("run_forbidden"), nil); err == nil {
		t.Fatal("explicit deny must win over full-auto")
	}

	full.SetMode(config.ApprovalAsk)
	if full.Mode() != config.ApprovalAsk {
		t.Fatalf("mode = %q", full.Mode())
	}
}
