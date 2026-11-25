package tui

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/zhimoe/aglet/internal/agent"
	sessionstore "github.com/zhimoe/aglet/internal/session"
)

type sessionsMsg struct {
	infos []sessionstore.Info
	err   error
}

type resumedMsg struct {
	id      string
	history []agent.Event
	fresh   bool
	err     error
}

type diffMsg struct {
	text string
	err  error
}

type shellEvent struct {
	run      int
	command  string
	output   string
	exitCode int
	timedOut bool
	canceled bool
	err      error
}

type shellEventMsg struct{ event shellEvent }

func loadSessionsCmd(rt *agent.Runtime) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		infos, err := rt.ListSessions(ctx)
		return sessionsMsg{infos: infos, err: err}
	}
}

func resumeSessionCmd(rt *agent.Runtime, id string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		ensured, err := rt.SetSession(ctx, id)
		if err != nil {
			return resumedMsg{id: id, err: err}
		}
		history, err := rt.HistoryFor(ctx, ensured)
		return resumedMsg{id: ensured, history: history, err: err}
	}
}

func newSessionCmd(rt *agent.Runtime) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		id, err := rt.SetSession(ctx, "")
		return resumedMsg{id: id, fresh: true, err: err}
	}
}

func gitDiffCmd(workspace string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", "diff", "--no-color")
		cmd.Dir = workspace
		out, err := cmd.CombinedOutput()
		if err != nil {
			msg := strings.TrimSpace(string(out))
			if msg == "" {
				msg = err.Error()
			}
			return diffMsg{err: fmt.Errorf("git diff: %s", msg)}
		}
		text := string(out)
		if len(text) > 64*1024 {
			text = text[:64*1024] + "\n...[diff truncated]"
		}
		return diffMsg{text: text}
	}
}

func startShellRun(rt *agent.Runtime, command string, run int, ctx context.Context) <-chan shellEvent {
	ch := make(chan shellEvent, 1)
	go func() {
		res, err := rt.RunShell(ctx, command)
		ev := shellEvent{
			run: run, command: command, output: res.Stdout,
			exitCode: res.ExitCode, timedOut: res.TimedOut, err: err,
		}
		if ctx.Err() != nil {
			ev.canceled = true
		}
		ch <- ev
		close(ch)
	}()
	return ch
}

func waitShell(ch <-chan shellEvent, run int) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return shellEventMsg{event: shellEvent{run: run, canceled: true}}
		}
		return shellEventMsg{event: ev}
	}
}
