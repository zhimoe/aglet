# aglet

A small terminal coding agent built with **Google ADK for Go v2** and **Bubble Tea v2**.

`aglet` is the little tip at the end of a shoelace: a lightweight, focused agent you can run from your terminal, configure with plain JSON, and extend with MCP tools.

## Features

- **Headless and interactive**: stream one-shot answers, or use a TUI with Markdown rendering, a plan panel, and session switching.
- **OpenAI-compatible providers**: OpenAI, DeepSeek, and any service that implements `/chat/completions`.
- **Real tools**: read, write, edit, list, glob, grep, shell, and git.
- **Approval modes**: `ask`, `read-only`, `auto-edit`, and `full-auto`, plus per-tool allow/deny rules and session grants.
- **Diff-aware approvals**: writes and edits show a unified diff before you approve them.
- **Project context**: discovers `AGLET.md`, `AGENTS.md`, `AGENTS.override.md`, and `CLAUDE.md` from the workspace and its ancestors.
- **Skills**: `SKILL.md` files with YAML frontmatter, loaded on demand with `load_skill`.
- **Subagents**: a `task` tool delegates self-contained work to built-in `general`/`explore` agents or `.aglet/agents/*.md` definitions, with isolated context and optional model overrides.
- **Hooks**: Claude-compatible `PreToolUse`, `PostToolUse`, `UserPromptSubmit`, `SessionStart`, `SessionEnd`, and `Stop` shell hooks.
- **Custom commands**: Markdown prompt templates in `.aglet/commands`, invoked as `/name args` with `$ARGUMENTS`/`$1..$9`.
- **Web tools**: `web_fetch` with an SSRF guard and HTML-to-text conversion, plus `web_search` for Tavily, Brave, and SearXNG.
- **Background jobs**: `run_command` with `run_in_background`, then poll with `bash_output` and stop with `kill_shell`.
- **Plan / todo panel**: the model updates a checklist with `update_plan`; the TUI renders it and tracks progress.
- **Git workflow**: `git_status`, `git_diff`, `git_log`, `git_commit`, plus `/diff`, `/review`, and `aglet review`.
- **Persistent sessions**: SQLite-backed sessions, list/resume/continue, history replay in the TUI.
- **MCP client**: stdio, SSE, and streamable HTTP servers; tools are discovered and exposed to the agent.
- **Event bridge**: ADK events normalized into text, thought, tool call, tool result, plan, usage, error, and done events.
- **JSONL output**: machine-readable events for scripts and tests.
- **No magic config**: one JSON file, no database server, no cloud dependency.

## Requirements

- Go **1.26.10+** (developed with Go 1.27.4)
- macOS, Linux, or Windows
  - Unix shell tool uses `sh -c`; Windows uses `cmd.exe /C`.
  - Unix cancellation kills the process group; Windows uses `taskkill /T /F`.
- An API key for an OpenAI-compatible provider

## Build

```bash
git clone https://github.com/zhimoe/aglet.git
cd aglet
go build -mod=vendor -o aglet ./cmd/aglet
```

The repository vendors all dependencies, so the build works offline. If you
change `go.mod`, refresh the vendor tree with `go mod tidy && go mod vendor`.

`aglet` is pure Go and does not require cgo: SQLite uses the pure-Go
`glebarez/sqlite` + `modernc.org/sqlite` stack. You can force a no-cgo build
(or cross-compile a static Linux binary):

```bash
CGO_ENABLED=0 go build -mod=vendor -o aglet ./cmd/aglet
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -mod=vendor -o aglet-linux ./cmd/aglet
```

### Windows

From PowerShell:

```powershell
.\scripts\build-windows.ps1              # dist\aglet-windows-amd64.exe
.\scripts\build-windows.ps1 -Arch arm64  # dist\aglet-windows-arm64.exe
.\scripts\build-windows.ps1 -Strip       # smaller binary (-s -w)
```

From `cmd.exe`:

```bat
scripts\build-windows.cmd
```

Or cross-compile from macOS/Linux:

```bash
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -mod=vendor -trimpath -o dist/aglet-windows-amd64.exe ./cmd/aglet
```

The Windows build is pure Go (`CGO_ENABLED=0`). `run_command` uses `cmd.exe`
syntax on Windows, not POSIX shell syntax.

## Quick start

```bash
./aglet config init --path ./aglet.json
```

Edit `aglet.json` and set the provider/model you want, then export the key:

```bash
export DEEPSEEK_API_KEY=sk-...
# or
export OPENAI_API_KEY=sk-...
```

### Headless

> Tested against DeepSeek `deepseek-flash` (including a tool call round trip)
> and `deepseek-v4-pro` (basic chat) through the OpenAI-compatible Chat
> Completions endpoint. Model names change over time; set what your provider
> actually exposes.

```bash
./aglet run --profile deepseek "list the files in this repo and explain go.mod"

# Agent CLI habits: exec is an alias for run
./aglet exec --profile deepseek "summarize the architecture"

# stdin works too
printf 'summarize the architecture\n' | ./aglet run --profile deepseek

# JSONL events
./aglet run --json --profile deepseek "run go test ./..."

# Continue the most recent session
./aglet run --continue "and what about the tests?"

# Auto-approve edits but still ask for shell commands
./aglet run --auto-edit "fix the failing test"
```

Assistant text goes to stdout. Tool calls, tool results, plan updates, usage,
and errors go to stderr, so stdout stays clean for piping. `--json` writes one
JSON object per line to stdout.

### TUI

```bash
./aglet tui --profile deepseek
./aglet resume                 # open the most recent session
./aglet resume <session-id>
```

- `Enter` sends; `Ctrl+J` inserts a newline.
- `Esc` cancels a running turn (or clears the input when idle).
- `Ctrl+C` cancels a running turn; when idle it exits.
- Completed assistant messages are rendered as Markdown; tool output stays monospace.
- The plan panel shows `update_plan` progress; the status bar shows the model, session, approval mode, token usage, and plan counts.
- Tool approvals appear as a dialog with a diff for file changes: `y` allow once, `a` always this session, `n`/`Esc` deny.

Shell escape:

```text
!git status
!go test ./...
```

`!` commands run outside the model but still go through the approval policy.
They are rejected in `read-only` mode.

Slash commands:

```text
/help  /clear  /quit
/sessions  /resume <id>  /new
/skills  /agents  /commands  /status  /permissions  /mode <name>
/plan [task]  /mcp  /diff  /review  /model
```

`/name args` runs a custom command from `.aglet/commands`.

## CLI

```text
aglet run|exec [flags] [prompt]  one-shot / headless turn ("-" or pipe for stdin)
aglet tui [flags]                interactive TUI
aglet resume [ID] --last         open the TUI for a previous session
aglet sessions list              list stored sessions (no model key needed)
aglet review [--base REF]        review a git diff with a read-only policy
aglet config init [--path P]     write an example config
aglet config show                print resolved config (secrets redacted)
aglet commands list              list custom slash commands
aglet agents list                list subagent definitions
aglet mcp list                   connect MCP servers and list tools
aglet version
```

Common flags:

```text
--config PATH      config file (default: ./aglet.json or ~/.config/aglet/config.json)
--profile NAME     profile from config (default: config.default_profile)
--model NAME       override the profile model
--workspace PATH   override workspace
--session ID       resume/create a session
--continue         continue the most recent session
--mode NAME        ask | read-only | auto-edit | full-auto
--show-thoughts    display thought events when the provider emits them
```

Aliases: `--read-only`, `--auto-edit`, `--full-auto`, `--plan`, `--yolo`.

Run flags:

```text
--json             emit JSONL events instead of text
--yes              auto-approve tools that require approval
--deny             deny all tools that require approval
--max-llm-calls N  cap model calls in this run
```

`aglet sessions list` prints newest first:

```text
ID                                    UPDATED           EVENTS  PREVIEW
6f0f1e3c-...                          2026-11-25 21:36  12      fix the failing test
```

## Configuration

`aglet` looks for the config in this order:

1. `--config PATH`
2. `$AGLET_CONFIG`
3. `./aglet.json`
4. `~/.config/aglet/config.json`

A config with the Phase 2 fields:

```json
{
  "default_profile": "deepseek",
  "approval_mode": "ask",
  "profiles": {
    "deepseek": {
      "api": "chat_completions",
      "base_url": "https://api.deepseek.com",
      "api_key_env": "DEEPSEEK_API_KEY",
      "model": "deepseek-flash"
    },
    "openai": {
      "api": "chat_completions",
      "base_url": "https://api.openai.com/v1",
      "api_key_env": "OPENAI_API_KEY",
      "model": "gpt-4.1"
    }
  },
  "agent": {
    "max_llm_calls": 50,
    "temperature": 0.2,
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
  "session_db": "~/.local/share/aglet/sessions.db",
  "ui": {
    "show_thoughts": true
  }
}
```

- API keys are read only from environment variables. The config stores the env var name, never the key.
- `api` is `chat_completions` by default. DeepSeek and most third-party OpenAI-compatible services require this; `responses` is available for providers that support the OpenAI Responses API.
- `workspace` confines all file tools. Paths are checked lexically and through symlinks.
- `context.max_skill_bytes` caps the size of skills, custom commands, and subagent definition files.
- `config show` prints the resolved config with MCP env/header values redacted.

### Approval modes

`approval_mode` (or `--mode`) controls the default policy. Explicit `deny`
rules always win.

| Mode | Behavior |
|---|---|
| `ask` (alias `default`) | Read tools auto-approve; writes, edits, commands, git commits, and MCP tools ask. |
| `read-only` (alias `plan`) | Only read-only tools are allowed; writes, edits, shell, and MCP are rejected. |
| `auto-edit` (alias `accept-edits`) | Reads plus `write_file`/`edit_file` are auto-approved; shell, commits, and MCP still ask. |
| `full-auto` (alias `auto`, `yolo`) | Everything is auto-approved except explicit `deny` rules. |

Read-only tools are `read_file`, `list_dir`, `glob`, `grep`, `git_status`,
`git_diff`, `git_log`, `load_skill`, `update_plan`, `task`, `web_fetch`,
`web_search`, `bash_output`, and `kill_shell`. Subagent tools still run through
the same policy, so a write inside a subagent is denied or approved normally.

Within `ask`/`auto-edit`, precedence is: explicit `deny` > mode restrictions >
`permissions.require_approval` > `permissions.auto_approve` > default ask.
File writes and edits include a unified diff in the approval prompt.

### Context files

Instruction files are discovered per directory while walking from the
filesystem root down to the workspace (like pi and Codex). Each directory
contributes the first matching file from `instruction_files`. Files are
size-capped and treated as untrusted context, not as executable instructions.

Set `"instruction_files": []` to disable discovery. The content is injected
into the system prompt inside `<project_instructions>` sections.

### Skills

Skills live in `<dir>/<name>/SKILL.md`. Default discovery directories:

- `<workspace>/.agents/skills`
- `<workspace>/.aglet/skills`
- `~/.agents/skills`
- `~/.config/aglet/skills`

Additional directories can be listed in `context.skills_dirs`. Example:

```markdown
---
name: git-review
description: Review a diff for correctness and risk
disable-model-invocation: false
---

# Git review

1. Run `git_diff`.
2. Look for correctness bugs, regressions, and missing tests.
```

The system prompt only contains a name/description/location index. The model
calls `load_skill` to read the full body when needed. Set
`disable-model-invocation: true` to keep a skill out of the model's index.
Duplicate names keep the first skill discovered; malformed skills produce
warnings without blocking startup. Use `/skills` in the TUI to list them.

### Subagents

The model delegates through a single `task` tool with `subagent_type`,
`description`, and a self-contained `prompt`. Two subagents are built in:

- `general`: every local and MCP tool except `task` (no recursion).
- `explore`: read-only file/search/git/skill tools plus `web_fetch` and
  `web_search` when enabled.

Custom definitions live in `.aglet/agents`, `.agents/agents`, or
`~/.config/aglet/agents` (plus `context.agents_dirs`). Files are Markdown with
YAML frontmatter:

```markdown
---
name: reviewer
description: Review a diff for correctness and risk
tools: read_file, grep, git_diff
model: deepseek
---
You are a strict code reviewer. Report findings with file paths.
```

`tools` accepts a YAML list or a comma-separated string and supports `mcp_*`
globs; Claude Code names are normalized (`Read` → `read_file`, `Bash` →
`run_command`, `TodoWrite` → `update_plan`). An empty `tools` means all local
and MCP tools. `model` may be a profile name, another model ID on the current
profile, or `inherit` (the default).
Custom definitions override built-ins with the same name; `aglet agents list`
shows the result.

Each invocation gets a fresh in-memory session and a fresh private plan board,
so subagent tool chatter and plans do not pollute the parent. Tools, approvals,
hooks, web access, and background jobs are shared with the parent; approval
prompts show the requesting agent. Each child run has its own
`agent.max_llm_calls` budget, reported back as a usage event. Subagent sessions
are ephemeral and cannot be resumed after the task returns.

### Hooks

Hooks are shell commands configured in `aglet.json`. Events use Claude Code
names (case-insensitive):

```json
"hooks": {
  "SessionStart": [
    {"matcher": "^(startup|resume)$", "hooks": [
      {"type": "command", "command": "bash ~/.aglet/hooks/session-state.sh", "timeout": 10}
    ]}
  ],
  "PreToolUse": [
    {"matcher": "run_command|write_file", "hooks": [
      {"type": "command", "command": "~/.aglet/hooks/guard.sh", "timeout": 10}
    ]}
  ]
}
```

Events: `UserPromptSubmit`, `PreToolUse`, `PostToolUse`, `SessionStart`,
`SessionEnd`, `Stop`. Hooks receive a JSON object on stdin
(`hook_event_name`, `session_id`, `cwd`, `permission_mode`, `tool_name`,
`tool_input`, `tool_response`, `prompt`, `source`, ...) and `AGLET_*`
environment variables. Exit code 2 blocks the action and uses stderr as the
reason. Exit 0 may print JSON with `decision: "block"`, `continue: false`,
`systemMessage`, or `hookSpecificOutput.additionalContext` /
`permissionDecision: "deny"`. Other failures and timeouts are non-blocking
notices.

Semantics to keep in mind:

- `permissionDecision: "allow"` does **not** bypass aglet's approval policy;
  `deny` and `block` are enforced.
- `PostToolUse` can add `hook_context`/`hook_feedback` to a successful result
  but never rewrites an error result.
- `Stop` hooks are observational in this version; a block becomes a notice and
  cannot force another model turn.
- `SessionStart`/`SessionEnd` fire on process start/exit, not on `/new` or
  `/resume` session switches.
- The TUI `!command` escape runs `PreToolUse`/`PostToolUse` exactly once;
  `run_command` goes through the same hooks via the ADK plugin.
- Hooks run with your user privileges. They are part of the trusted local
  config, not a sandbox.

Hook stdout/stderr are capped at 64 KiB, and the stdin payload is truncated for
large tool arguments.

### Custom commands

Markdown files under `.aglet/commands`, `.agents/commands`, or
`~/.config/aglet/commands` become slash commands. Subdirectories use `:`
namespacing (`git/review.md` → `/git:review`):

```markdown
---
description: Review a diff against a base revision
argument-hint: "[base]"
---
Review the changes against $1. Focus on correctness and missing tests.
Use $ARGUMENTS when you need the full argument string.
```

`$ARGUMENTS` expands to everything after the command name; `$1`..`$9` are
positional arguments. If the body has no placeholder, arguments are appended.
`\$` is a literal dollar sign. Built-in slash commands win over custom names.

Run a command in the TUI with `/name args`, or headlessly with
`aglet run "/name args"`. `/commands` and `aglet commands list` show what was
discovered. The `allowed-tools` and `model` frontmatter fields are parsed but
not enforced in this version. Claude-style `!` (bash expansion) and `@` (file
reference) syntax is not executed; the body is sent as a prompt after argument
substitution.

### Web tools

`web_fetch` retrieves HTTP(S) URLs, blocks loopback/private/link-local targets
by default, honors an optional host allowlist, follows at most five redirects,
and converts HTML to readable text. `web_search` supports Tavily, Brave, and
self-hosted SearXNG:

```json
"web": {
  "fetch": {"enabled": true, "max_bytes": 1048576, "timeout_sec": 30,
            "allow_private_networks": false, "allowed_hosts": []},
  "search": {"enabled": true, "provider": "tavily", "base_url": "",
             "api_key_env": "TAVILY_API_KEY", "max_results": 5, "timeout_sec": 20}
}
```

API keys are read from the named environment variable; the config never stores
the key. Search requests use the configured base URL as trusted administrator
configuration (so a local SearXNG works), while `web_fetch` applies the SSRF
guard. Both tools are read-only and auto-approved in the default config; remove
them from `permissions.auto_approve` if arbitrary network egress is not
acceptable. Fetched content is untrusted and may contain prompt injection.

### Background jobs

Set `run_in_background: true` on `run_command` to start a long-running process.
The tool returns a `job_id` immediately. Poll with
`bash_output {job_id, wait_ms?}` (new output only, up to 64 KiB per call,
cursor-based) and stop it with `kill_shell {job_id}`. Jobs survive across turns,
are limited to 32, and are killed when aglet exits. Starting a job still needs
`run_command` approval; reading/killing is read-only. Job output buffers hold
1 MiB and drop the oldest bytes when full.

### Context compaction

Compaction is disabled by default. To enable ADK's session compaction:

```json
"agent": {
  "compaction": {
    "token_threshold": 60000,
    "event_retention_size": 20,
    "compaction_interval": 0,
    "overlap_size": 0
  }
}
```

`token_threshold` must be paired with `event_retention_size > 0`. When the
threshold is crossed, older events are summarized and the most recent
`event_retention_size` events are retained.

### Plan and todo

The model is instructed to call `update_plan` for multi-step work with steps
such as `pending`, `in_progress`, and `completed`. The TUI replaces the plan
panel on each update and shows `plan 2/5` in the status bar. Headless text mode
prints `[plan]` to stderr; JSON mode emits a `plan` event.

`/plan` switches the TUI to read-only planning mode and asks the model to
produce a plan without making changes.

## Git workflow

| Tool | Purpose | Default approval |
|---|---|---|
| `git_status` | Working tree status | auto |
| `git_diff` | Unstaged or staged diff | auto |
| `git_log` | Recent one-line history | auto |
| `git_commit` | Commit (optionally `git add -A`) | ask |

`aglet review` collects the working-tree diff (or `--base REF`) and sends it to
a read-only agent for review. In the TUI, `/diff` appends the current diff and
`/review` starts a review turn using the `git_diff` tool.

## Sessions

Sessions are stored in SQLite via ADK's database session service.

- `aglet sessions list` lists stored sessions without needing a model key.
- `aglet resume [ID]` opens the TUI for a session; with no ID it opens the latest.
- `aglet run --session ID` continues a session headlessly.
- `aglet run --continue` (or `tui --continue`) selects the latest session.
- In the TUI: `/sessions`, `/resume <id>`, and `/new`.

Session switching is only allowed while idle.

## MCP

Add MCP servers under `mcp_servers`. Example stdio server:

```json
{
  "mcp_servers": {
    "everything": {
      "transport": "stdio",
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-everything"],
      "enabled": true,
      "require_confirmation": true,
      "connect_timeout_sec": 10
    }
  }
}
```

Remote servers:

```json
{
  "mcp_servers": {
    "remote": {
      "transport": "http",
      "url": "https://example.com/mcp",
      "headers": { "Authorization": "Bearer ${MY_TOKEN}" },
      "enabled": true
    },
    "legacy": {
      "transport": "sse",
      "url": "https://example.com/sse",
      "enabled": false
    }
  }
}
```

Inspect discovered tools without starting the agent:

```bash
./aglet mcp list
```

MCP tools are exposed as `mcp_<server>_<tool>` (sanitized, max 64 characters).
They are sensitive by default; use `permissions.auto_approve` for trusted
read-only tools.

## Security model

- File tools are restricted to the workspace, including symlink escape checks.
- Writes, edits, commands, git commits, and MCP tools require confirmation by default.
- Approval modes can loosen or tighten this, but explicit `deny` rules always win.
- In headless mode, non-TTY stdin denies sensitive tools unless `--yes` is passed. Use `--deny` to force rejection.
- `run_command` has a default 120s timeout (max 600s), truncates output, and kills the process group on cancellation. On Windows it uses `cmd.exe /C` and `taskkill /T /F`.
- Project instruction files, skills, custom command bodies, and fetched web content are untrusted context; the prompt tells the model not to follow instructions from them that conflict with the user or policy.
- Hooks execute arbitrary local shell commands configured by the user. They can observe tool arguments/results and block calls; they are not sandboxed. Review hook scripts before enabling them.
- `web_fetch` blocks private/link-local addresses and validates redirects, but it is still network egress. `web_search` uses the configured provider base URL. Remove the web tools from `permissions.auto_approve` in sensitive environments.
- Subagents inherit the approval policy and hooks. Writes, shell commands, and MCP calls inside a subagent still prompt (and show the requesting agent). Subagent sessions are ephemeral.
- No OS-level sandbox is provided. Run `aglet` in a dedicated directory or container when working with untrusted code, and avoid `--yes` there.

## Architecture

```
cmd/aglet            CLI entrypoint
internal/cli         run / tui / resume / sessions / review / config / mcp
internal/agent       ADK runtime, event bridge, prompt, compaction, hooks bridge,
                     subagent executor, task tool, emitter hub
internal/tools       local file, search, shell, git, skill, plan, web, and background tools
internal/mcp         custom MCP manager -> ADK function tools
internal/permissions approval modes, allow/ask/deny policy, session grants
internal/instructions AGLET.md / AGENTS.md discovery and injection
internal/skills      SKILL.md discovery, frontmatter, index rendering
internal/subagents   subagent definition discovery and built-ins
internal/commands    custom slash command discovery and expansion
internal/hooks       hook execution, matching, and JSON protocol
internal/web         SSRF-guarded fetch, HTML-to-text, search providers
internal/plan        thread-safe plan board and status validation
internal/diff        unified diffs for approvals
internal/session     ADK database session service (SQLite), list/preview
internal/headless    stream output, JSONL, stdin approvals
internal/tui         Bubble Tea v2 model, Markdown rendering, plan panel
internal/proc        platform shell/process-group helpers
internal/frontmatter Markdown YAML frontmatter splitting
internal/config      JSON config loading and validation
```

The runtime owns ADK's `llmagent`, `runner`, session service, plan board, and
compaction configuration. Tools are wrapped with an approval policy. MCP tools
are connected with the official Go SDK, discovered at startup, and wrapped as
ADK function tools so they use the same approval flow as local tools.

Events flow in one direction:

```
ADK session.Event
    -> agent.Mapper (partial/final text dedupe, tool calls/results, usage)
    -> plan board update_plan event
    -> headless writer or TUI event channel
```

## Development

```bash
go fmt ./...
go vet ./...
go test ./...
go test -race ./internal/...
go mod tidy && go mod vendor
go build -mod=vendor -o aglet ./cmd/aglet
```

Do not run `gofmt -w .` at the repository root: it would rewrite vendored
upstream files. Use `go fmt ./...`, which only formats this module.

Tests cover event mapping, a fake-model runtime + tool loop, plan events and
session switching, config validation/redaction/compaction, permission modes,
diff and skill discovery, instructions, git helpers, file/search tools,
headless approvals, in-memory MCP discovery, hooks (blocking, JSON output,
matchers, payloads), subagent discovery and a fake-model `task` round trip,
custom command discovery/expansion, SSRF-guarded web fetch/HTML/search, and
background job lifecycle/cursors.

## Current limitations

- TUI Markdown rendering applies to completed assistant messages; streaming text is plain.
- No syntax highlighting, ACP server, or LSP integration yet.
- No session tree/fork/clone, steering queue, or runtime `/model` switching.
- MCP resources/prompts and runtime reload are not implemented; only tools are discovered.
- Stop hooks are observational and cannot force another model turn; hook `allow`/`ask` decisions do not override aglet's policy.
- Custom command `allowed-tools` and `model` frontmatter are parsed but not enforced.
- Subagent/command/hook definitions are loaded at startup; restart aglet after editing them.
- Subagent sessions are ephemeral (no resume), and background jobs live only for the process lifetime (not persisted across restarts).
- No OS sandbox; approval and workspace checks are the current boundary.
- The Windows target is cross-compiled and compile-checked here; it has not been run on a real Windows machine in this repo's test setup. Please run `go test ./...` and a quick `aglet run` on Windows before relying on it.

These are deliberate next steps rather than hidden behavior. The skeleton is
meant to be small enough to read and extend.
