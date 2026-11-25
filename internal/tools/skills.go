package tools

import (
	"fmt"

	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"

	"github.com/zhimoe/aglet/internal/skills"
)

type loadSkillArgs struct {
	Name string `json:"name" jsonschema:"Skill name from the available_skills list."`
}

type loadSkillResult struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Path        string `json:"path"`
	Content     string `json:"content"`
}

func newLoadSkillTool(deps Deps) (tool.Tool, error) {
	handler := func(ctx adkagent.Context, args loadSkillArgs) (loadSkillResult, error) {
		if err := authorize(ctx, deps, "load_skill", "Load skill "+args.Name, map[string]any{"name": args.Name}); err != nil {
			return loadSkillResult{}, err
		}
		s, ok := skills.Find(deps.Skills, args.Name)
		if !ok {
			return loadSkillResult{}, fmt.Errorf("skill %q not found", args.Name)
		}
		return loadSkillResult{Name: s.Name, Description: s.Description, Path: s.Path, Content: s.Body}, nil
	}
	return functiontool.New(functiontool.Config{
		Name:        "load_skill",
		Description: "Load the full instructions for a skill listed in available_skills.",
	}, handler)
}
