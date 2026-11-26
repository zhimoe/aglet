package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const AppName = "aglet"

// Config is the on-disk configuration format.
type Config struct {
	DefaultProfile string                     `json:"default_profile"`
	ApprovalMode   string                     `json:"approval_mode"`
	Profiles       map[string]Profile         `json:"profiles"`
	Agent          AgentConfig                `json:"agent"`
	Context        ContextConfig              `json:"context"`
	Workspace      string                     `json:"workspace"`
	Permissions    PermissionsConfig          `json:"permissions"`
	MCPServers     map[string]MCPServerConfig `json:"mcp_servers"`
	SessionDB      string                     `json:"session_db"`
	UI             UIConfig                   `json:"ui"`
	StrictMCP      bool                       `json:"strict_mcp"`
	Hooks          HooksConfig                `json:"hooks"`
	Web            WebConfig                  `json:"web"`
}

type Profile struct {
	// API is either "chat_completions" (default) or "responses".
	API       string `json:"api"`
	BaseURL   string `json:"base_url"`
	APIKeyEnv string `json:"api_key_env"`
	Model     string `json:"model"`
}

type AgentConfig struct {
	MaxLLMCalls      int              `json:"max_llm_calls"`
	Temperature      float32          `json:"temperature"`
	SystemPromptFile string           `json:"system_prompt_file"`
	Compaction       CompactionConfig `json:"compaction"`
}

type CompactionConfig struct {
	TokenThreshold     int `json:"token_threshold"`
	EventRetentionSize int `json:"event_retention_size"`
	CompactionInterval int `json:"compaction_interval"`
	OverlapSize        int `json:"overlap_size"`
}

type ContextConfig struct {
	InstructionFiles    []string `json:"instruction_files"`
	SkillsDirs          []string `json:"skills_dirs"`
	AgentsDirs          []string `json:"agents_dirs"`
	CommandsDirs        []string `json:"commands_dirs"`
	SkillsEnabled       *bool    `json:"skills_enabled"`
	MaxInstructionBytes int      `json:"max_instruction_bytes"`
	MaxSkillBytes       int      `json:"max_skill_bytes"`
}

func (c ContextConfig) SkillsEnabledValue() bool {
	return c.SkillsEnabled == nil || *c.SkillsEnabled
}

// Hook event names. The JSON keys are accepted case-insensitively and with
// optional underscores or dashes.
const (
	HookUserPromptSubmit = "UserPromptSubmit"
	HookPreToolUse       = "PreToolUse"
	HookPostToolUse      = "PostToolUse"
	HookSessionStart     = "SessionStart"
	HookSessionEnd       = "SessionEnd"
	HookStop             = "Stop"
)

// HooksConfig maps an event name to its matcher groups.
type HooksConfig map[string][]HookMatcherConfig

type HookMatcherConfig struct {
	Matcher string              `json:"matcher"`
	Hooks   []HookCommandConfig `json:"hooks"`
}

type HookCommandConfig struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

// CanonicalHookEvent returns the canonical event name for a user-provided key.
func CanonicalHookEvent(name string) (string, bool) {
	key := strings.ToLower(strings.TrimSpace(name))
	key = strings.NewReplacer("_", "", "-", "", " ", "").Replace(key)
	switch key {
	case "userpromptsubmit":
		return HookUserPromptSubmit, true
	case "pretooluse":
		return HookPreToolUse, true
	case "posttooluse":
		return HookPostToolUse, true
	case "sessionstart":
		return HookSessionStart, true
	case "sessionend":
		return HookSessionEnd, true
	case "stop":
		return HookStop, true
	default:
		return "", false
	}
}

// HookMatcherAllowed reports whether an event may carry a non-empty matcher.
func HookMatcherAllowed(event string) bool {
	switch event {
	case HookPreToolUse, HookPostToolUse, HookSessionStart:
		return true
	default:
		return false
	}
}

type WebConfig struct {
	Fetch  WebFetchConfig  `json:"fetch"`
	Search WebSearchConfig `json:"search"`
}

type WebFetchConfig struct {
	Enabled              *bool    `json:"enabled"`
	MaxBytes             int      `json:"max_bytes"`
	TimeoutSec           int      `json:"timeout_sec"`
	AllowPrivateNetworks bool     `json:"allow_private_networks"`
	AllowedHosts         []string `json:"allowed_hosts"`
}

type WebSearchConfig struct {
	Enabled    *bool  `json:"enabled"`
	Provider   string `json:"provider"`
	BaseURL    string `json:"base_url"`
	APIKeyEnv  string `json:"api_key_env"`
	MaxResults int    `json:"max_results"`
	TimeoutSec int    `json:"timeout_sec"`
}

func (w WebConfig) FetchEnabledValue() bool {
	return w.Fetch.Enabled == nil || *w.Fetch.Enabled
}

func (w WebConfig) SearchEnabledValue() bool {
	return w.Search.Enabled != nil && *w.Search.Enabled
}

// ApprovalMode constants and aliases.
const (
	ApprovalAsk      = "ask"
	ApprovalReadOnly = "read-only"
	ApprovalAutoEdit = "auto-edit"
	ApprovalFullAuto = "full-auto"
)

func NormalizeApprovalMode(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "ask", "default":
		return ApprovalAsk
	case "read-only", "readonly", "plan":
		return ApprovalReadOnly
	case "auto-edit", "autoedit", "accept-edits", "acceptedits":
		return ApprovalAutoEdit
	case "full-auto", "fullauto", "auto", "yolo":
		return ApprovalFullAuto
	default:
		return s
	}
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
	fetchEnabled := true
	searchEnabled := false
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
		ApprovalMode: ApprovalAsk,
		Agent: AgentConfig{
			MaxLLMCalls: 50,
			Temperature: 0.2,
		},
		Context: ContextConfig{
			InstructionFiles:    []string{"AGLET.md", "AGENTS.md", "AGENTS.override.md", "CLAUDE.md"},
			SkillsDirs:          []string{".agents/skills", ".aglet/skills", "~/.agents/skills", "~/.config/aglet/skills"},
			AgentsDirs:          []string{".aglet/agents", ".agents/agents", "~/.config/aglet/agents"},
			CommandsDirs:        []string{".aglet/commands", ".agents/commands", "~/.config/aglet/commands"},
			MaxInstructionBytes: 32 * 1024,
			MaxSkillBytes:       64 * 1024,
		},
		Workspace: ".",
		Permissions: PermissionsConfig{
			AutoApprove: []string{
				"read_file", "list_dir", "glob", "grep", "git_status", "git_diff", "git_log",
				"load_skill", "update_plan", "task", "web_fetch", "web_search",
				"bash_output", "kill_shell",
			},
			RequireApproval: []string{"write_file", "edit_file", "run_command", "git_commit", "mcp_*"},
		},
		SessionDB:  "~/.local/share/aglet/sessions.db",
		UI:         UIConfig{ShowThoughts: &showThoughts},
		MCPServers: map[string]MCPServerConfig{},
		Hooks:      HooksConfig{},
		Web: WebConfig{
			Fetch: WebFetchConfig{
				Enabled:    &fetchEnabled,
				MaxBytes:   1024 * 1024,
				TimeoutSec: 30,
			},
			Search: WebSearchConfig{
				Enabled:    &searchEnabled,
				Provider:   "tavily",
				APIKeyEnv:  "TAVILY_API_KEY",
				MaxResults: 5,
				TimeoutSec: 20,
			},
		},
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
	if cfg.ApprovalMode == "" {
		cfg.ApprovalMode = d.ApprovalMode
	} else {
		cfg.ApprovalMode = NormalizeApprovalMode(cfg.ApprovalMode)
	}
	if cfg.Agent.MaxLLMCalls == 0 {
		cfg.Agent.MaxLLMCalls = d.Agent.MaxLLMCalls
	}
	if cfg.Agent.Temperature == 0 {
		cfg.Agent.Temperature = d.Agent.Temperature
	}
	if cfg.Context.InstructionFiles == nil {
		cfg.Context.InstructionFiles = d.Context.InstructionFiles
	}
	if cfg.Context.SkillsDirs == nil {
		cfg.Context.SkillsDirs = d.Context.SkillsDirs
	}
	if cfg.Context.AgentsDirs == nil {
		cfg.Context.AgentsDirs = d.Context.AgentsDirs
	}
	if cfg.Context.CommandsDirs == nil {
		cfg.Context.CommandsDirs = d.Context.CommandsDirs
	}
	if cfg.Context.SkillsEnabled == nil {
		enabled := true
		cfg.Context.SkillsEnabled = &enabled
	}
	if cfg.Context.MaxInstructionBytes <= 0 {
		cfg.Context.MaxInstructionBytes = d.Context.MaxInstructionBytes
	}
	if cfg.Context.MaxSkillBytes <= 0 {
		cfg.Context.MaxSkillBytes = d.Context.MaxSkillBytes
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
	cfg.Hooks = normalizeHooks(cfg.Hooks)
	applyWebDefaults(&cfg.Web, d.Web)
	if cfg.SessionDB == "" {
		cfg.SessionDB = d.SessionDB
	}
	if cfg.UI.ShowThoughts == nil {
		cfg.UI.ShowThoughts = d.UI.ShowThoughts
	}
}

func normalizeHooks(in HooksConfig) HooksConfig {
	if len(in) == 0 {
		return HooksConfig{}
	}
	out := make(HooksConfig, len(in))
	for name, matchers := range in {
		event, ok := CanonicalHookEvent(name)
		if !ok {
			event = name
		}
		for i := range matchers {
			matchers[i].Matcher = strings.TrimSpace(matchers[i].Matcher)
			for j := range matchers[i].Hooks {
				hook := &matchers[i].Hooks[j]
				hook.Type = strings.ToLower(strings.TrimSpace(hook.Type))
				if hook.Type == "" {
					hook.Type = "command"
				}
				hook.Command = strings.TrimSpace(hook.Command)
				if hook.Timeout == 0 {
					hook.Timeout = 30
				}
			}
		}
		out[event] = append(out[event], matchers...)
	}
	return out
}

func applyWebDefaults(w *WebConfig, d WebConfig) {
	if w.Fetch.Enabled == nil {
		w.Fetch.Enabled = d.Fetch.Enabled
	}
	if w.Fetch.MaxBytes <= 0 {
		w.Fetch.MaxBytes = d.Fetch.MaxBytes
	}
	if w.Fetch.TimeoutSec <= 0 {
		w.Fetch.TimeoutSec = d.Fetch.TimeoutSec
	}
	if w.Search.Enabled == nil {
		w.Search.Enabled = d.Search.Enabled
	}
	w.Search.Provider = strings.ToLower(strings.TrimSpace(w.Search.Provider))
	if w.Search.Provider == "" {
		w.Search.Provider = d.Search.Provider
	}
	w.Search.BaseURL = strings.TrimSpace(w.Search.BaseURL)
	w.Search.APIKeyEnv = strings.TrimSpace(w.Search.APIKeyEnv)
	if w.Search.APIKeyEnv == "" {
		switch w.Search.Provider {
		case "tavily":
			w.Search.APIKeyEnv = "TAVILY_API_KEY"
		case "brave":
			w.Search.APIKeyEnv = "BRAVE_API_KEY"
		}
	}
	if w.Search.MaxResults <= 0 {
		w.Search.MaxResults = d.Search.MaxResults
	}
	if w.Search.TimeoutSec <= 0 {
		w.Search.TimeoutSec = d.Search.TimeoutSec
	}
}

func (c *Config) Validate() error {
	switch c.ApprovalMode {
	case ApprovalAsk, ApprovalReadOnly, ApprovalAutoEdit, ApprovalFullAuto:
	default:
		return fmt.Errorf("config: approval_mode %q is invalid (want ask, read-only, auto-edit or full-auto)", c.ApprovalMode)
	}
	comp := c.Agent.Compaction
	if comp.TokenThreshold < 0 || comp.EventRetentionSize < 0 || comp.CompactionInterval < 0 || comp.OverlapSize < 0 {
		return fmt.Errorf("config: agent.compaction values must be non-negative")
	}
	if comp.TokenThreshold == 0 && comp.CompactionInterval == 0 {
		if comp.EventRetentionSize > 0 || comp.OverlapSize > 0 {
			return fmt.Errorf("config: agent.compaction needs token_threshold or compaction_interval to enable a strategy")
		}
	} else {
		if comp.TokenThreshold > 0 && comp.EventRetentionSize <= 0 {
			return fmt.Errorf("config: agent.compaction.event_retention_size must be > 0 when token_threshold is set")
		}
		if comp.EventRetentionSize > 0 && comp.TokenThreshold == 0 {
			return fmt.Errorf("config: agent.compaction.event_retention_size requires token_threshold")
		}
		if comp.OverlapSize > 0 && comp.CompactionInterval == 0 {
			return fmt.Errorf("config: agent.compaction.overlap_size requires compaction_interval")
		}
		if comp.CompactionInterval > 0 && comp.EventRetentionSize > 0 && comp.CompactionInterval < comp.EventRetentionSize {
			return fmt.Errorf("config: agent.compaction.compaction_interval should be >= event_retention_size")
		}
	}
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
	if err := validateHooks(c.Hooks); err != nil {
		return err
	}
	if err := validateWeb(c.Web); err != nil {
		return err
	}
	return nil
}

func validateHooks(hooks HooksConfig) error {
	for name, matchers := range hooks {
		event, ok := CanonicalHookEvent(name)
		if !ok {
			return fmt.Errorf("config: hooks has unknown event %q (want UserPromptSubmit, PreToolUse, PostToolUse, SessionStart, SessionEnd or Stop)", name)
		}
		for i, matcher := range matchers {
			if matcher.Matcher != "" {
				if !HookMatcherAllowed(event) {
					return fmt.Errorf("config: hooks.%s[%d].matcher is not supported for %s", event, i, event)
				}
				if _, err := regexp.Compile("^(?:" + matcher.Matcher + ")$"); err != nil {
					return fmt.Errorf("config: hooks.%s[%d].matcher: %w", event, i, err)
				}
			}
			if len(matcher.Hooks) == 0 {
				return fmt.Errorf("config: hooks.%s[%d] has no hooks", event, i)
			}
			for j, hook := range matcher.Hooks {
				if hook.Type != "" && hook.Type != "command" {
					return fmt.Errorf("config: hooks.%s[%d].hooks[%d].type %q is invalid (want command)", event, i, j, hook.Type)
				}
				if hook.Command == "" {
					return fmt.Errorf("config: hooks.%s[%d].hooks[%d].command is required", event, i, j)
				}
				if hook.Timeout < 0 || hook.Timeout > 600 {
					return fmt.Errorf("config: hooks.%s[%d].hooks[%d].timeout must be 0..600 seconds", event, i, j)
				}
			}
		}
	}
	return nil
}

func validateWeb(web WebConfig) error {
	if web.Fetch.MaxBytes < 0 || web.Fetch.MaxBytes > 5*1024*1024 {
		return fmt.Errorf("config: web.fetch.max_bytes must be 0..%d", 5*1024*1024)
	}
	if web.Fetch.TimeoutSec < 0 || web.Fetch.TimeoutSec > 120 {
		return fmt.Errorf("config: web.fetch.timeout_sec must be 0..120")
	}
	for _, host := range web.Fetch.AllowedHosts {
		host = strings.TrimSpace(host)
		if host == "" || strings.Contains(host, "://") || strings.Contains(host, "/") {
			return fmt.Errorf("config: web.fetch.allowed_hosts entry %q must be a hostname (optionally *.example.com)", host)
		}
	}
	provider := strings.ToLower(strings.TrimSpace(web.Search.Provider))
	switch provider {
	case "", "tavily", "brave", "searxng":
	default:
		return fmt.Errorf("config: web.search.provider %q is invalid (want tavily, brave or searxng)", web.Search.Provider)
	}
	if web.Search.MaxResults < 0 || web.Search.MaxResults > 20 {
		return fmt.Errorf("config: web.search.max_results must be 0..20")
	}
	if web.Search.TimeoutSec < 0 || web.Search.TimeoutSec > 120 {
		return fmt.Errorf("config: web.search.timeout_sec must be 0..120")
	}
	if !web.SearchEnabledValue() {
		return nil
	}
	switch provider {
	case "tavily", "brave":
		if web.Search.APIKeyEnv == "" {
			return fmt.Errorf("config: web.search.api_key_env is required for %s", provider)
		}
	case "searxng":
		if web.Search.BaseURL == "" {
			return fmt.Errorf("config: web.search.base_url is required for searxng")
		}
	}
	if web.Search.BaseURL != "" {
		u, err := url.Parse(web.Search.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("config: web.search.base_url must be an http(s) URL")
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
  "approval_mode": "ask",
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
    "system_prompt_file": "",
    "compaction": {
      "token_threshold": 0,
      "event_retention_size": 0,
      "compaction_interval": 0,
      "overlap_size": 0
    }
  },
  "context": {
    "instruction_files": ["AGLET.md", "AGENTS.md", "AGENTS.override.md", "CLAUDE.md"],
    "skills_dirs": [".agents/skills", ".aglet/skills", "~/.agents/skills", "~/.config/aglet/skills"],
    "agents_dirs": [".aglet/agents", ".agents/agents", "~/.config/aglet/agents"],
    "commands_dirs": [".aglet/commands", ".agents/commands", "~/.config/aglet/commands"],
    "skills_enabled": true,
    "max_instruction_bytes": 32768,
    "max_skill_bytes": 65536
  },
  "workspace": ".",
  "permissions": {
    "auto_approve": ["read_file", "list_dir", "glob", "grep", "git_status", "git_diff", "git_log", "load_skill", "update_plan", "task", "web_fetch", "web_search", "bash_output", "kill_shell"],
    "require_approval": ["write_file", "edit_file", "run_command", "git_commit", "mcp_*"],
    "deny": []
  },
  "hooks": {},
  "web": {
    "fetch": {
      "enabled": true,
      "max_bytes": 1048576,
      "timeout_sec": 30,
      "allow_private_networks": false,
      "allowed_hosts": []
    },
    "search": {
      "enabled": false,
      "provider": "tavily",
      "base_url": "",
      "api_key_env": "TAVILY_API_KEY",
      "max_results": 5,
      "timeout_sec": 20
    }
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
