package agent

import (
	"testing"

	"google.golang.org/adk/v2/model"
	adksession "google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

func testEvent(partial bool, parts ...*genai.Part) *adksession.Event {
	return &adksession.Event{
		Author: "aglet",
		LLMResponse: model.LLMResponse{
			Partial: partial,
			Content: &genai.Content{Role: "model", Parts: parts},
		},
	}
}

func countType(events []Event, typ EventType) int {
	n := 0
	for _, e := range events {
		if e.Type == typ {
			n++
		}
	}
	return n
}

func TestMapperPartialDedupe(t *testing.T) {
	m := &Mapper{}
	var got []Event
	got = append(got, m.Map(testEvent(true, &genai.Part{Text: "Hel"}))...)
	got = append(got, m.Map(testEvent(true, &genai.Part{Text: "lo"}))...)
	got = append(got, m.Map(testEvent(false, &genai.Part{Text: "Hello"}))...)
	if n := countType(got, EventTextDelta); n != 2 {
		t.Fatalf("text events = %d, want 2 (%#v)", n, got)
	}
	if got[0].Text != "Hel" || got[1].Text != "lo" {
		t.Fatalf("texts = %q %q", got[0].Text, got[1].Text)
	}
}

func TestMapperFinalOnly(t *testing.T) {
	m := &Mapper{}
	got := m.Map(testEvent(false, &genai.Part{Text: "Hello"}))
	if len(got) != 1 || got[0].Text != "Hello" {
		t.Fatalf("got %#v", got)
	}
}

func TestMapperToolThoughtUsageError(t *testing.T) {
	m := &Mapper{}
	ev := testEvent(false,
		&genai.Part{FunctionCall: &genai.FunctionCall{ID: "c1", Name: "read_file", Args: map[string]any{"path": "x"}}},
		&genai.Part{Text: "thinking", Thought: true},
	)
	ev.UsageMetadata = &genai.GenerateContentResponseUsageMetadata{
		PromptTokenCount: 3, CandidatesTokenCount: 4, TotalTokenCount: 7,
	}
	got := m.Map(ev)
	if countType(got, EventToolCall) != 1 {
		t.Fatalf("no tool call: %#v", got)
	}
	if countType(got, EventThought) != 1 {
		t.Fatalf("no thought: %#v", got)
	}
	if countType(got, EventUsage) != 1 {
		t.Fatalf("no usage: %#v", got)
	}

	resp := testEvent(false, &genai.Part{FunctionResponse: &genai.FunctionResponse{
		ID: "c1", Name: "read_file", Response: map[string]any{"content": "hello"},
	}})
	got = m.Map(resp)
	if countType(got, EventToolResult) != 1 {
		t.Fatalf("no tool result: %#v", got)
	}
	if got[0].Result.Text != "hello" || got[0].Result.IsError {
		t.Fatalf("result = %#v", got[0].Result)
	}

	errEv := &adksession.Event{LLMResponse: model.LLMResponse{ErrorCode: "X", ErrorMessage: "bad"}}
	if n := countType(m.Map(errEv), EventError); n != 1 {
		t.Fatalf("error events = %d", n)
	}
}

func TestMapHistorySkipsPartialAndKeepsAuthor(t *testing.T) {
	partial := testEvent(true, &genai.Part{Text: "part"})
	if got := MapHistory(partial); len(got) != 0 {
		t.Fatalf("partial history = %#v", got)
	}
	full := testEvent(false, &genai.Part{Text: "hello"})
	full.Author = "user"
	got := MapHistory(full)
	if len(got) != 1 || got[0].Author != "user" || got[0].Text != "hello" {
		t.Fatalf("history = %#v", got)
	}
}
