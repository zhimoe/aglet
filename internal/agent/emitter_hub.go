package agent

import "sync"

// emitterHub serializes events from the model loop, parallel tool callbacks,
// hooks, and subagents into a single sink.
type emitterHub struct {
	mu      sync.Mutex
	sink    Emitter
	pending []string
}

func newEmitterHub() *emitterHub { return &emitterHub{} }

// SetSink installs an event sink and drains notices queued before it existed.
func (h *emitterHub) SetSink(sink Emitter) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sink = sink
	if sink == nil {
		return
	}
	pending := h.pending
	h.pending = nil
	for _, text := range pending {
		sink(Event{Type: EventNotice, Text: text})
	}
}

// Emit sends an event to the current sink. Notices are queued when no sink is
// installed; other events are dropped.
func (h *emitterHub) Emit(ev Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sink != nil {
		h.sink(ev)
		return
	}
	if ev.Type == EventNotice && len(h.pending) < 100 && ev.Text != "" {
		h.pending = append(h.pending, ev.Text)
	}
}

// Notice emits a notice event.
func (h *emitterHub) Notice(text string) {
	if text == "" {
		return
	}
	h.Emit(Event{Type: EventNotice, Text: text})
}
