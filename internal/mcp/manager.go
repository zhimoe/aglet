package mcp

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"

	"github.com/zhimoe/aglet/internal/config"
	"github.com/zhimoe/aglet/internal/permissions"
	"github.com/zhimoe/aglet/internal/version"
)

const maxMCPResultBytes = 100 * 1024

// Manager owns MCP client sessions and exposes their tools as ADK tools.
type Manager struct {
	tools   []tool.Tool
	closers []io.Closer
	log     *slog.Logger
}

// New connects to every enabled MCP server and discovers its tools.
func New(ctx context.Context, servers map[string]config.MCPServerConfig, policy *permissions.Policy, approver permissions.Approver, strict bool, logger *slog.Logger) (*Manager, error) {
	if logger == nil {
		logger = slog.Default()
	}
	m := &Manager{log: logger}
	names := make([]string, 0, len(servers))
	for name, s := range servers {
		if s.IsEnabled() {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	used := map[string]bool{}
	for _, name := range names {
		s := servers[name]
		timeout := time.Duration(s.ConnectTimeoutSec) * time.Second
		if timeout <= 0 {
			timeout = 10 * time.Second
		}
		connectCtx, cancel := context.WithTimeout(ctx, timeout)
		cs, err := connect(connectCtx, s, name)
		cancel()
		if err != nil {
			if strict {
				_ = m.Close()
				return nil, fmt.Errorf("mcp server %s: %w", name, err)
			}
			logger.Warn("mcp server unavailable", "server", name, "error", err)
			continue
		}
		m.closers = append(m.closers, closerFunc(func() error { return cs.Close() }))
		tools, err := discoverTools(ctx, name, s, cs, policy, approver, used)
		if err != nil {
			if strict {
				_ = m.Close()
				return nil, fmt.Errorf("mcp server %s: %w", name, err)
			}
			logger.Warn("mcp tool discovery failed", "server", name, "error", err)
			continue
		}
		m.tools = append(m.tools, tools...)
		logger.Info("mcp server connected", "server", name, "tools", len(tools))
	}
	return m, nil
}

func connect(ctx context.Context, s config.MCPServerConfig, name string) (*mcpsdk.ClientSession, error) {
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "aglet", Version: version.Version}, nil)
	var transport mcpsdk.Transport
	switch s.Transport {
	case "stdio":
		cmd := exec.Command(s.Command, s.Args...)
		cmd.Env = os.Environ()
		for k, v := range s.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		transport = &mcpsdk.CommandTransport{Command: cmd}
	case "sse":
		transport = &mcpsdk.SSEClientTransport{Endpoint: s.URL, HTTPClient: httpClient(s.Headers)}
	case "http":
		transport = &mcpsdk.StreamableClientTransport{Endpoint: s.URL, HTTPClient: httpClient(s.Headers)}
	default:
		return nil, fmt.Errorf("unsupported transport %q", s.Transport)
	}
	cs, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, err
	}
	return cs, nil
}

func httpClient(headers map[string]string) *http.Client {
	if len(headers) == 0 {
		return http.DefaultClient
	}
	return &http.Client{Transport: &headerRoundTripper{base: http.DefaultTransport, headers: headers}}
}

type headerRoundTripper struct {
	base    http.RoundTripper
	headers map[string]string
}

func (h *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	for k, v := range h.headers {
		r.Header.Set(k, v)
	}
	return h.base.RoundTrip(r)
}

func discoverTools(ctx context.Context, serverName string, s config.MCPServerConfig, cs *mcpsdk.ClientSession, policy *permissions.Policy, approver permissions.Approver, used map[string]bool) ([]tool.Tool, error) {
	res, err := cs.ListTools(ctx, &mcpsdk.ListToolsParams{})
	if err != nil {
		return nil, err
	}
	var out []tool.Tool
	for _, mt := range res.Tools {
		if mt == nil {
			continue
		}
		name := uniqueToolName(s.ToolPrefix, serverName, mt.Name, used)
		schema, err := toInputSchema(mt.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("tool %s schema: %w", mt.Name, err)
		}
		desc := mt.Description
		if desc == "" {
			desc = fmt.Sprintf("MCP tool %s from server %s", mt.Name, serverName)
		}
		mcpName := mt.Name
		server := serverName
		needsConfirm := s.NeedsConfirmation()
		handler := func(ctx adkagent.Context, args map[string]any) (map[string]any, error) {
			if args == nil {
				args = map[string]any{}
			}
			summary := fmt.Sprintf("%s/%s", server, mcpName)
			if policy != nil {
				if err := policy.Authorize(ctx, permissions.ApprovalRequest{
					ToolName: name, Server: server, Summary: summary, Args: args,
				}, approver); err != nil {
					return nil, err
				}
			} else if needsConfirm {
				if err := authorizeMCP(ctx, name, server, summary, args, approver); err != nil {
					return nil, err
				}
			}
			callRes, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: mcpName, Arguments: args})
			if err != nil {
				return nil, err
			}
			text := formatContent(callRes)
			text, truncated := truncate(text, maxMCPResultBytes)
			return map[string]any{
				"content":   text,
				"is_error":  callRes.IsError,
				"truncated": truncated,
			}, nil
		}
		t, err := functiontool.New[map[string]any, map[string]any](functiontool.Config{
			Name:        name,
			Description: desc,
			InputSchema: schema,
		}, handler)
		if err != nil {
			return nil, fmt.Errorf("tool %s: %w", mt.Name, err)
		}
		out = append(out, t)
	}
	return out, nil
}

func authorizeMCP(ctx context.Context, name, server, summary string, args map[string]any, approver permissions.Approver) error {
	if approver == nil {
		return fmt.Errorf("tool %q requires approval but no approver is configured", name)
	}
	d, err := approver.Approve(ctx, permissions.ApprovalRequest{
		ToolName: name, Server: server, Summary: summary, Args: args,
	})
	if err != nil {
		return err
	}
	if d == permissions.DecisionDeny {
		return fmt.Errorf("tool %q rejected by user", name)
	}
	return nil
}

func toInputSchema(v any) (*jsonschema.Schema, error) {
	if v == nil {
		return &jsonschema.Schema{Type: "object"}, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func uniqueToolName(prefix, server, toolName string, used map[string]bool) string {
	if prefix == "" {
		prefix = "mcp"
	}
	raw := prefix + "_" + server + "_" + toolName
	sanitized := sanitizeName(raw)
	if len(sanitized) > 64 {
		sum := sha1.Sum([]byte(sanitized))
		suffix := hex.EncodeToString(sum[:4])
		sanitized = sanitized[:55] + "_" + suffix
	}
	if used[sanitized] {
		sum := sha1.Sum([]byte(raw))
		suffix := hex.EncodeToString(sum[:4])
		prefixLen := len(sanitized)
		if prefixLen > 55 {
			prefixLen = 55
		}
		sanitized = sanitized[:prefixLen] + "_" + suffix
	}
	used[sanitized] = true
	return sanitized
}

func sanitizeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		out = "mcp_tool"
	}
	return out
}

func formatContent(res *mcpsdk.CallToolResult) string {
	if res == nil {
		return ""
	}
	var parts []string
	for _, c := range res.Content {
		switch v := c.(type) {
		case *mcpsdk.TextContent:
			parts = append(parts, v.Text)
		case *mcpsdk.ResourceLink:
			parts = append(parts, fmt.Sprintf("[resource %s %s]", v.Name, v.URI))
		default:
			b, err := json.Marshal(c)
			if err != nil {
				parts = append(parts, fmt.Sprintf("%v", c))
			} else {
				parts = append(parts, string(b))
			}
		}
	}
	if len(parts) == 0 && res.StructuredContent != nil {
		b, err := json.Marshal(res.StructuredContent)
		if err == nil {
			parts = append(parts, string(b))
		}
	}
	return strings.Join(parts, "\n")
}

func truncate(s string, max int) (string, bool) {
	if len(s) <= max {
		return s, false
	}
	return s[:max] + "\n...[truncated]", true
}

type closerFunc func() error

func (f closerFunc) Close() error { return f() }

func (m *Manager) Tools() []tool.Tool {
	out := make([]tool.Tool, len(m.tools))
	copy(out, m.tools)
	return out
}

func (m *Manager) Close() error {
	var errs []error
	for _, c := range m.closers {
		if err := c.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	m.closers = nil
	return errors.Join(errs...)
}
