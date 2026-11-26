package agent

import (
	"fmt"
	"strings"

	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"

	"github.com/zhimoe/aglet/internal/permissions"
	"github.com/zhimoe/aglet/internal/subagents"
)

type taskArgs struct {
	SubagentType string `json:"subagent_type" jsonschema:"Subagent type. Use one of the names listed in the tool description."`
	Description  string `json:"description" jsonschema:"Short label for the delegated task."`
	Prompt       string `json:"prompt" jsonschema:"Complete, self-contained instructions and expected output."`
}

type taskResult struct {
	Subagent  string `json:"subagent"`
	Result    string `json:"result"`
	ToolCalls int    `json:"tool_calls,omitzero"`
	Truncated bool   `json:"truncated,omitzero"`
}

func newTaskTool(rt *Runtime) (tool.Tool, error) {
	description := taskDescription(rt.subagentList)
	handler := func(ctx adkagent.Context, args taskArgs) (taskResult, error) {
		if strings.TrimSpace(args.SubagentType) == "" {
			return taskResult{}, fmt.Errorf("subagent_type is required")
		}
		if strings.TrimSpace(args.Prompt) == "" {
			return taskResult{}, fmt.Errorf("prompt is required")
		}
		def, ok := subagents.Find(rt.subagentList, strings.TrimSpace(args.SubagentType))
		if !ok {
			return taskResult{}, fmt.Errorf("unknown subagent_type %q; available: %s", args.SubagentType, subagentNames(rt.subagentList))
		}
		description := strings.TrimSpace(args.Description)
		if description == "" {
			description = "subagent task"
		}
		if policy := rt.toolDeps.Policy; policy != nil {
			if err := policy.Authorize(ctx, permissions.ApprovalRequest{
				ToolName: "task",
				Agent:    ctx.AgentName(),
				Summary:  "Delegate to " + def.Name + ": " + description,
				Args: map[string]any{
					"subagent_type": def.Name,
					"description":   description,
					"prompt":        args.Prompt,
				},
			}, rt.toolDeps.Approver); err != nil {
				return taskResult{}, err
			}
		}
		rt.hub.Notice("subagent " + def.Name + " started: " + description)
		run, err := rt.runSubagent(ctx, def, description, args.Prompt)
		if err != nil {
			rt.hub.Notice("subagent " + def.Name + " failed: " + err.Error())
			return taskResult{}, err
		}
		rt.hub.Notice(fmt.Sprintf("subagent %s finished (%d tool calls)", def.Name, run.ToolCalls))
		return taskResult{
			Subagent: def.Name, Result: run.Text,
			ToolCalls: run.ToolCalls, Truncated: run.Truncated,
		}, nil
	}
	return functiontool.New(functiontool.Config{
		Name:        "task",
		Description: description,
	}, handler)
}

func taskDescription(defs []subagents.Definition) string {
	var b strings.Builder
	b.WriteString("Delegate a self-contained task to a subagent. The subagent cannot see this conversation, so the prompt must include every relevant detail and the expected output. It cannot ask the user questions. Do not delegate work that is small enough to do directly.\n\nAvailable subagent types:\n")
	for _, def := range defs {
		fmt.Fprintf(&b, "- %s: %s\n", def.Name, def.Description)
	}
	return b.String()
}

func subagentNames(defs []subagents.Definition) string {
	names := make([]string, 0, len(defs))
	for _, def := range defs {
		names = append(names, def.Name)
	}
	return strings.Join(names, ", ")
}
