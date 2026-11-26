package tools

import (
	"runtime"
	"strings"
	"testing"
	"time"
)

func newTestJobs(t *testing.T) *JobManager {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	m := NewJobManager()
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func TestJobEchoAndCursor(t *testing.T) {
	m := newTestJobs(t)
	snap, err := m.Start(t.TempDir(), `printf a; sleep 0.2; printf b`, 0)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Status != JobRunning || snap.ID == "" {
		t.Fatalf("snap = %#v", snap)
	}
	var output strings.Builder
	snap, err = m.Read(snap.ID, 5*time.Second, 0)
	if err != nil {
		t.Fatal(err)
	}
	output.WriteString(snap.Output)
	if snap.Status != JobExited || snap.ExitCode != 0 {
		t.Fatalf("snap = %#v", snap)
	}
	snap, err = m.Read(snap.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	output.WriteString(snap.Output)
	if got := output.String(); got != "ab" {
		t.Fatalf("output = %q", got)
	}
}

func TestJobKill(t *testing.T) {
	m := newTestJobs(t)
	snap, err := m.Start(t.TempDir(), "sleep 10", 0)
	if err != nil {
		t.Fatal(err)
	}
	snap, err = m.Kill(snap.ID)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Status != JobKilled {
		t.Fatalf("status = %q", snap.Status)
	}
}

func TestJobTimeout(t *testing.T) {
	m := newTestJobs(t)
	snap, err := m.Start(t.TempDir(), "sleep 10", 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	snap, err = m.Read(snap.ID, 5*time.Second, 0)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Status != JobTimedOut {
		t.Fatalf("status = %q", snap.Status)
	}
}

func TestJobManagerCloseKills(t *testing.T) {
	m := newTestJobs(t)
	snap, err := m.Start(t.TempDir(), "sleep 10", 0)
	if err != nil {
		t.Fatal(err)
	}
	job, ok := m.Get(snap.ID)
	if !ok {
		t.Fatal("job missing")
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-job.done:
	case <-time.After(5 * time.Second):
		t.Fatal("job did not stop")
	}
	if _, err := m.Start(t.TempDir(), "echo x", 0); err == nil {
		t.Fatal("expected closed manager to reject new jobs")
	}
}

func TestJobOutputTruncation(t *testing.T) {
	m := newTestJobs(t)
	// More output than the read limit: More should be true.
	snap, err := m.Start(t.TempDir(), `yes x | head -c 200000`, 0)
	if err != nil {
		t.Fatal(err)
	}
	snap, err = m.Read(snap.ID, 5*time.Second, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Output) != 1024 || !snap.More {
		t.Fatalf("len=%d more=%v status=%s", len(snap.Output), snap.More, snap.Status)
	}
}
