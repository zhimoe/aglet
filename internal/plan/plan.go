package plan

import (
	"fmt"
	"sync"
)

const (
	StatusPending    = "pending"
	StatusInProgress = "in_progress"
	StatusCompleted  = "completed"
)

type Step struct {
	Step   string `json:"step"`
	Status string `json:"status"`
}

func ValidStatus(s string) bool {
	switch s {
	case StatusPending, StatusInProgress, StatusCompleted:
		return true
	default:
		return false
	}
}

// Board holds the current plan and notifies a listener on updates.
type Board struct {
	mu    sync.Mutex
	steps []Step
	emit  func([]Step)
}

func New() *Board { return &Board{} }

func (b *Board) SetEmitter(fn func([]Step)) {
	b.mu.Lock()
	b.emit = fn
	b.mu.Unlock()
}

func (b *Board) Update(steps []Step) error {
	if len(steps) == 0 {
		return fmt.Errorf("plan must contain at least one step")
	}
	if len(steps) > 20 {
		return fmt.Errorf("plan has too many steps (max 20)")
	}
	clean := make([]Step, 0, len(steps))
	for i, s := range steps {
		s.Step = trim(s.Step)
		if s.Step == "" {
			return fmt.Errorf("plan step %d is empty", i+1)
		}
		if s.Status == "" {
			s.Status = StatusPending
		}
		if !ValidStatus(s.Status) {
			return fmt.Errorf("plan step %d has invalid status %q", i+1, s.Status)
		}
		clean = append(clean, s)
	}
	b.mu.Lock()
	b.steps = clean
	emit := b.emit
	b.mu.Unlock()
	if emit != nil {
		emit(append([]Step(nil), clean...))
	}
	return nil
}

func (b *Board) Steps() []Step {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]Step(nil), b.steps...)
}

func trim(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\n' || s[0] == '\r') {
		s = s[1:]
	}
	for len(s) > 0 {
		c := s[len(s)-1]
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			break
		}
		s = s[:len(s)-1]
	}
	return s
}
