package agent

import (
	"context"
	"iter"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"

	"github.com/zhimoe/aglet/internal/config"
	"github.com/zhimoe/aglet/internal/permissions"
	"github.com/zhimoe/aglet/internal/tools"
)

type scriptedLLM struct {
	mu        sync.Mutex
	calls     int
	responses []*model.LLMResponse
	requests  []*model.LLMRequest
}

func (s *scriptedLLM) Name() string { return "scripted" }

func (s *scriptedLLM) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		s.mu.Lock()
		s.requests = append(s.requests, req)
		i := s.calls
		s.calls++
		var resp *model.LLMResponse
		if i < len(s.responses) {
			resp = s.responses[i]
		} else {
			resp = textResponse("done")
		}
		s.mu.Unlock()
		yield(resp, nil)
	}
}

func textResponse(text string) *model.LLMResponse {
	return &model.LLMResponse{
		Content:      &genai.Content{Role: "model", Parts: []*genai.Part{{Text: text}}},
		TurnComplete: true,
	}
}

func callResponse(name string, args map[string]any) *model.LLMResponse {
	return &model.LLMResponse{
		Content: &genai.Content{Role: "model", Parts: []*genai.Part{{
			FunctionCall: &genai.FunctionCall{ID: "call_" + name, Name: name, Args: args},
		}}},
		TurnComplete: true,
	}
}

func phase3Config(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.Workspace = dir
	cfg.SessionDB = filepath.Join(t.TempDir(), "sessions.db")
	cfg.MCPServers = map[string]config.MCPServerConfig{}
	cfg.Context.AgentsDirs = []string{}
	cfg.Context.CommandsDirs = []string{}
	cfg.Context.SkillsDirs = []string{}
	cfg.Permissions = config.PermissionsConfig{AutoApprove: []string{"*"}}
	return cfg
}

func TestSubagentTask(t *testing.T) {
	cfg := phase3Config(t)
	llm := &scriptedLLM{responses: []*model.LLMResponse{
		callResponse("task", map[string]any{
			"subagent_type": "explore",
			"description":   "find files",
			"prompt":        "List the repository files.",
		}),
		{
			Content:      &genai.Content{Role: "model", Parts: []*genai.Part{{Text: "child findings"}}},
			TurnComplete: true,
			UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
				PromptTokenCount: 3, CandidatesTokenCount: 4, TotalTokenCount: 7,
			},
		},
		textResponse("final answer"),
	}}
	rt, err := New(t.Context(), cfg, Options{Profile: "deepseek", Workspace: cfg.Workspace, ModelLLM: llm})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	var events []Event
	if err := rt.Run(t.Context(), "delegate", func(ev Event) { events = append(events, ev) }); err != nil {
		t.Fatal(err)
	}
	var sawTask, sawChildResult, sawFinal, sawChildUsage, sawStarted bool
	for _, ev := range events {
		switch ev.Type {
		case EventToolCall:
			if ev.Call != nil && ev.Call.Name == "task" {
				sawTask = true
			}
		case EventToolResult:
			if ev.Result != nil && strings.Contains(ev.Result.Text, "child findings") {
				sawChildResult = true
			}
		case EventTextDelta:
			if strings.Contains(ev.Text, "final answer") {
				sawFinal = true
			}
		case EventUsage:
			if ev.Author == "explore" && ev.Usage != nil && ev.Usage.Total == 7 {
				sawChildUsage = true
			}
		case EventNotice:
			if strings.Contains(ev.Text, "subagent explore started") {
				sawStarted = true
			}
		}
	}
	if !sawTask || !sawChildResult || !sawFinal || !sawChildUsage || !sawStarted {
		t.Fatalf("events missing: task=%v result=%v final=%v usage=%v started=%v\n%#v",
			sawTask, sawChildResult, sawFinal, sawChildUsage, sawStarted, events)
	}
}

func TestFilterSubagentTools(t *testing.T) {
	cfg := phase3Config(t)
	all, err := tools.NewLocalTools(tools.Deps{Workspace: cfg.Workspace, Web: cfg.Web})
	if err != nil {
		t.Fatal(err)
	}
	filtered, missing := filterSubagentTools(all, []string{"read_file", "grep", "mcp_*"})
	if len(missing) != 1 || missing[0] != "mcp_*" {
		t.Fatalf("missing = %v", missing)
	}
	names := map[string]bool{}
	for _, item := range filtered {
		names[item.Name()] = true
	}
	if !names["read_file"] || !names["grep"] || names["write_file"] || names["run_command"] {
		t.Fatalf("names = %v", names)
	}
	allTools, _ := filterSubagentTools(all, nil)
	for _, item := range allTools {
		if item.Name() == "task" {
			t.Fatal("task must never be available to a subagent")
		}
	}
}

func TestHookBlocksTool(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	cfg := phase3Config(t)
	if err := writeTestFile(cfg.Workspace, "hello.txt", "hello from file"); err != nil {
		t.Fatal(err)
	}
	cfg.Hooks = config.HooksConfig{
		"PreToolUse": {{Matcher: "read_file", Hooks: []config.HookCommandConfig{{
			Command: "echo nope >&2; cat >/dev/null; exit 2",
		}}}},
	}
	llm := &scriptedLLM{responses: []*model.LLMResponse{
		callResponse("read_file", map[string]any{"path": "hello.txt"}),
		textResponse("done"),
	}}
	rt, err := New(t.Context(), cfg, Options{Profile: "deepseek", Workspace: cfg.Workspace, ModelLLM: llm})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	var events []Event
	if err := rt.Run(t.Context(), "read", func(ev Event) { events = append(events, ev) }); err != nil {
		t.Fatal(err)
	}
	var result *ToolResult
	for _, ev := range events {
		if ev.Type == EventToolResult && ev.Result != nil && ev.Result.Name == "read_file" {
			result = ev.Result
		}
	}
	if result == nil || !result.IsError || !strings.Contains(result.Text, "nope") {
		t.Fatalf("result = %#v", result)
	}
	if strings.Contains(result.Text, "hello from file") {
		t.Fatalf("tool executed despite hook: %#v", result)
	}
}

func TestHookNoticeAndPromptContext(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	cfg := phase3Config(t)
	cfg.Hooks = config.HooksConfig{
		"PreToolUse": {{Matcher: "read_file", Hooks: []config.HookCommandConfig{{
			Command: `cat >/dev/null; printf '{"systemMessage":"hello hook"}'`,
		}}}},
		"UserPromptSubmit": {{Hooks: []config.HookCommandConfig{{
			Command: `cat >/dev/null; printf '{"hookSpecificOutput":{"additionalContext":"extra-context"}}'`,
		}}}},
	}
	if err := writeTestFile(cfg.Workspace, "hello.txt", "file body"); err != nil {
		t.Fatal(err)
	}
	llm := &scriptedLLM{responses: []*model.LLMResponse{
		callResponse("read_file", map[string]any{"path": "hello.txt"}),
		textResponse("done"),
	}}
	rt, err := New(t.Context(), cfg, Options{Profile: "deepseek", Workspace: cfg.Workspace, ModelLLM: llm})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	var events []Event
	if err := rt.Run(t.Context(), "read", func(ev Event) { events = append(events, ev) }); err != nil {
		t.Fatal(err)
	}
	sawNotice := false
	for _, ev := range events {
		if ev.Type == EventNotice && strings.Contains(ev.Text, "hello hook") {
			sawNotice = true
		}
	}
	if !sawNotice {
		t.Fatalf("missing hook notice: %#v", events)
	}
	llm.mu.Lock()
	requests := append([]*model.LLMRequest(nil), llm.requests...)
	llm.mu.Unlock()
	if len(requests) == 0 || !requestContains(requests[0], "extra-context") {
		t.Fatalf("prompt context missing from model request")
	}
}

func TestSessionStartContext(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	cfg := phase3Config(t)
	cfg.Hooks = config.HooksConfig{
		"SessionStart": {{Matcher: "startup", Hooks: []config.HookCommandConfig{{
			Command: `cat >/dev/null; printf '{"hookSpecificOutput":{"additionalContext":"startup-context"}}'`,
		}}}},
	}
	llm := &scriptedLLM{responses: []*model.LLMResponse{textResponse("ok")}}
	rt, err := New(t.Context(), cfg, Options{Profile: "deepseek", Workspace: cfg.Workspace, ModelLLM: llm})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if err := rt.Run(t.Context(), "hi", func(Event) {}); err != nil {
		t.Fatal(err)
	}
	llm.mu.Lock()
	requests := append([]*model.LLMRequest(nil), llm.requests...)
	llm.mu.Unlock()
	if len(requests) == 0 {
		t.Fatal("no model request")
	}
	req := requests[0]
	if req.Config == nil || req.Config.SystemInstruction == nil {
		t.Fatalf("no system instruction: %#v", req)
	}
	var text strings.Builder
	for _, p := range req.Config.SystemInstruction.Parts {
		text.WriteString(p.Text)
	}
	if !strings.Contains(text.String(), "startup-context") {
		t.Fatalf("system prompt missing session context:\n%s", text.String())
	}
}

func TestSessionEndHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	cfg := phase3Config(t)
	marker := filepath.Join(t.TempDir(), "ended")
	cfg.Hooks = config.HooksConfig{
		"SessionEnd": {{Hooks: []config.HookCommandConfig{{
			Command: "cat >/dev/null; touch " + marker,
		}}}},
	}
	llm := &scriptedLLM{}
	rt, err := New(t.Context(), cfg, Options{Profile: "deepseek", Workspace: cfg.Workspace, ModelLLM: llm})
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("SessionEnd hook did not run: %v", err)
	}
}

func TestRunShellWithHooks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	cfg := phase3Config(t)
	cfg.Hooks = config.HooksConfig{
		"PreToolUse": {{Matcher: "run_command", Hooks: []config.HookCommandConfig{{
			Command: `cat >/dev/null; printf '{"systemMessage":"shell-hook"}'`,
		}}}},
	}
	llm := &scriptedLLM{}
	rt, err := New(t.Context(), cfg, Options{Profile: "deepseek", Workspace: cfg.Workspace, Policy: permissions.NewPolicy(cfg.Permissions), ModelLLM: llm})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	res, err := rt.RunShellWithHooks(t.Context(), "printf command-output")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Stdout, "command-output") || !strings.Contains(res.Stdout, "shell-hook") {
		t.Fatalf("stdout = %q", res.Stdout)
	}
}

func requestContains(req *model.LLMRequest, needle string) bool {
	for _, content := range req.Contents {
		for _, part := range content.Parts {
			if strings.Contains(part.Text, needle) {
				return true
			}
		}
	}
	return false
}

func writeTestFile(dir, name, content string) error {
	return os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
}
