package config

import (
	"os"
	"path/filepath"
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

func TestExpandPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	if got := ExpandPath("~/x"); got != filepath.Join(home, "x") {
		t.Fatalf("got %q", got)
	}
}
