# aglet

A small terminal coding agent built with **Google ADK for Go v2** and **Bubble Tea v2**.

`aglet` is the little tip at the end of a shoelace: a lightweight, focused agent you can run from your terminal, configure with plain JSON, and extend with MCP tools.

## Features

- **Headless and interactive**: stream one-shot answers, or use a minimal TUI.
- **OpenAI-compatible providers**: OpenAI, DeepSeek, and any service that implements `/chat/completions`.
- **Real tools**: read, write, edit, list, glob, grep, and run shell commands.
- **Human approval**: read-only tools run automatically; writes, edits, commands, and MCP tools ask first.
- **MCP client**: stdio, SSE, and streamable HTTP servers; tools are discovered and exposed to the agent.
- **Persistent sessions**: SQLite-backed sessions, resumable with `--session`, with history replay in the TUI.
- **Event bridge**: ADK events normalized into text, thought, tool call, tool result, usage, error, and done events.
- **JSONL output**: machine-readable events for scripts and tests.
- **No magic config**: one JSON file, no database server, no cloud dependency.

## Requirements

- Go **1.26.10+** (developed with Go 1.27.4)
- macOS or Linux (the shell tool uses `sh`; process-group cancellation is Unix-specific)
- An API key for an OpenAI-compatible provider

## Build

```bash
git clone https://github.com/zhimoe/aglet.git
cd aglet
go mod tidy
go build -o aglet ./cmd/aglet
```

`aglet` is pure Go and does not require cgo: SQLite uses the pure-Go
`glebarez/sqlite` + `modernc.org/sqlite` stack. You can force a no-cgo build
(or cross-compile a static Linux binary):

```bash
CGO_ENABLED=0 go build -o aglet ./cmd/aglet
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o aglet-linux ./cmd/aglet
```

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

```bash
./aglet run --profile deepseek "list the files in this repo and explain go.mod"

# stdin works too
printf 'summarize the architecture\n' | ./aglet run --profile deepseek

# JSONL events
./aglet run --json --profile deepseek "run go test ./..."
```

Assistant text goes to stdout. Tool calls, tool results, usage, and errors go to stderr, so stdout stays clean for piping. `--json` writes one JSON object per line to stdout.

### TUI

```bash
./aglet tui --profile deepseek
```

- `Enter` sends; `Ctrl+J` inserts a newline.
- `Esc` cancels a running turn (or clears the input when idle).
- `Ctrl+C` cancels a running turn; when idle it exits.
- `/help`, `/clear`, `/quit`.
- Tool approvals appear as a dialog: `y` allow once, `a` always this session, `n`/`Esc` deny.
- Use `--session ID` to resume a session. The transcript is replayed from the ADK session store.

## Configuration

`aglet` looks for the config in this order:

1. `--config PATH`
2. `$AGLET_CONFIG`
3. `./aglet.json`
4. `~/.config/aglet/config.json`

A minimal config:

```json
{
  "default_profile": "deepseek",
  "profiles": {
    "deepseek": {
      "api": "chat_completions",
      "base_url": "https://api.deepseek.com",
      "api_key_env": "DEEPSEEK_API_KEY",
      "model": "deepseek-chat"
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
    "temperature": 0.2
  },
  "workspace": ".",
  "permissions": {
    "auto_approve": ["read_file", "list_dir", "glob", "grep"],
    "require_approval": ["write_file", "edit_file", "run_command", "mcp_*"],
    "deny": []
  },
  "session_db": "~/.local/share/aglet/sessions.db"
}
```

- API keys are read only from environment variables. The config stores the env var name, never the key.
- `api` is `chat_completions` by default. DeepSeek and most third-party OpenAI-compatible services require this; `responses` is available for providers that support the OpenAI Responses API.
- `workspace` confines all file tools. Paths are checked lexically and through symlinks.
- `permissions` precedence: `deny` > `require_approval` > `auto_approve`. Unlisted tools require approval.
- `config show` prints the resolved config with MCP env/header values redacted.

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

MCP tools are exposed as `mcp_<server>_<tool>` (sanitized, max 64 characters). They are sensitive by default; use `permissions.auto_approve` for trusted read-only tools.

## Security model

- File tools are restricted to the workspace, including symlink escape checks.
- Writes, edits, commands, and MCP tools require confirmation by default.
- In headless mode, non-TTY stdin denies sensitive tools unless `--yes` is passed. Use `--deny` to force rejection.
- `run_command` has a default 120s timeout (max 600s), truncates output, and kills the process group on cancellation.
- No OS-level sandbox is provided. Run `aglet` in a dedicated directory or container when working with untrusted code, and avoid `--yes` there.

## Architecture

```
cmd/aglet            CLI entrypoint
internal/cli         run / tui / config / mcp / version
internal/agent       ADK runtime, event bridge, system prompt
internal/tools       local file, search, and shell tools
internal/mcp         custom MCP manager -> ADK function tools
internal/permissions allow/ask/deny policy and approval interface
internal/session     ADK database session service (SQLite)
internal/headless    stream output and stdin approvals
internal/tui         Bubble Tea v2 model and rendering
internal/config      JSON config loading and validation
```

The runtime owns ADK's `llmagent`, `runner`, and session service. Tools are wrapped with an approval policy. MCP tools are connected with the official Go SDK, discovered at startup, and wrapped as ADK function tools so they use the same approval flow as local tools.

Events flow in one direction:

```
ADK session.Event
    -> agent.Mapper (partial/final text dedupe, tool calls/results, usage)
    -> headless writer or TUI event channel
```

## Development

```bash
gofmt -w .
go vet ./...
go test ./...
go test -race ./internal/...
go build -o aglet ./cmd/aglet
```

Tests cover event mapping, a fake-model runtime + tool loop, config validation/redaction, permission precedence, file/search tools, headless approvals, and in-memory MCP discovery.

## Current limitations

- TUI rendering is plain text + lipgloss; Markdown, syntax highlighting, and diff previews are not implemented yet.
- No skills, hooks, subagents, ACP server, or LSP integration yet.
- MCP resources/prompts and runtime reload are not implemented; only tools are discovered.
- Sessions work across processes, but there is no session list UI yet.
- No OS sandbox; approval and workspace checks are the current boundary.

These are deliberate next steps rather than hidden behavior. The skeleton is meant to be small enough to read and extend.
