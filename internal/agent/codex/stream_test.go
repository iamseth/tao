package codex

import (
	"strings"
	"testing"
)

func TestHandleEventCapturesThreadAndAgentMessages(t *testing.T) {
	var result Result
	client := Client{}
	threadEvent := event{"type": "thread.started", "thread_id": "thread_1"}
	messageEvent := event{
		"type": "item.completed",
		"item": map[string]any{
			"type": "agent_message",
			"content": []any{
				map[string]any{"type": "text", "text": "hello"},
			},
		},
	}
	if err := client.handleEvent(threadEvent, &result); err != nil {
		t.Fatal(err)
	}
	if err := client.handleEvent(messageEvent, &result); err != nil {
		t.Fatal(err)
	}
	if result.SessionID != "thread_1" || result.FinalText != "hello" || result.Output != "hello" || result.ProviderID != "openai" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestHandleEventCapturesCurrentCodexEventNames(t *testing.T) {
	var result Result
	client := Client{}
	for _, ev := range []event{
		{"type": "session_configured", "session_id": "session_1", "model": "gpt-5-codex", "model_provider_id": "openai"},
		{"type": "agent_message", "message": "working"},
		{"type": "task_complete", "last_agent_message": "done", "total_token_usage": map[string]any{"input_tokens": float64(10), "cached_input_tokens": float64(2), "output_tokens": float64(4), "reasoning_output_tokens": float64(3)}},
	} {
		if err := client.handleEvent(ev, &result); err != nil {
			t.Fatal(err)
		}
	}
	if result.SessionID != "session_1" || result.ModelID != "gpt-5-codex" || result.ProviderID != "openai" {
		t.Fatalf("unexpected identity: %#v", result)
	}
	if result.Output != "working\ndone" || result.FinalText != "done" {
		t.Fatalf("unexpected assistant text: %#v", result)
	}
	if !result.UsageObserved || result.Usage.Input != 10 || result.Usage.CachedInput != 2 || result.Usage.Output != 4 || result.Usage.ReasoningOutput != 3 {
		t.Fatalf("unexpected usage: %#v", result.Usage)
	}
}

func TestHandleEventIgnoresNonAgentMessageItems(t *testing.T) {
	for _, ev := range []event{
		{"type": "item.completed", "item": map[string]any{"type": "tool_call", "text": "ignore"}},
		{"type": "item_completed", "item": map[string]any{"type": "message", "role": "user", "content": "ignore"}},
	} {
		var result Result
		err := (Client{}).handleEvent(ev, &result)
		if err != nil {
			t.Fatal(err)
		}
		if result.Output != "" || result.FinalText != "" {
			t.Fatalf("expected no text, got %#v", result)
		}
	}
}

func TestHandleEventAccumulatesTurnUsage(t *testing.T) {
	var result Result
	client := Client{}
	for _, ev := range []event{
		{"type": "turn.completed", "usage": map[string]any{"input_tokens": float64(10), "cached_input_tokens": float64(2), "output_tokens": float64(4), "reasoning_output_tokens": float64(1)}},
		{"type": "turn_complete", "turn": map[string]any{"usage": map[string]any{"input_tokens": float64(3), "output_tokens": float64(5), "total_tokens": float64(20)}}},
	} {
		if err := client.handleEvent(ev, &result); err != nil {
			t.Fatal(err)
		}
	}
	if !result.UsageObserved || result.Usage.Input != 13 || result.Usage.CachedInput != 2 || result.Usage.Output != 9 || result.Usage.ReasoningOutput != 1 || result.Usage.Total != 20 {
		t.Fatalf("unexpected usage: %#v", result.Usage)
	}
}

func TestHandleEventReturnsErrorEvents(t *testing.T) {
	for _, ev := range []event{
		{"type": "error", "error": map[string]any{"message": "auth failed"}},
		{"type": "turn.failed", "message": "turn exploded"},
		{"type": "stream_error", "reason": "stream exploded"},
		{"type": "turn_aborted", "reason": "turn aborted"},
	} {
		t.Run(ev["type"].(string), func(t *testing.T) {
			err := (Client{}).handleEvent(ev, &Result{})
			if err == nil || !strings.Contains(err.Error(), "codex agent error") {
				t.Fatalf("expected codex error, got %v", err)
			}
		})
	}
}
