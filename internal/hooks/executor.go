package hooks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/zhimoe/aglet/internal/proc"
)

const maxHookOutput = 64 * 1024

type execExecutor struct{}

func (execExecutor) Run(ctx context.Context, req Request) ([]byte, []byte, int, error) {
	runCtx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()

	cmd := proc.Command(runCtx, req.Command)
	cmd.Dir = req.Workspace
	cmd.Env = append(os.Environ(), req.Env...)
	cmd.Stdin = bytes.NewReader(req.Stdin)
	stdout := &cappedBuffer{limit: maxHookOutput}
	stderr := &cappedBuffer{limit: maxHookOutput}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	proc.Configure(cmd)

	if err := cmd.Start(); err != nil {
		return nil, nil, 0, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case <-runCtx.Done():
		proc.KillGroup(cmd.Process.Pid)
		<-done
		return stdout.Bytes(), stderr.Bytes(), -1, fmt.Errorf("hook timed out after %s", req.Timeout)
	case err := <-done:
		exitCode := 0
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				exitCode = exitErr.ExitCode()
			} else {
				return stdout.Bytes(), stderr.Bytes(), 0, err
			}
		}
		return stdout.Bytes(), stderr.Bytes(), exitCode, nil
	}
}

type cappedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	remain := b.limit - b.buf.Len()
	if remain <= 0 {
		return len(p), nil
	}
	if len(p) > remain {
		_, _ = b.buf.Write(p[:remain])
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *cappedBuffer) Bytes() []byte { return b.buf.Bytes() }
