package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/zhimoe/aglet/internal/config"
)

type fakeExecutor struct {
	mu     sync.Mutex
	calls  []Request
	stdout string
	stderr string
	code   int
	err    error
}

func (f *fakeExecutor) Run(_ context.Context, req Request) ([]byte, []byte, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, req)
	return []byte(f.stdout), []byte(f.stderr), f.code, f.err
}

func (f *fakeExecutor) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func newRunner(t *testing.T, cfg config.HooksConfig, exec *fakeExecutor) *Runner {
	t.Helper()
	return New(cfg, Options{
		Workspace: t.TempDir(),
		SessionID: func() string { return "s1" },
		Executor:  exec,
	})
}

func TestMatcher(t *testing.T) {
	exec := &fakeExecutor{}
	r := newRunner(t, config.HooksConfig{
		"PreToolUse": {{Matcher: "write_file", Hooks: []config.HookCommandConfig{{Command: "hook"}}}},
	}, exec)
	r.Run(t.Context(), config.HookPreToolUse, Input{ToolName: "read_file"})
	if exec.count() != 0 {
		t.Fatal("hook should not run for read_file")
	}
	res := r.Run(t.Context(), config.HookPreToolUse, Input{ToolName: "write_file"})
	if exec.count() != 1 || res.Blocked {
		t.Fatalf("calls=%d res=%#v", exec.count(), res)
	}
}

func TestExitTwoBlocks(t *testing.T) {
	exec := &fakeExecutor{code: 2, stderr: "not allowed"}
	r := newRunner(t, config.HooksConfig{
		"PreToolUse": {{Hooks: []config.HookCommandConfig{{Command: "hook"}}}},
	}, exec)
	res := r.Run(t.Context(), config.HookPreToolUse, Input{ToolName: "run_command"})
	if !res.Blocked || res.Reason != "not allowed" {
		t.Fatalf("res = %#v", res)
	}
}

func TestJSONOutput(t *testing.T) {
	exec := &fakeExecutor{stdout: `{
	  "systemMessage": "hello",
	  "hookSpecificOutput": {"permissionDecision": "deny", "permissionDecisionReason": "no", "additionalContext": "ctx"}
	}`}
	r := newRunner(t, config.HooksConfig{
		"PreToolUse": {{Hooks: []config.HookCommandConfig{{Command: "hook"}}}},
	}, exec)
	res := r.Run(t.Context(), config.HookPreToolUse, Input{ToolName: "write_file"})
	if !res.Blocked || res.Reason != "no" {
		t.Fatalf("res = %#v", res)
	}
	if len(res.SystemMessages) != 1 || res.SystemMessages[0] != "hello" {
		t.Fatalf("messages = %v", res.SystemMessages)
	}
}

func TestAdditionalContext(t *testing.T) {
	exec := &fakeExecutor{stdout: `{"hookSpecificOutput":{"additionalContext":"remember this"}}`}
	r := newRunner(t, config.HooksConfig{
		"UserPromptSubmit": {{Hooks: []config.HookCommandConfig{{Command: "hook"}}}},
	}, exec)
	res := r.Run(t.Context(), config.HookUserPromptSubmit, Input{Prompt: "hi"})
	if res.Blocked || res.AdditionalContext != "remember this" {
		t.Fatalf("res = %#v", res)
	}
}

func TestTimeoutIsWarning(t *testing.T) {
	exec := &fakeExecutor{err: context.DeadlineExceeded}
	r := newRunner(t, config.HooksConfig{
		"PreToolUse": {{Hooks: []config.HookCommandConfig{{Command: "hook"}}}},
	}, exec)
	res := r.Run(t.Context(), config.HookPreToolUse, Input{ToolName: "write_file"})
	if res.Blocked || len(res.Warnings) != 1 {
		t.Fatalf("res = %#v", res)
	}
}

func TestPayloadAndEnv(t *testing.T) {
	exec := &fakeExecutor{}
	r := newRunner(t, config.HooksConfig{
		"PreToolUse": {{Hooks: []config.HookCommandConfig{{Command: "hook"}}}},
	}, exec)
	r.Run(t.Context(), config.HookPreToolUse, Input{
		ToolName: "write_file", ToolUseID: "c1", ToolInput: map[string]any{"path": "x"},
	})
	if exec.count() != 1 {
		t.Fatal("hook did not run")
	}
	req := exec.calls[0]
	var in Input
	if err := json.Unmarshal(req.Stdin, &in); err != nil {
		t.Fatal(err)
	}
	if in.HookEventName != "PreToolUse" || in.SessionID != "s1" || in.ToolName != "write_file" || in.ToolUseID != "c1" {
		t.Fatalf("payload = %#v", in)
	}
	joined := strings.Join(req.Env, ",")
	if !strings.Contains(joined, "AGLET_HOOK_EVENT=PreToolUse") || !strings.Contains(joined, "AGLET_SESSION_ID=s1") {
		t.Fatalf("env = %v", req.Env)
	}
}

func TestDefaultExecutor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	r := New(config.HooksConfig{
		"PreToolUse": {{Hooks: []config.HookCommandConfig{{
			Command: `printf '{"systemMessage":"from shell"}'`,
		}}}},
	}, Options{Workspace: t.TempDir()})
	res := r.Run(t.Context(), config.HookPreToolUse, Input{ToolName: "read_file"})
	if res.Blocked || len(res.SystemMessages) != 1 || res.SystemMessages[0] != "from shell" {
		t.Fatalf("res = %#v", res)
	}
}

func TestExitOtherIsWarning(t *testing.T) {
	exec := &fakeExecutor{code: 1, err: errors.New("boom")}
	r := newRunner(t, config.HooksConfig{
		"PreToolUse": {{Hooks: []config.HookCommandConfig{{Command: "hook"}}}},
	}, exec)
	res := r.Run(t.Context(), config.HookPreToolUse, Input{ToolName: "write_file"})
	if res.Blocked || len(res.Warnings) != 1 {
		t.Fatalf("res = %#v", res)
	}
}
