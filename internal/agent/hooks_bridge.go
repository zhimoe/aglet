package agent

import (
	"fmt"
	"maps"
	"strings"
	"sync"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/plugin"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/genai"

	"github.com/zhimoe/aglet/internal/config"
	"github.com/zhimoe/aglet/internal/hooks"
)

const rootAgentName = "aglet"

// hookBridge adapts the hooks runner to ADK plugin callbacks.
type hookBridge struct {
	runner     *hooks.Runner
	hub        *emitterHub
	denied     sync.Map
	preContext sync.Map
}

func newHookBridge(runner *hooks.Runner, hub *emitterHub) *hookBridge {
	return &hookBridge{runner: runner, hub: hub}
}

// plugins returns the full plugin for the root runner and a tool-only plugin
// for nested subagent runners.
func (b *hookBridge) plugins() (mainPlugin, toolPlugin *plugin.Plugin, err error) {
	if b == nil || b.runner == nil {
		return nil, nil, nil
	}
	if !b.runner.Has(config.HookUserPromptSubmit) && !b.runner.HasToolEvents() && !b.runner.Has(config.HookStop) {
		return nil, nil, nil
	}
	mainCfg := plugin.Config{Name: "aglet-hooks"}
	if b.runner.Has(config.HookUserPromptSubmit) {
		mainCfg.OnUserMessageCallback = b.onUserMessage
	}
	if b.runner.HasToolEvents() {
		mainCfg.BeforeToolCallback = b.beforeTool
		mainCfg.AfterToolCallback = b.afterTool
	}
	if b.runner.Has(config.HookStop) {
		mainCfg.AfterRunCallback = b.afterRun
	}
	mainPlugin, err = plugin.New(mainCfg)
	if err != nil {
		return nil, nil, err
	}
	if b.runner.HasToolEvents() {
		toolPlugin, err = plugin.New(plugin.Config{
			Name:               "aglet-hooks-tools",
			BeforeToolCallback: b.beforeTool,
			AfterToolCallback:  b.afterTool,
		})
		if err != nil {
			return nil, nil, err
		}
	}
	return mainPlugin, toolPlugin, nil
}

func (b *hookBridge) notice(result hooks.Result) {
	if b == nil || b.hub == nil {
		return
	}
	for _, msg := range result.SystemMessages {
		b.hub.Notice(msg)
	}
	for _, msg := range result.Warnings {
		b.hub.Notice("hook warning: " + msg)
	}
}

func (b *hookBridge) beforeTool(ctx agent.Context, t tool.Tool, args map[string]any) (map[string]any, error) {
	res := b.runner.Run(ctx, config.HookPreToolUse, hooks.Input{
		ToolName:  t.Name(),
		ToolInput: args,
		ToolUseID: ctx.FunctionCallID(),
	})
	b.notice(res)
	if !res.Blocked {
		if id := ctx.FunctionCallID(); id != "" && res.AdditionalContext != "" {
			b.preContext.Store(id, res.AdditionalContext)
		}
		return nil, nil
	}
	if id := ctx.FunctionCallID(); id != "" {
		b.denied.Store(id, struct{}{})
	}
	return map[string]any{
		"error":        "blocked by PreToolUse hook: " + res.Reason,
		"hook_blocked": true,
	}, nil
}

func (b *hookBridge) afterTool(ctx agent.Context, t tool.Tool, args, result map[string]any, toolErr error) (map[string]any, error) {
	callID := ctx.FunctionCallID()
	if callID != "" {
		if _, blocked := b.denied.LoadAndDelete(callID); blocked {
			return nil, nil
		}
	}
	var preContext string
	if callID != "" {
		if value, ok := b.preContext.LoadAndDelete(callID); ok {
			preContext, _ = value.(string)
		}
	}
	input := hooks.Input{
		ToolName:     t.Name(),
		ToolInput:    args,
		ToolUseID:    callID,
		ToolResponse: result,
	}
	if toolErr != nil {
		input.ToolError = toolErr.Error()
	}
	res := b.runner.Run(ctx, config.HookPostToolUse, input)
	b.notice(res)
	if toolErr != nil {
		return nil, nil
	}
	out := maps.Clone(result)
	changed := false
	if preContext != "" {
		if out == nil {
			out = map[string]any{}
		}
		out["hook_context"] = preContext
		changed = true
	}
	if res.AdditionalContext != "" {
		if out == nil {
			out = map[string]any{}
		}
		if existing, ok := out["hook_context"].(string); ok && existing != "" {
			out["hook_context"] = existing + "\n" + res.AdditionalContext
		} else {
			out["hook_context"] = res.AdditionalContext
		}
		changed = true
	}
	if res.Blocked {
		if out == nil {
			out = map[string]any{}
		}
		out["hook_feedback"] = res.Reason
		changed = true
	}
	if changed {
		return out, nil
	}
	return nil, nil
}

func (b *hookBridge) onUserMessage(ctx agent.InvocationContext, content *genai.Content) (*genai.Content, error) {
	res := b.runner.Run(ctx, config.HookUserPromptSubmit, hooks.Input{Prompt: contentText(content)})
	b.notice(res)
	if res.Blocked {
		return nil, fmt.Errorf("prompt blocked by UserPromptSubmit hook: %s", res.Reason)
	}
	if res.AdditionalContext == "" || content == nil {
		return content, nil
	}
	ctxPart := &genai.Part{Text: "\n<hook_context event=\"UserPromptSubmit\">\n" + res.AdditionalContext + "\n</hook_context>"}
	parts := make([]*genai.Part, 0, len(content.Parts)+1)
	parts = append(parts, ctxPart)
	parts = append(parts, content.Parts...)
	clone := *content
	clone.Parts = parts
	return &clone, nil
}

func (b *hookBridge) afterRun(ctx agent.InvocationContext) {
	if ctx == nil || ctx.Err() != nil {
		return
	}
	if a := ctx.Agent(); a == nil || a.Name() != rootAgentName {
		return
	}
	res := b.runner.Run(ctx, config.HookStop, hooks.Input{})
	b.notice(res)
	if res.Blocked && b.hub != nil {
		b.hub.Notice("Stop hook requested to stop: " + res.Reason + " (aglet only supports observational Stop hooks)")
	}
}

func contentText(content *genai.Content) string {
	if content == nil {
		return ""
	}
	var b strings.Builder
	for _, p := range content.Parts {
		if p == nil || p.Thought || p.Text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(p.Text)
	}
	return b.String()
}
