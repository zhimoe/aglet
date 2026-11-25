package agent

import (
	"fmt"
	"os"

	"github.com/zhimoe/aglet/internal/config"
)

// BuildSystemPrompt returns the configured prompt or a sensible default.
func BuildSystemPrompt(cfg *config.Config, workspace string) (string, error) {
	if cfg.Agent.SystemPromptFile != "" {
		p := config.ExpandPath(cfg.Agent.SystemPromptFile)
		b, err := os.ReadFile(p)
		if err != nil {
			return "", fmt.Errorf("read system_prompt_file %s: %w", p, err)
		}
		return string(b), nil
	}
	return fmt.Sprintf(`You are aglet, a terminal coding agent.

Workspace root: %s

Rules:
- Use the provided tools to inspect and modify files. Never invent file contents.
- Read relevant files before editing. Prefer small, exact edits with edit_file.
- Use run_command for tests, builds, and shell inspection. Commands may require user approval.
- Tool calls that write files, run commands, or call MCP tools may be paused for human approval by the host.
- Do not ask the user to type approval in chat; the host handles approvals.
- Keep answers concise and focused. Report what you changed and how you verified it.
- If a tool returns an error, read it carefully and adjust. Do not retry identical failing calls.
- Do not expose secrets. Never print API keys or tokens.
`, workspace), nil
}
