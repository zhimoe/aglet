package agent

import (
	"context"
	"fmt"
	"os"
	"path"
	"slices"
	"strings"
	"unicode"

	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/model/openaimodel"
	"google.golang.org/adk/v2/plugin"
	"google.golang.org/adk/v2/runner"
	adksession "google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/genai"

	"github.com/zhimoe/aglet/internal/config"
	"github.com/zhimoe/aglet/internal/plan"
	"github.com/zhimoe/aglet/internal/subagents"
	"github.com/zhimoe/aglet/internal/tools"
)

const maxSubagentResult = 64 * 1024

type subagentRun struct {
	Text      string
	ToolCalls int
	Truncated bool
}

func (r *Runtime) runSubagent(ctx context.Context, def subagents.Definition, description, prompt string) (subagentRun, error) {
	select {
	case r.subagentSem <- struct{}{}:
		defer func() { <-r.subagentSem }()
	case <-ctx.Done():
		return subagentRun{}, ctx.Err()
	}

	childLLM, err := r.modelFor(def.Model)
	if err != nil {
		return subagentRun{}, err
	}
	childDeps := r.toolDeps
	childDeps.Plan = plan.New()
	localTools, err := tools.NewLocalTools(childDeps)
	if err != nil {
		return subagentRun{}, err
	}
	allTools := localTools
	if r.mcp != nil {
		allTools = append(allTools, r.mcp.Tools()...)
	}
	childTools, missing := filterSubagentTools(allTools, def.Tools)
	for _, name := range missing {
		if name == "web_fetch" || name == "web_search" {
			r.log.Debug("optional subagent tool is disabled", "subagent", def.Name, "tool", name)
			continue
		}
		r.log.Warn("subagent tool not found", "subagent", def.Name, "tool", name)
	}
	hasLoadSkill := slices.ContainsFunc(childTools, func(t tool.Tool) bool { return t.Name() == "load_skill" })
	systemPrompt := BuildSubagentPrompt(def, childDeps.Workspace, r.instr, r.skillList, hasLoadSkill)
	userMessage := "Task: " + description + "\n\n" + prompt

	childAgent, err := llmagent.New(llmagent.Config{
		Name:        subagentAgentName(def.Name),
		Description: def.Description,
		Model:       childLLM,
		InstructionProvider: func(adkagent.ReadonlyContext) (string, error) {
			return systemPrompt, nil
		},
		Tools:                 childTools,
		GenerateContentConfig: r.genConfig,
	})
	if err != nil {
		return subagentRun{}, fmt.Errorf("create subagent %s: %w", def.Name, err)
	}
	var plugins []*plugin.Plugin
	if r.toolHookPlugin != nil {
		plugins = append(plugins, r.toolHookPlugin)
	}
	childRunner, err := runner.New(runner.Config{
		AppName:           r.appName,
		Agent:             childAgent,
		SessionService:    adksession.InMemoryService(),
		AutoCreateSession: true,
		PluginConfig:      runner.PluginConfig{Plugins: plugins},
	})
	if err != nil {
		return subagentRun{}, fmt.Errorf("create subagent runner: %w", err)
	}
	maxCalls := r.cfg.Agent.MaxLLMCalls
	if maxCalls == 0 {
		maxCalls = 50
	}
	input := genai.NewContentFromText(userMessage, genai.RoleUser)
	var lastText string
	var usage Usage
	toolCalls := 0
	for ev, err := range childRunner.Run(ctx, r.userID, "", input, adkagent.RunConfig{
		StreamingMode: adkagent.StreamingModeNone,
		MaxLLMCalls:   maxCalls,
	}) {
		if err != nil {
			if ctx.Err() != nil {
				return subagentRun{}, ctx.Err()
			}
			return subagentRun{}, err
		}
		if ev == nil {
			continue
		}
		if ev.ErrorCode != "" {
			return subagentRun{}, fmt.Errorf("%s: %s", ev.ErrorCode, ev.ErrorMessage)
		}
		if ev.Partial {
			continue
		}
		if ev.UsageMetadata != nil {
			usage.Prompt += ev.UsageMetadata.PromptTokenCount
			usage.Completion += ev.UsageMetadata.CandidatesTokenCount
			usage.Total += ev.UsageMetadata.TotalTokenCount
		}
		if ev.Content == nil {
			continue
		}
		var text strings.Builder
		for _, part := range ev.Content.Parts {
			if part == nil {
				continue
			}
			if part.FunctionCall != nil {
				toolCalls++
			}
			if part.Thought || part.Text == "" {
				continue
			}
			text.WriteString(part.Text)
		}
		if text.Len() > 0 {
			lastText = text.String()
		}
	}
	if strings.TrimSpace(lastText) == "" {
		lastText = "(subagent produced no text response)"
	}
	if usage.Total > 0 {
		u := usage
		r.hub.Emit(Event{Type: EventUsage, Author: def.Name, Usage: &u})
	}
	truncated := len(lastText) > maxSubagentResult
	if truncated {
		lastText = lastText[:maxSubagentResult] + "\n...[truncated]"
	}
	return subagentRun{Text: lastText, ToolCalls: toolCalls, Truncated: truncated}, nil
}

func filterSubagentTools(all []tool.Tool, patterns []string) ([]tool.Tool, []string) {
	if len(patterns) == 0 {
		out := make([]tool.Tool, 0, len(all))
		for _, t := range all {
			if t.Name() != "task" {
				out = append(out, t)
			}
		}
		return out, nil
	}
	matched := make([]bool, len(patterns))
	var out []tool.Tool
	for _, t := range all {
		name := t.Name()
		if name == "task" {
			continue
		}
		for i, pattern := range patterns {
			ok, err := path.Match(pattern, name)
			if err != nil {
				ok = pattern == name
			}
			if ok || pattern == name {
				matched[i] = true
				out = append(out, t)
				break
			}
		}
	}
	var missing []string
	for i, pattern := range patterns {
		if !matched[i] {
			missing = append(missing, pattern)
		}
	}
	return out, missing
}

func subagentAgentName(name string) string {
	var b strings.Builder
	b.WriteString("subagent_")
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

func (r *Runtime) modelFor(name string) (model.LLM, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.EqualFold(name, "inherit") {
		if r.testLLM != nil {
			return r.testLLM, nil
		}
		return r.llm, nil
	}
	if r.testLLM != nil {
		return r.testLLM, nil
	}
	if (name == r.profile || name == r.modelName) && r.llm != nil {
		return r.llm, nil
	}
	r.modelMu.Lock()
	defer r.modelMu.Unlock()
	key := name
	if _, ok := r.cfg.Profiles[name]; ok {
		if cached, ok := r.models[key]; ok {
			return cached, nil
		}
		llm, err := buildModel(r.cfg.Profiles[name])
		if err != nil {
			return nil, err
		}
		r.models[key] = llm
		return llm, nil
	}
	profile, ok := r.cfg.Profiles[r.profile]
	if !ok {
		return nil, fmt.Errorf("profile %q not found", r.profile)
	}
	key = r.profile + "/" + name
	if cached, ok := r.models[key]; ok {
		return cached, nil
	}
	profile.Model = name
	llm, err := buildModel(profile)
	if err != nil {
		return nil, err
	}
	r.models[key] = llm
	return llm, nil
}

func buildModel(profile config.Profile) (model.LLM, error) {
	apiKey := os.Getenv(profile.APIKeyEnv)
	if apiKey == "" {
		return nil, fmt.Errorf("environment variable %s is empty (required for model %s)", profile.APIKeyEnv, profile.Model)
	}
	api := openaimodel.APIChatCompletions
	if profile.API == "responses" {
		api = openaimodel.APIResponses
	}
	return openaimodel.NewModel(context.Background(), profile.Model, &openaimodel.ClientConfig{
		APIKey:  apiKey,
		BaseURL: profile.BaseURL,
		API:     api,
	})
}
