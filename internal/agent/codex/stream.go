package codex

import (
	"errors"
	"fmt"
	"strings"

	"github.com/iamseth/tao/internal/agent/jsonmap"
	"github.com/iamseth/tao/internal/agent/streamjson"
)

type event = map[string]any

func (c Client) handleEvent(ev streamjson.Event, result *Result) error {
	if provider := jsonmap.FirstString(ev, "model_provider_id", "provider_id", "providerID", "provider"); provider != "" {
		result.ProviderID = provider
	}
	if result.ProviderID == "" {
		result.ProviderID = "openai"
	}
	if model := jsonmap.FirstString(ev, "model", "model_id", "modelId"); model != "" {
		result.ModelID = model
	}
	switch jsonmap.String(ev, "type") {
	case "session_configured", "thread.started", "thread_started":
		if result.SessionID == "" {
			result.SessionID = jsonmap.FirstString(ev, "session_id", "sessionId", "thread_id", "threadId")
		}
	case "turn.completed", "turn_completed", "turn_complete", "task_complete", "token_count":
		accumulateTokens(result, eventUsage(ev))
		if text := jsonmap.FirstString(ev, "last_agent_message", "message", "final_message"); text != "" {
			c.recordAssistantText(result, text)
		}
	case "agent_message":
		if text := agentMessageEventText(ev); text != "" {
			c.recordAssistantText(result, text)
		}
	case "item.completed", "item_completed":
		if text := agentMessageText(ev); text != "" {
			c.recordAssistantText(result, text)
		}
	case "error", "stream_error", "turn.failed", "turn_failed", "turn_aborted", "task_failed":
		return eventError(ev)
	}
	return nil
}

func (c Client) recordAssistantText(result *Result, text string) {
	if text == "" || result.FinalText == text {
		return
	}
	if result.Output != "" {
		result.Output += "\n"
	}
	result.Output += text
	result.FinalText = text
	c.logAssistant(text)
}

func (c Client) logAssistant(text string) {
	if c.Log != nil && text != "" {
		_, _ = fmt.Fprintf(c.Log, "assistant: %s\n", text)
	}
}

func eventUsage(ev event) map[string]any {
	for _, key := range []string{"usage", "token_usage", "total_token_usage"} {
		if usage := mapValue(ev, key); usage != nil {
			return usage
		}
	}
	if turn := mapValue(ev, "turn"); turn != nil {
		for _, key := range []string{"usage", "token_usage", "total_token_usage"} {
			if usage := mapValue(turn, key); usage != nil {
				return usage
			}
		}
	}
	return nil
}

func accumulateTokens(result *Result, usage map[string]any) {
	if usage == nil {
		return
	}
	result.UsageObserved = true
	result.Usage.Input += jsonmap.FirstInt64(usage, "input_tokens", "inputTokens")
	result.Usage.CachedInput += jsonmap.FirstInt64(usage, "cached_input_tokens", "cachedInputTokens", "cache_read_tokens", "cacheReadTokens")
	result.Usage.Output += jsonmap.FirstInt64(usage, "output_tokens", "outputTokens")
	result.Usage.ReasoningOutput += jsonmap.FirstInt64(usage, "reasoning_output_tokens", "reasoningOutputTokens", "reasoning_tokens", "reasoningTokens")
	result.Usage.Total += jsonmap.FirstInt64(usage, "total_tokens", "totalTokens", "total")
}

func agentMessageEventText(ev event) string {
	if text := jsonmap.FirstString(ev, "message", "text", "last_agent_message"); text != "" {
		return text
	}
	if message := mapValue(ev, "message"); message != nil {
		return agentMessageMapText(message)
	}
	return contentText(ev["content"])
}

func agentMessageText(ev event) string {
	item := mapValue(ev, "item")
	if item == nil {
		return ""
	}
	typ := jsonmap.String(item, "type")
	role := jsonmap.FirstString(item, "role", "author")
	if typ != "agent_message" && role != "assistant" {
		return ""
	}
	return agentMessageMapText(item)
}

func agentMessageMapText(values map[string]any) string {
	if text := jsonmap.FirstString(values, "text", "message", "content"); text != "" {
		return text
	}
	return contentText(values["content"])
}

func contentText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case map[string]any:
		return contentBlockText(typed)
	case []any:
		var b strings.Builder
		for _, block := range typed {
			blockMap, ok := block.(map[string]any)
			if !ok {
				continue
			}
			b.WriteString(contentBlockText(blockMap))
		}
		return b.String()
	default:
		return ""
	}
}

func contentBlockText(block map[string]any) string {
	if typ := jsonmap.String(block, "type"); typ != "" && typ != "text" && typ != "output_text" && typ != "final_answer" {
		return ""
	}
	return jsonmap.FirstString(block, "text", "content")
}

func eventError(ev event) error {
	message := jsonmap.FirstString(ev, "message", "error", "error_message", "reason")
	if nested := mapValue(ev, "error"); nested != nil {
		if nestedMessage := jsonmap.FirstString(nested, "message", "error", "reason"); nestedMessage != "" {
			message = nestedMessage
		}
	}
	if message == "" {
		message = "codex agent failed"
	}
	return errors.New("codex agent error: " + message)
}

func mapValue(values map[string]any, key string) map[string]any {
	nested, _ := values[key].(map[string]any)
	return nested
}
