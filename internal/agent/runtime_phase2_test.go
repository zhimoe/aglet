package agent

import (
	"context"
	"iter"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"

	"github.com/zhimoe/aglet/internal/config"
	"github.com/zhimoe/aglet/internal/permissions"
	"github.com/zhimoe/aglet/internal/plan"
)

type planLLM struct {
	mu    sync.Mutex
	calls int
}

func (f *planLLM) Name() string { return "plan-fake" }

func (f *planLLM) GenerateContent(_ context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		f.mu.Lock()
		f.calls++
		n := f.calls
		f.mu.Unlock()
		if n == 1 {
			yield(&model.LLMResponse{
				Content: &genai.Content{Role: "model", Parts: []*genai.Part{{
					FunctionCall: &genai.FunctionCall{
						ID: "plan_1", Name: "update_plan",
						Args: map[string]any{"plan": []any{
							map[string]any{"step": "inspect", "status": "in_progress"},
							map[string]any{"step": "answer", "status": "pending"},
						}},
					},
				}}},
				TurnComplete: true,
			}, nil)
			return
		}
		yield(&model.LLMResponse{
			Content:      &genai.Content{Role: "model", Parts: []*genai.Part{{Text: "plan ready"}}},
			TurnComplete: true,
		}, nil)
	}
}

func TestRuntimePlanAndSessionSwitch(t *testing.T) {
	cfg := config.Defaults()
	cfg.Workspace = t.TempDir()
	cfg.SessionDB = filepath.Join(t.TempDir(), "sessions.db")
	cfg.MCPServers = map[string]config.MCPServerConfig{}
	cfg.Agent.MaxLLMCalls = 10
	policy := permissions.NewPolicy(cfg.Permissions)
	llm := &planLLM{}
	rt, err := New(context.Background(), cfg, Options{
		Profile: "deepseek", Workspace: cfg.Workspace, Policy: policy, ModelLLM: llm,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	var planEvents []plan.Step
	if err := rt.Run(context.Background(), "make a plan", func(ev Event) {
		if ev.Type == EventPlan {
			planEvents = ev.Plan
		}
	}); err != nil {
		t.Fatal(err)
	}
	if len(planEvents) != 2 || !slices.ContainsFunc(planEvents, func(s plan.Step) bool { return s.Status == plan.StatusInProgress }) {
		t.Fatalf("plan events = %#v", planEvents)
	}
	oldID := rt.SessionID()
	newID, err := rt.SetSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if newID == oldID {
		t.Fatal("expected a new session id")
	}
	history, err := rt.HistoryFor(context.Background(), oldID)
	if err != nil {
		t.Fatal(err)
	}
	foundUser := false
	for _, ev := range history {
		if ev.Author == "user" && ev.Text == "make a plan" {
			foundUser = true
		}
	}
	if !foundUser {
		t.Fatalf("history missing user message: %#v", history)
	}
	infos, err := rt.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) < 2 {
		t.Fatalf("infos = %#v", infos)
	}
	foundPreview := false
	for _, in := range infos {
		if in.ID == oldID && in.Preview == "make a plan" {
			foundPreview = true
		}
	}
	if !foundPreview {
		t.Fatalf("preview missing: %#v", infos)
	}
}
