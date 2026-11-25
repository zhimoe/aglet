package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const AppName = "aglet"

// Config is the on-disk configuration format.
type Config struct {
	DefaultProfile string                     `json:"default_profile"`
	Profiles       map[string]Profile         `json:"profiles"`
	Agent          AgentConfig                `json:"agent"`
	Workspace      string                     `json:"workspace"`
	Permissions    PermissionsConfig          `json:"permissions"`
	MCPServers     map[string]MCPServerConfig `json:"mcp_servers"`
	SessionDB      string                     `json:"session_db"`
	UI             UIConfig                   `json:"ui"`
	StrictMCP      bool                       `json:"strict_mcp"`
}

type Profile struct {
	// API is either "chat_completions" (default) or "responses".
	API       string `json:"api"`
	BaseURL   string `json:"base_url"`
	APIKeyEnv string `json:"api_key_env"`
	Model     string `json:"model"`
}

type AgentConfig struct {
	MaxLLMCalls      int     `json:"max_llm_calls"`
	Temperature      float32 `json:"temperature"`
	SystemPromptFile string  `json:"system_prompt_file"`
}

type PermissionsConfig struct {
	AutoApprove     []string `json:"auto_approve"`
	RequireApproval []string `json:"require_approval"`
	Deny            []string `json:"deny"`
}

type MCPServerConfig struct {
	Transport           string            `json:"transport"`
	Command             string            `json:"command"`
	Args                []string          `json:"args"`
	Env                 map[string]string `json:"env"`
	URL                 string            `json:"url"`
	Headers             map[string]string `json:"headers"`
	Enabled             *bool             `json:"enabled"`
	RequireConfirmation *bool             `json:"require_confirmation"`
	ToolPrefix          string            `json:"tool_prefix"`
	ConnectTimeoutSec   int               `json:"connect_timeout_sec"`
}

func (m MCPServerConfig) IsEnabled() bool {
	return m.Enabled == nil || *m.Enabled
}

func (m MCPServerConfig) NeedsConfirmation() bool {
	return m.RequireConfirmation == nil || *m.RequireConfirmation
}

type UIConfig struct {
	ShowThoughts *bool `json:"show_thoughts"`
}

func (u UIConfig) ShowThoughtsEnabled() bool {
	return u.ShowThoughts == nil || *u.ShowThoughts
}

// Defaults returns the built-in defaults, used both for `config init` and for
// filling fields missing from a user config.
func Defaults() *Config {
	showThoughts := true
	return &Config{
		DefaultProfile: "deepseek",
		Profiles: map[string]Profile{
			"openai": {
				API:       "chat_completions",
				BaseURL:   "https://api.openai.com/v1",
				APIKeyEnv: "OPENAI_API_KEY",
				Model:     "gpt-4.1",
			},
			"deepseek": {
				API:       "chat_completions",
				BaseURL:   "https://api.deepseek.com",
				APIKeyEnv: "DEEPSEEK_API_KEY",
				Model:     "deepseek-flash",
			},
		},
		Agent: AgentConfig{
			MaxLLMCalls: 50,
			Temperature: 0.2,
		},
		Workspace: ".",
		Permissions: PermissionsConfig{
			AutoApprove:     []string{"read_file", "list_dir", "glob", "grep"},
			RequireApproval: []string{"write_file", "edit_file", "run_command", "mcp_*"},
		},
		SessionDB:  "~/.local/share/aglet/sessions.db",
		UI:         UIConfig{ShowThoughts: &showThoughts},
		MCPServers: map[string]MCPServerConfig{},
	}
}

// Load reads and validates a config file. It returns the resolved path too.
func Load(explicit string) (*Config, string, error) {
	path, exists := ResolvePath(explicit)
	if !exists {
		return nil, path, fmt.Errorf("config not found at %s; run `aglet config init` or pass --config", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, path, fmt.Errorf("read config %s: %w", path, err)
	}
	var cfg Config
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return nil, path, fmt.Errorf("parse config %s: %w", path, err)
	}
	ApplyDefaults(&cfg)
	if err := cfg.Validate(); err != nil {
		return nil, path, err
	}
	return &cfg, path, nil
}

// ApplyDefaults fills zero/empty fields and expands MCP environment references.
func ApplyDefaults(cfg *Config) {
	d := Defaults()
	if cfg.DefaultProfile == "" {
		cfg.DefaultProfile = d.DefaultProfile
	}
	if len(cfg.Profiles) == 0 {
		cfg.Profiles = d.Profiles
	}
	for name, p := range cfg.Profiles {
		if p.API == "" {
			p.API = "chat_completions"
		}
		cfg.Profiles[name] = p
	}
	if cfg.Agent.MaxLLMCalls == 0 {
		cfg.Agent.MaxLLMCalls = d.Agent.MaxLLMCalls
	}
	if cfg.Agent.Temperature == 0 {
		cfg.Agent.Temperature = d.Agent.Temperature
	}
	if cfg.Workspace == "" {
		cfg.Workspace = d.Workspace
	}
	if cfg.Permissions.AutoApprove == nil {
		cfg.Permissions.AutoApprove = d.Permissions.AutoApprove
	}
	if cfg.Permissions.RequireApproval == nil {
		cfg.Permissions.RequireApproval = d.Permissions.RequireApproval
	}
	if cfg.Permissions.Deny == nil {
		cfg.Permissions.Deny = []string{}
	}
	if cfg.MCPServers == nil {
		cfg.MCPServers = map[string]MCPServerConfig{}
	}
	for name, s := range cfg.MCPServers {
		if s.Transport == "" {
			switch {
			case s.Command != "":
				s.Transport = "stdio"
			case s.URL != "":
				s.Transport = "http"
			}
		}
		if s.ConnectTimeoutSec <= 0 {
			s.ConnectTimeoutSec = 10
		}
		if s.Env == nil {
			s.Env = map[string]string{}
		}
		if s.Headers == nil {
			s.Headers = map[string]string{}
		}
		for k, v := range s.Env {
			s.Env[k] = os.ExpandEnv(v)
		}
		for k, v := range s.Headers {
			s.Headers[k] = os.ExpandEnv(v)
		}
		cfg.MCPServers[name] = s
	}
	if cfg.SessionDB == "" {
		cfg.SessionDB = d.SessionDB
	}
	if cfg.UI.ShowThoughts == nil {
		cfg.UI.ShowThoughts = d.UI.ShowThoughts
	}
}

func (c *Config) Validate() error {
	if len(c.Profiles) == 0 {
		return fmt.Errorf("config: profiles must not be empty")
	}
	if _, ok := c.Profiles[c.DefaultProfile]; !ok {
		return fmt.Errorf("config: default_profile %q not found in profiles", c.DefaultProfile)
	}
	for name, p := range c.Profiles {
		if p.API != "chat_completions" && p.API != "responses" {
			return fmt.Errorf("config: profile %q has invalid api %q (want chat_completions or responses)", name, p.API)
		}
		if p.BaseURL == "" {
			return fmt.Errorf("config: profile %q base_url is required", name)
		}
		if p.APIKeyEnv == "" {
			return fmt.Errorf("config: profile %q api_key_env is required", name)
		}
		if p.Model == "" {
			return fmt.Errorf("config: profile %q model is required", name)
		}
	}
	for name, s := range c.MCPServers {
		if !s.IsEnabled() {
			continue
		}
		switch s.Transport {
		case "stdio":
			if s.Command == "" {
				return fmt.Errorf("config: mcp_servers.%s.command is required for stdio", name)
			}
		case "sse", "http":
			if s.URL == "" {
				return fmt.Errorf("config: mcp_servers.%s.url is required for %s", name, s.Transport)
			}
		default:
			return fmt.Errorf("config: mcp_servers.%s.transport %q is invalid (want stdio, sse, http)", name, s.Transport)
		}
	}
	return nil
}

// ResolveProfile returns the selected profile, applying an optional model override.
func (c *Config) ResolveProfile(name, modelOverride string) (string, Profile, error) {
	if name == "" {
		name = c.DefaultProfile
	}
	p, ok := c.Profiles[name]
	if !ok {
		return "", Profile{}, fmt.Errorf("profile %q not found", name)
	}
	if modelOverride != "" {
		p.Model = modelOverride
	}
	return name, p, nil
}

// RedactedJSON returns a copy of the config with secret-bearing values masked.
func (c *Config) RedactedJSON() ([]byte, error) {
	cp := *c
	cp.MCPServers = make(map[string]MCPServerConfig, len(c.MCPServers))
	for name, s := range c.MCPServers {
		sc := s
		sc.Env = make(map[string]string, len(s.Env))
		for k := range s.Env {
			sc.Env[k] = "***"
		}
		sc.Headers = make(map[string]string, len(s.Headers))
		for k := range s.Headers {
			sc.Headers[k] = "***"
		}
		cp.MCPServers[name] = sc
	}
	return json.MarshalIndent(cp, "", "  ")
}

// ResolvePath returns the config path to use and whether it exists.
func ResolvePath(explicit string) (string, bool) {
	if explicit != "" {
		p := ExpandPath(explicit)
		return p, fileExists(p)
	}
	if p := os.Getenv("AGLET_CONFIG"); p != "" {
		p = ExpandPath(p)
		return p, fileExists(p)
	}
	if p := "aglet.json"; fileExists(p) {
		abs, _ := filepath.Abs(p)
		return abs, true
	}
	p := DefaultPath()
	return p, fileExists(p)
}

func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "aglet.json"
	}
	return filepath.Join(home, ".config", "aglet", "config.json")
}

func ExpandPath(p string) string {
	if p == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return p
	}
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// ExampleJSON is written by `aglet config init`.
const ExampleJSON = `{
  "default_profile": "deepseek",
  "profiles": {
    "openai": {
      "api": "chat_completions",
      "base_url": "https://api.openai.com/v1",
      "api_key_env": "OPENAI_API_KEY",
      "model": "gpt-4.1"
    },
    "deepseek": {
      "api": "chat_completions",
      "base_url": "https://api.deepseek.com",
      "api_key_env": "DEEPSEEK_API_KEY",
      "model": "deepseek-flash"
    }
  },
  "agent": {
    "max_llm_calls": 50,
    "temperature": 0.2,
    "system_prompt_file": ""
  },
  "workspace": ".",
  "permissions": {
    "auto_approve": ["read_file", "list_dir", "glob", "grep"],
    "require_approval": ["write_file", "edit_file", "run_command", "mcp_*"],
    "deny": []
  },
  "mcp_servers": {
    "everything": {
      "transport": "stdio",
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-everything"],
      "env": {},
      "enabled": false,
      "require_confirmation": true,
      "connect_timeout_sec": 10
    }
  },
  "session_db": "~/.local/share/aglet/sessions.db",
  "ui": {
    "show_thoughts": true
  }
}
`
