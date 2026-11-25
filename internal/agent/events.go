package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	adksession "google.golang.org/adk/v2/session"

	"github.com/zhimoe/aglet/internal/plan"
)

type EventType string

const (
	EventTextDelta  EventType = "text"
	EventThought    EventType = "thought"
	EventToolCall   EventType = "tool_call"
	EventToolResult EventType = "tool_result"
	EventUsage      EventType = "usage"
	EventError      EventType = "error"
	EventDone       EventType = "done"
	EventPlan       EventType = "plan"
)

type Event struct {
	Type   EventType
	Author string // "user" or the agent name for history replay; empty for live streaming
	Text   string
	Call   *ToolCall
	Result *ToolResult
	Usage  *Usage
	Plan   []plan.Step
	Err    error
}

type ToolCall struct {
	ID   string
	Name string
	Args map[string]any
}

type ToolResult struct {
	ID      string
	Name    string
	Result  map[string]any
	IsError bool
	Text    string
}

type Usage struct {
	Prompt     int32
	Completion int32
	Total      int32
}

// Emitter receives normalized events. It may be called from a runtime goroutine.
type Emitter func(Event)

// Mapper converts ADK session events into UI events. It keeps per-turn state so
// partial text deltas are not duplicated by the final aggregate event.
type Mapper struct {
	streamed   strings.Builder
	sawPartial bool
}

func (m *Mapper) Reset() {
	m.streamed.Reset()
	m.sawPartial = false
}

// Map converts one ADK event into zero or more UI events.
func (m *Mapper) Map(ev *adksession.Event) []Event {
	if ev == nil {
		return nil
	}
	var out []Event
	if ev.ErrorCode != "" {
		out = append(out, Event{Type: EventError, Err: fmt.Errorf("%s: %s", ev.ErrorCode, ev.ErrorMessage)})
	}
	if ev.Content != nil {
		var finalText strings.Builder
		for _, p := range ev.Content.Parts {
			if p == nil {
				continue
			}
			switch {
			case p.FunctionCall != nil:
				out = append(out, Event{
					Type:   EventToolCall,
					Author: ev.Author,
					Call: &ToolCall{
						ID:   p.FunctionCall.ID,
						Name: p.FunctionCall.Name,
						Args: p.FunctionCall.Args,
					},
				})
			case p.FunctionResponse != nil:
				text, isErr := responseText(p.FunctionResponse.Response)
				out = append(out, Event{
					Type:   EventToolResult,
					Author: ev.Author,
					Result: &ToolResult{
						ID:      p.FunctionResponse.ID,
						Name:    p.FunctionResponse.Name,
						Result:  p.FunctionResponse.Response,
						IsError: isErr,
						Text:    text,
					},
				})
			case p.Text != "" && p.Thought:
				out = append(out, Event{Type: EventThought, Author: ev.Author, Text: p.Text})
			case p.Text != "":
				if ev.Partial {
					out = append(out, Event{Type: EventTextDelta, Author: ev.Author, Text: p.Text})
					m.streamed.WriteString(p.Text)
					m.sawPartial = true
				} else {
					finalText.WriteString(p.Text)
				}
			}
		}
		if finalText.Len() > 0 {
			final := finalText.String()
			switch {
			case !m.sawPartial:
				out = append(out, Event{Type: EventTextDelta, Author: ev.Author, Text: final})
				m.streamed.WriteString(final)
			case strings.HasPrefix(final, m.streamed.String()):
				extra := final[m.streamed.Len():]
				if extra != "" {
					out = append(out, Event{Type: EventTextDelta, Author: ev.Author, Text: extra})
					m.streamed.WriteString(extra)
				}
			}
		}
	}
	if ev.UsageMetadata != nil {
		out = append(out, Event{Type: EventUsage, Usage: &Usage{
			Prompt:     ev.UsageMetadata.PromptTokenCount,
			Completion: ev.UsageMetadata.CandidatesTokenCount,
			Total:      ev.UsageMetadata.TotalTokenCount,
		}})
	}
	return out
}

// MapHistory converts a stored (non-streaming) event for UI replay.
func MapHistory(ev *adksession.Event) []Event {
	if ev == nil || ev.Partial {
		return nil
	}
	var out []Event
	if ev.Content != nil {
		for _, p := range ev.Content.Parts {
			if p == nil {
				continue
			}
			switch {
			case p.FunctionCall != nil:
				out = append(out, Event{Type: EventToolCall, Author: ev.Author, Call: &ToolCall{
					ID: p.FunctionCall.ID, Name: p.FunctionCall.Name, Args: p.FunctionCall.Args,
				}})
			case p.FunctionResponse != nil:
				text, isErr := responseText(p.FunctionResponse.Response)
				out = append(out, Event{Type: EventToolResult, Author: ev.Author, Result: &ToolResult{
					ID: p.FunctionResponse.ID, Name: p.FunctionResponse.Name,
					Result: p.FunctionResponse.Response, IsError: isErr, Text: text,
				}})
			case p.Text != "" && p.Thought:
				out = append(out, Event{Type: EventThought, Author: ev.Author, Text: p.Text})
			case p.Text != "":
				out = append(out, Event{Type: EventTextDelta, Author: ev.Author, Text: p.Text})
			}
		}
	}
	if ev.UsageMetadata != nil {
		out = append(out, Event{Type: EventUsage, Author: ev.Author, Usage: &Usage{
			Prompt:     ev.UsageMetadata.PromptTokenCount,
			Completion: ev.UsageMetadata.CandidatesTokenCount,
			Total:      ev.UsageMetadata.TotalTokenCount,
		}})
	}
	return out
}

func responseText(resp map[string]any) (string, bool) {
	if resp == nil {
		return "", false
	}
	if v, ok := resp["error"]; ok && v != nil {
		return fmt.Sprint(v), true
	}
	if v, ok := resp["is_error"].(bool); ok && v {
		if s, ok := resp["content"].(string); ok {
			return s, true
		}
	}
	for _, key := range []string{"content", "result", "output", "text", "message"} {
		if v, ok := resp[key]; ok {
			if s, ok := v.(string); ok {
				return s, false
			}
		}
	}
	b, err := json.Marshal(resp)
	if err != nil {
		return fmt.Sprint(resp), false
	}
	return string(b), false
}
