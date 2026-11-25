package tools

import (
	"fmt"
	"strings"

	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"

	"github.com/zhimoe/aglet/internal/plan"
)

type planStepArg struct {
	Step   string `json:"step" jsonschema:"Short description of the step."`
	Status string `json:"status" jsonschema:"pending, in_progress, or completed."`
}

type updatePlanArgs struct {
	Explanation string        `json:"explanation,omitempty" jsonschema:"Optional note about the plan change."`
	Plan        []planStepArg `json:"plan" jsonschema:"Ordered plan steps."`
}

type updatePlanResult struct {
	Explanation string      `json:"explanation,omitempty"`
	Plan        []plan.Step `json:"plan"`
}

func newUpdatePlanTool(deps Deps) (tool.Tool, error) {
	handler := func(ctx adkagent.Context, args updatePlanArgs) (updatePlanResult, error) {
		if err := authorize(ctx, deps, "update_plan", "Update plan", map[string]any{"plan": args.Plan}); err != nil {
			return updatePlanResult{}, err
		}
		if deps.Plan == nil {
			return updatePlanResult{}, fmt.Errorf("plan board is not configured")
		}
		steps := make([]plan.Step, 0, len(args.Plan))
		for _, s := range args.Plan {
			status := normalizeStatus(s.Status)
			steps = append(steps, plan.Step{Step: s.Step, Status: status})
		}
		if err := deps.Plan.Update(steps); err != nil {
			return updatePlanResult{}, err
		}
		return updatePlanResult{Explanation: args.Explanation, Plan: deps.Plan.Steps()}, nil
	}
	return functiontool.New(functiontool.Config{
		Name:        "update_plan",
		Description: "Create or update a short task plan. Use for multi-step work; keep statuses current.",
	}, handler)
}

func normalizeStatus(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "pending", "todo":
		return plan.StatusPending
	case "in_progress", "in-progress", "active", "doing", "running":
		return plan.StatusInProgress
	case "completed", "complete", "done", "finished":
		return plan.StatusCompleted
	default:
		return s
	}
}
