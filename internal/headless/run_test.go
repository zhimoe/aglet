package headless

import (
	"context"
	"strings"
	"testing"

	"github.com/zhimoe/aglet/internal/permissions"
)

func TestApproverYes(t *testing.T) {
	a := NewApprover(Options{Yes: true, TTY: false})
	d, err := a.Approve(context.Background(), permissions.ApprovalRequest{ToolName: "write_file"})
	if err != nil {
		t.Fatal(err)
	}
	if d != permissions.DecisionAllowOnce {
		t.Fatalf("decision = %v", d)
	}
}

func TestApproverNonTTYDenies(t *testing.T) {
	a := NewApprover(Options{TTY: false})
	_, err := a.Approve(context.Background(), permissions.ApprovalRequest{ToolName: "run_command"})
	if err == nil || !strings.Contains(err.Error(), "not a TTY") {
		t.Fatalf("err = %v", err)
	}
}

func TestApproverDeny(t *testing.T) {
	a := NewApprover(Options{Deny: true, TTY: true})
	_, err := a.Approve(context.Background(), permissions.ApprovalRequest{ToolName: "write_file"})
	if err == nil {
		t.Fatal("expected denial")
	}
}

func TestApproverTTYPrompt(t *testing.T) {
	a := NewApprover(Options{
		TTY: true,
		In:  strings.NewReader("a\n"),
		Err: &strings.Builder{},
	})
	d, err := a.Approve(context.Background(), permissions.ApprovalRequest{ToolName: "write_file"})
	if err != nil {
		t.Fatal(err)
	}
	if d != permissions.DecisionAllowSession {
		t.Fatalf("decision = %v", d)
	}
}
