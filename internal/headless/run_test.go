package headless

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/zhimoe/aglet/internal/agent"
	"github.com/zhimoe/aglet/internal/permissions"
	"github.com/zhimoe/aglet/internal/plan"
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

func TestEventJSONPlan(t *testing.T) {
	b, err := json.Marshal(eventJSON(agent.Event{
		Type: agent.EventPlan,
		Plan: []plan.Step{{Step: "one", Status: plan.StatusInProgress}, {Step: "two", Status: plan.StatusPending}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `"type":"plan"`) || !strings.Contains(s, `"step":"one"`) || !strings.Contains(s, `"status":"in_progress"`) {
		t.Fatalf("json = %s", s)
	}
}

func TestEventJSONNotice(t *testing.T) {
	b, err := json.Marshal(eventJSON(agent.Event{Type: agent.EventNotice, Text: "hello"}))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `"type":"notice"`) || !strings.Contains(s, `"text":"hello"`) {
		t.Fatalf("json = %s", s)
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
