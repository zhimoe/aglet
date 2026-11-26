package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestLoadDefaultsAndRedact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	raw := `{
	  "profiles": {
	    "deepseek": {
	      "base_url": "https://api.deepseek.com",
	      "api_key_env": "DEEPSEEK_API_KEY",
	      "model": "deepseek-flash"
	    }
	  },
	  "mcp_servers": {
	    "x": {
	      "transport": "stdio",
	      "command": "echo",
	      "env": {"TOKEN": "${TOKEN}"},
	      "headers": {"Authorization": "Bearer ${TOKEN}"}
	    }
	  }
	}`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TOKEN", "secret-value")

	cfg, gotPath, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if gotPath != path {
		t.Fatalf("path = %q, want %q", gotPath, path)
	}
	if cfg.DefaultProfile != "deepseek" {
		t.Fatalf("default profile = %q", cfg.DefaultProfile)
	}
	if got := cfg.Profiles["deepseek"].API; got != "chat_completions" {
		t.Fatalf("api default = %q", got)
	}
	if cfg.Agent.MaxLLMCalls != 50 {
		t.Fatalf("max calls = %d", cfg.Agent.MaxLLMCalls)
	}
	if !cfg.UI.ShowThoughtsEnabled() {
		t.Fatal("show thoughts should default true")
	}
	if got := cfg.MCPServers["x"].Env["TOKEN"]; got != "secret-value" {
		t.Fatalf("env expansion = %q", got)
	}
	b, err := cfg.RedactedJSON()
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if strings.Contains(s, "secret-value") {
		t.Fatalf("redacted config leaked secret: %s", s)
	}
	if !strings.Contains(s, `"TOKEN": "***"`) {
		t.Fatalf("env key not redacted: %s", s)
	}
}

func TestLoadRejectsInvalidAPI(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	raw := `{
	  "default_profile": "x",
	  "profiles": {
	    "x": {
	      "api": "nope",
	      "base_url": "https://example.com",
	      "api_key_env": "X_KEY",
	      "model": "m"
	    }
	  }
	}`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(path); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestResolveProfileOverride(t *testing.T) {
	cfg := Defaults()
	name, p, err := cfg.ResolveProfile("openai", "custom-model")
	if err != nil {
		t.Fatal(err)
	}
	if name != "openai" || p.Model != "custom-model" {
		t.Fatalf("got %q %q", name, p.Model)
	}
	if _, _, err := cfg.ResolveProfile("missing", ""); err == nil {
		t.Fatal("expected missing profile error")
	}
}

func TestPhase2Defaults(t *testing.T) {
	cfg := Defaults()
	if cfg.ApprovalMode != ApprovalAsk {
		t.Fatalf("approval mode = %q", cfg.ApprovalMode)
	}
	if len(cfg.Context.InstructionFiles) == 0 || !cfg.Context.SkillsEnabledValue() {
		t.Fatalf("context defaults = %#v", cfg.Context)
	}
	if cfg.Agent.Compaction.TokenThreshold != 0 {
		t.Fatal("compaction should default off")
	}
	for _, needle := range []string{`"approval_mode"`, `"context"`, `"compaction"`} {
		if !strings.Contains(ExampleJSON, needle) {
			t.Fatalf("ExampleJSON missing %s", needle)
		}
	}
}

func TestApprovalModeNormalizeAndValidate(t *testing.T) {
	cases := map[string]string{
		"":             ApprovalAsk,
		"default":      ApprovalAsk,
		"plan":         ApprovalReadOnly,
		"accept-edits": ApprovalAutoEdit,
		"auto":         ApprovalFullAuto,
	}
	for in, want := range cases {
		if got := NormalizeApprovalMode(in); got != want {
			t.Fatalf("NormalizeApprovalMode(%q) = %q, want %q", in, got, want)
		}
	}
	cfg := Defaults()
	cfg.ApprovalMode = "bogus"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid approval mode")
	}
}

func TestCompactionValidation(t *testing.T) {
	cfg := Defaults()
	cfg.Agent.Compaction = CompactionConfig{TokenThreshold: 1000}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected retention error")
	}
	cfg.Agent.Compaction = CompactionConfig{TokenThreshold: 1000, EventRetentionSize: 20}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	invalid := []CompactionConfig{
		{EventRetentionSize: 20},
		{OverlapSize: 5},
		{CompactionInterval: 20, OverlapSize: 5, EventRetentionSize: 10},
		{TokenThreshold: 1000, EventRetentionSize: 20, CompactionInterval: 10},
	}
	for i, comp := range invalid {
		cfg.Agent.Compaction = comp
		if err := cfg.Validate(); err == nil {
			t.Fatalf("case %d: expected validation error for %#v", i, comp)
		}
	}
	cfg.Agent.Compaction = CompactionConfig{CompactionInterval: 20, OverlapSize: 5}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("sliding-window-only compaction should be valid: %v", err)
	}
}

func TestPhase3Defaults(t *testing.T) {
	cfg := Defaults()
	if len(cfg.Context.AgentsDirs) == 0 || len(cfg.Context.CommandsDirs) == 0 {
		t.Fatalf("context dirs = %#v", cfg.Context)
	}
	if !cfg.Web.FetchEnabledValue() || cfg.Web.SearchEnabledValue() {
		t.Fatalf("web defaults = %#v", cfg.Web)
	}
	if cfg.Hooks == nil {
		t.Fatal("hooks map should be initialized")
	}
	for _, tool := range []string{"task", "web_fetch", "web_search", "bash_output", "kill_shell"} {
		if !slices.Contains(cfg.Permissions.AutoApprove, tool) {
			t.Fatalf("auto_approve missing %s: %v", tool, cfg.Permissions.AutoApprove)
		}
	}
	for _, needle := range []string{`"agents_dirs"`, `"commands_dirs"`, `"hooks"`, `"web"`} {
		if !strings.Contains(ExampleJSON, needle) {
			t.Fatalf("ExampleJSON missing %s", needle)
		}
	}
}

func TestHooksValidation(t *testing.T) {
	valid := HookCommandConfig{Command: "hook.sh"}
	cases := []struct {
		name  string
		hooks HooksConfig
		ok    bool
	}{
		{"valid session matcher", HooksConfig{"SessionStart": {{Matcher: "startup", Hooks: []HookCommandConfig{valid}}}}, true},
		{"unknown event", HooksConfig{"Unknown": {{Hooks: []HookCommandConfig{valid}}}}, false},
		{"bad regex", HooksConfig{"PreToolUse": {{Matcher: "[", Hooks: []HookCommandConfig{valid}}}}, false},
		{"matcher not allowed", HooksConfig{"UserPromptSubmit": {{Matcher: "x", Hooks: []HookCommandConfig{valid}}}}, false},
		{"empty hooks", HooksConfig{"PreToolUse": {{}}}, false},
		{"empty command", HooksConfig{"PreToolUse": {{Hooks: []HookCommandConfig{{}}}}}, false},
		{"bad type", HooksConfig{"PreToolUse": {{Hooks: []HookCommandConfig{{Type: "http", Command: "x"}}}}}, false},
		{"bad timeout", HooksConfig{"PreToolUse": {{Hooks: []HookCommandConfig{{Command: "x", Timeout: 1000}}}}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Defaults()
			cfg.Hooks = tc.hooks
			err := cfg.Validate()
			if tc.ok && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestWebValidation(t *testing.T) {
	enabled := true
	disabled := false
	base := Defaults()
	base.Web.Search.Enabled = &enabled
	base.Web.Search.Provider = "searxng"
	base.Web.Search.BaseURL = ""
	if err := base.Validate(); err == nil {
		t.Fatal("expected searxng base_url error")
	}
	base.Web.Search.BaseURL = "http://localhost:8888"
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	base.Web.Search.Provider = "unknown"
	if err := base.Validate(); err == nil {
		t.Fatal("expected provider error")
	}
	tavily := Defaults()
	tavily.Web.Search.Enabled = &enabled
	tavily.Web.Search.Provider = "tavily"
	tavily.Web.Search.APIKeyEnv = ""
	if err := tavily.Validate(); err == nil {
		t.Fatal("expected api key env error")
	}
	size := Defaults()
	size.Web.Fetch.MaxBytes = 10 * 1024 * 1024
	if err := size.Validate(); err == nil {
		t.Fatal("expected max_bytes error")
	}
	off := Defaults()
	off.Web.Search.Enabled = &disabled
	off.Web.Search.Provider = ""
	if err := off.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestExampleJSONParses(t *testing.T) {
	check := func(name string, data []byte) {
		t.Helper()
		var cfg Config
		dec := json.NewDecoder(strings.NewReader(string(data)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&cfg); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		ApplyDefaults(&cfg)
		if err := cfg.Validate(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	check("ExampleJSON", []byte(ExampleJSON))
	root, err := os.ReadFile(filepath.Join("..", "..", "config.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	check("config.example.json", root)
}

func TestExpandPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	if got := ExpandPath("~/x"); got != filepath.Join(home, "x") {
		t.Fatalf("got %q", got)
	}
}
