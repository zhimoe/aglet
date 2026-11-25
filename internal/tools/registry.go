package tools

import (
	"context"
	"fmt"
	"path/filepath"

	"google.golang.org/adk/v2/tool"

	"github.com/zhimoe/aglet/internal/permissions"
)

// Deps carries shared state for local tools.
type Deps struct {
	Workspace string
	Policy    *permissions.Policy
	Approver  permissions.Approver
}

// NewLocalTools constructs the built-in local tool set.
func NewLocalTools(deps Deps) ([]tool.Tool, error) {
	if deps.Workspace == "" {
		deps.Workspace = "."
	}
	abs, err := filepath.Abs(deps.Workspace)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}
	deps.Workspace = abs

	builders := []func(Deps) (tool.Tool, error){
		newReadFileTool,
		newWriteFileTool,
		newEditFileTool,
		newListDirTool,
		newGlobTool,
		newGrepTool,
		newRunCommandTool,
	}
	out := make([]tool.Tool, 0, len(builders))
	for _, build := range builders {
		t, err := build(deps)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

func authorize(ctx context.Context, deps Deps, toolName, summary string, args map[string]any) error {
	if deps.Policy == nil {
		return nil
	}
	return deps.Policy.Authorize(ctx, toolName, "", summary, args, deps.Approver)
}
