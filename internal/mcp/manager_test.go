package mcp

import (
	"context"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/zhimoe/aglet/internal/config"
	"github.com/zhimoe/aglet/internal/permissions"
)

type echoIn struct {
	Text string `json:"text"`
}

func boolPtr(v bool) *bool { return &v }

func TestDiscoverToolsInMemory(t *testing.T) {
	ctx := context.Background()
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "test-server", Version: "1"}, nil)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "echo", Description: "Echo text"}, func(
		_ context.Context, _ *mcpsdk.CallToolRequest, in echoIn,
	) (*mcpsdk.CallToolResult, any, error) {
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: in.Text}}}, nil, nil
	})

	t1, t2 := mcpsdk.NewInMemoryTransports()
	ss, err := server.Connect(ctx, t1, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-client", Version: "1"}, nil)
	cs, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	policy := permissions.NewPolicy(config.PermissionsConfig{AutoApprove: []string{"*"}})
	tools, err := discoverTools(ctx, "test", config.MCPServerConfig{
		Transport: "stdio", RequireConfirmation: boolPtr(true),
	}, cs, policy, nil, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 {
		t.Fatalf("tools = %d", len(tools))
	}
	if !strings.Contains(tools[0].Name(), "echo") {
		t.Fatalf("name = %q", tools[0].Name())
	}
	if tools[0].Description() != "Echo text" {
		t.Fatalf("description = %q", tools[0].Description())
	}
}

func TestToInputSchema(t *testing.T) {
	s, err := toInputSchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"text": map[string]any{"type": "string"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if s == nil || s.Type != "object" {
		t.Fatalf("schema = %#v", s)
	}
}

func TestUniqueToolName(t *testing.T) {
	used := map[string]bool{}
	a := uniqueToolName("", "my server", "do.thing", used)
	b := uniqueToolName("", "my server", "do.thing", used)
	if a == b {
		t.Fatalf("expected collision resolution, got %q twice", a)
	}
	if len(a) > 64 || len(b) > 64 {
		t.Fatalf("name too long: %q %q", a, b)
	}
	if strings.ContainsAny(a, " .") {
		t.Fatalf("invalid chars in %q", a)
	}
	if !strings.HasPrefix(a, "mcp_my_server_do_thing") {
		t.Fatalf("unexpected name %q", a)
	}
}
