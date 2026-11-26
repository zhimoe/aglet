package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"

	"github.com/zhimoe/aglet/internal/proc"
)

const (
	jobOutputLimit = 1024 * 1024
	defaultJobRead = 64 * 1024
	maxJobRead     = 256 * 1024
	maxJobs        = 32
)

// Job status values.
const (
	JobRunning  = "running"
	JobExited   = "exited"
	JobKilled   = "killed"
	JobFailed   = "failed"
	JobTimedOut = "timed_out"
)

// JobSnapshot is a point-in-time view of a background job.
type JobSnapshot struct {
	ID        string `json:"job_id"`
	Command   string `json:"command"`
	Status    string `json:"status"`
	ExitCode  int    `json:"exit_code"`
	Output    string `json:"output,omitempty"`
	Truncated bool   `json:"truncated,omitzero"`
	More      bool   `json:"more,omitzero"`
}

// Job is one background shell command.
type Job struct {
	ID      string
	Command string
	Cwd     string
	Started time.Time

	mu       sync.Mutex
	data     []byte
	total    int64
	readAt   int64
	status   string
	exitCode int
	timedOut bool
	killed   bool
	pid      int
	runCtx   context.Context
	done     chan struct{}
	doneOnce sync.Once
	cancel   context.CancelFunc
}

// JobManager owns background jobs. It is safe for concurrent use.
type JobManager struct {
	mu     sync.Mutex
	jobs   map[string]*Job
	order  []string
	nextID int
	ctx    context.Context
	cancel context.CancelFunc
	closed bool
}

// NewJobManager returns a manager whose jobs are killed on Close.
func NewJobManager() *JobManager {
	ctx, cancel := context.WithCancel(context.Background())
	return &JobManager{jobs: map[string]*Job{}, ctx: ctx, cancel: cancel}
}

// Start launches a background command. A zero timeout means no timeout.
func (m *JobManager) Start(cwd, command string, timeout time.Duration) (JobSnapshot, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return JobSnapshot{}, fmt.Errorf("background jobs are closed")
	}
	m.pruneLocked()
	if len(m.jobs) >= maxJobs {
		m.mu.Unlock()
		return JobSnapshot{}, fmt.Errorf("too many background jobs (max %d); kill one with kill_shell", maxJobs)
	}
	m.nextID++
	id := fmt.Sprintf("bg-%d", m.nextID)
	runCtx := m.ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		runCtx, cancel = context.WithTimeout(runCtx, timeout)
	}
	cmd := proc.Command(runCtx, command)
	cmd.Dir = cwd
	cmd.Env = os.Environ()
	job := &Job{
		ID: id, Command: command, Cwd: cwd, Started: time.Now(),
		status: JobRunning, runCtx: runCtx, done: make(chan struct{}), cancel: cancel,
	}
	if job.cancel == nil {
		job.cancel = func() {}
	}
	cmd.Stdout = job
	cmd.Stderr = job
	proc.Configure(cmd)
	if err := cmd.Start(); err != nil {
		job.cancel()
		m.mu.Unlock()
		return JobSnapshot{}, err
	}
	job.pid = cmd.Process.Pid
	m.jobs[id] = job
	m.order = append(m.order, id)
	m.mu.Unlock()

	go func() {
		err := cmd.Wait()
		job.finish(err)
	}()
	if timeout > 0 {
		go func() {
			<-runCtx.Done()
			if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
				job.markTimedOut()
				proc.KillGroup(job.pid)
			}
		}()
	}
	return job.snapshot("", false, false), nil
}

// Get returns a job by ID.
func (m *JobManager) Get(id string) (*Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[id]
	return job, ok
}

// Read returns the job's unread output, optionally waiting for it to exit.
func (m *JobManager) Read(id string, wait time.Duration, maxBytes int) (JobSnapshot, error) {
	job, ok := m.Get(id)
	if !ok {
		return JobSnapshot{}, fmt.Errorf("unknown job %q", id)
	}
	if wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-job.done:
		case <-timer.C:
		}
	}
	if maxBytes <= 0 {
		maxBytes = defaultJobRead
	}
	if maxBytes > maxJobRead {
		maxBytes = maxJobRead
	}
	output, truncated, more, status, exitCode := job.read(maxBytes)
	return JobSnapshot{
		ID: id, Command: job.Command, Status: status, ExitCode: exitCode,
		Output: output, Truncated: truncated, More: more,
	}, nil
}

// Kill stops a job's process group and waits briefly for it to exit.
func (m *JobManager) Kill(id string) (JobSnapshot, error) {
	job, ok := m.Get(id)
	if !ok {
		return JobSnapshot{}, fmt.Errorf("unknown job %q", id)
	}
	job.kill()
	select {
	case <-job.done:
	case <-time.After(2 * time.Second):
	}
	return job.snapshot("", false, false), nil
}

// Close kills every running job. It is safe to call more than once.
func (m *JobManager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	jobs := make([]*Job, 0, len(m.jobs))
	for _, id := range m.order {
		if job, ok := m.jobs[id]; ok {
			jobs = append(jobs, job)
		}
	}
	m.mu.Unlock()
	m.cancel()
	for _, job := range jobs {
		job.kill()
	}
	for _, job := range jobs {
		select {
		case <-job.done:
		case <-time.After(2 * time.Second):
		}
	}
	return nil
}

func (m *JobManager) pruneLocked() {
	for len(m.jobs) >= maxJobs {
		removed := false
		for i, id := range m.order {
			job := m.jobs[id]
			if job == nil || job.isDone() {
				delete(m.jobs, id)
				m.order = append(m.order[:i], m.order[i+1:]...)
				removed = true
				break
			}
		}
		if !removed {
			return
		}
	}
}

// Write implements io.Writer for the job's combined output.
func (j *Job) Write(p []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	original := len(p)
	if original == 0 {
		return 0, nil
	}
	if original > jobOutputLimit {
		p = p[original-jobOutputLimit:]
	}
	overflow := len(j.data) + len(p) - jobOutputLimit
	if overflow > 0 {
		copy(j.data, j.data[overflow:])
		j.data = j.data[:len(j.data)-overflow]
	}
	j.data = append(j.data, p...)
	j.total += int64(original)
	return original, nil
}

func (j *Job) read(max int) (output string, truncated, more bool, status string, exitCode int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	base := j.total - int64(len(j.data))
	if j.readAt < base {
		truncated = true
		j.readAt = base
	}
	start := j.readAt - base
	if start < 0 {
		start = 0
	}
	if start > int64(len(j.data)) {
		start = int64(len(j.data))
	}
	end := min(int64(len(j.data)), start+int64(max))
	output = string(j.data[start:end])
	j.readAt = base + end
	more = j.readAt < j.total
	return output, truncated, more, j.status, j.exitCode
}

func (j *Job) finish(err error) {
	j.mu.Lock()
	exitCode := 0
	status := JobExited
	if err != nil {
		var exitErr *exec.ExitError
		switch {
		case errors.As(err, &exitErr):
			exitCode = exitErr.ExitCode()
			status = JobFailed
		default:
			status = JobFailed
		}
	}
	switch {
	case j.timedOut || (j.runCtx != nil && errors.Is(j.runCtx.Err(), context.DeadlineExceeded)):
		status = JobTimedOut
	case j.killed:
		status = JobKilled
	}
	j.status = status
	j.exitCode = exitCode
	j.mu.Unlock()
	j.doneOnce.Do(func() { close(j.done) })
	j.cancel()
}

func (j *Job) markTimedOut() {
	j.mu.Lock()
	j.timedOut = true
	j.mu.Unlock()
}

func (j *Job) kill() {
	j.mu.Lock()
	if j.status != JobRunning {
		j.mu.Unlock()
		return
	}
	j.killed = true
	pid := j.pid
	j.mu.Unlock()
	j.cancel()
	if pid > 0 {
		proc.KillGroup(pid)
	}
}

func (j *Job) isDone() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.status != JobRunning
}

func (j *Job) snapshot(output string, truncated, more bool) JobSnapshot {
	j.mu.Lock()
	defer j.mu.Unlock()
	return JobSnapshot{
		ID: j.ID, Command: j.Command, Status: j.status, ExitCode: j.exitCode,
		Output: output, Truncated: truncated, More: more,
	}
}

type bashOutputArgs struct {
	JobID    string `json:"job_id" jsonschema:"Background job id returned by run_command."`
	WaitMS   int    `json:"wait_ms,omitempty" jsonschema:"Wait up to this many milliseconds for the job to exit. Default 0, max 30000."`
	MaxBytes int    `json:"max_bytes,omitempty" jsonschema:"Maximum bytes of new output. Default 65536, max 262144."`
}

type bashOutputResult struct {
	JobID     string `json:"job_id"`
	Status    string `json:"status"`
	ExitCode  int    `json:"exit_code"`
	Output    string `json:"output"`
	Truncated bool   `json:"truncated,omitzero"`
	More      bool   `json:"more,omitzero"`
}

type killShellArgs struct {
	JobID string `json:"job_id" jsonschema:"Background job id to kill."`
}

type killShellResult struct {
	JobID    string `json:"job_id"`
	Status   string `json:"status"`
	ExitCode int    `json:"exit_code"`
}

func newBashOutputTool(deps Deps) (tool.Tool, error) {
	handler := func(ctx adkagent.Context, args bashOutputArgs) (bashOutputResult, error) {
		if args.JobID == "" {
			return bashOutputResult{}, fmt.Errorf("job_id is required")
		}
		if err := authorize(ctx, deps, "bash_output", "Read output of "+args.JobID, map[string]any{"job_id": args.JobID}); err != nil {
			return bashOutputResult{}, err
		}
		if deps.Jobs == nil {
			return bashOutputResult{}, fmt.Errorf("background jobs are not available")
		}
		wait := time.Duration(args.WaitMS) * time.Millisecond
		if wait < 0 {
			wait = 0
		}
		if wait > 30*time.Second {
			wait = 30 * time.Second
		}
		snap, err := deps.Jobs.Read(args.JobID, wait, args.MaxBytes)
		if err != nil {
			return bashOutputResult{}, err
		}
		return bashOutputResult{
			JobID: snap.ID, Status: snap.Status, ExitCode: snap.ExitCode,
			Output: snap.Output, Truncated: snap.Truncated, More: snap.More,
		}, nil
	}
	return functiontool.New(functiontool.Config{
		Name:        "bash_output",
		Description: "Return new output from a background command started with run_command(run_in_background=true). Optionally waits for exit.",
	}, handler)
}

func newKillShellTool(deps Deps) (tool.Tool, error) {
	handler := func(ctx adkagent.Context, args killShellArgs) (killShellResult, error) {
		if args.JobID == "" {
			return killShellResult{}, fmt.Errorf("job_id is required")
		}
		if err := authorize(ctx, deps, "kill_shell", "Kill "+args.JobID, map[string]any{"job_id": args.JobID}); err != nil {
			return killShellResult{}, err
		}
		if deps.Jobs == nil {
			return killShellResult{}, fmt.Errorf("background jobs are not available")
		}
		snap, err := deps.Jobs.Kill(args.JobID)
		if err != nil {
			return killShellResult{}, err
		}
		return killShellResult{JobID: snap.ID, Status: snap.Status, ExitCode: snap.ExitCode}, nil
	}
	return functiontool.New(functiontool.Config{
		Name:        "kill_shell",
		Description: "Kill a background command and its process group.",
	}, handler)
}
