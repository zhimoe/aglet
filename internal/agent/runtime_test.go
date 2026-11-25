package agent

import (
	"context"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"

	"github.com/zhimoe/aglet/internal/config"
	"github.com/zhimoe/aglet/internal/permissions"
)

type fakeLLM struct {
	mu    sync.Mutex
	calls int
}

func (f *fakeLLM) Name() string { return "fake" }

func (f *fakeLLM) GenerateContent(_ context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		f.mu.Lock()
		f.calls++
		n := f.calls
		f.mu.Unlock()
		if n == 1 {
			yield(&model.LLMResponse{
				Content: &genai.Content{
					Role: "model",
					Parts: []*genai.Part{{
						FunctionCall: &genai.FunctionCall{
							ID: "call_1", Name: "read_file",
							Args: map[string]any{"path": "hello.txt"},
						},
					}},
				},
				TurnComplete: true,
			}, nil)
			return
		}
		yield(&model.LLMResponse{
			Content: &genai.Content{
				Role:  "model",
				Parts: []*genai.Part{{Text: "file says hello"}},
			},
			TurnComplete: true,
		}, nil)
	}
}

func TestRuntimeToolLoop(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello from file"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Workspace = dir
	cfg.SessionDB = filepath.Join(t.TempDir(), "sessions.db")
	cfg.MCPServers = map[string]config.MCPServerConfig{}
	cfg.Permissions = config.PermissionsConfig{AutoApprove: []string{"*"}}

	policy := permissions.NewPolicy(cfg.Permissions)
	llm := &fakeLLM{}
	rt, err := New(context.Background(), cfg, Options{
		Profile:   "deepseek",
		Workspace: dir,
		Policy:    policy,
		ModelLLM:  llm,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer rt.Close()

	var events []Event
	if err := rt.Run(context.Background(), "read hello.txt", func(e Event) {
		events = append(events, e)
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var sawCall, sawResult, sawText bool
	for _, e := range events {
		switch e.Type {
		case EventToolCall:
			if e.Call != nil && e.Call.Name == "read_file" {
				sawCall = true
			}
		case EventToolResult:
			if e.Result != nil && strings.Contains(e.Result.Text, "hello from file") {
				sawResult = true
			}
		case EventTextDelta:
			if strings.Contains(e.Text, "file says hello") {
				sawText = true
			}
		}
	}
	if !sawCall || !sawResult || !sawText {
		t.Fatalf("missing events: call=%v result=%v text=%v; events=%#v", sawCall, sawResult, sawText, events)
	}
	if llm.calls < 2 {
		t.Fatalf("model calls = %d, want >= 2", llm.calls)
	}
}
