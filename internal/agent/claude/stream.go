package claude

import (
	"errors"
	"fmt"
	"strings"

	"github.com/iamseth/tao/internal/agent/jsonmap"
	"github.com/iamseth/tao/internal/agent/streamjson"
)

type event = map[string]any

// handleEvent folds one claude stream event into result. It is the claude-only
// part of the stream pump; the process lifecycle lives in internal/agent/streamjson.
func (c Client) handleEvent(ev streamjson.Event, result *Result) error {
	result.Events = ev
	extractTelemetry(ev, result)
	c.logEvent(ev)
	if text := assistantText(ev); text != "" {
		if result.Output != "" {
			result.Output += "\n"
		}
		result.Output += text
		result.FinalText = text
	}
	if text := resultText(ev); text != "" && result.FinalText == "" {
		result.Output = text
		result.FinalText = text
	}
	if err := eventError(ev); err != nil {
		return err
	}
	return nil
}

func (c Client) logEvent(ev event) {
	if c.Log == nil {
		return
	}
	if text := assistantText(ev); text != "" {
		_, _ = fmt.Fprintf(c.Log, "assistant: %s\n", text)
	}
	for _, call := range toolCalls(ev) {
		_, _ = fmt.Fprintf(c.Log, "→ %s %s\n", call.name, call.input)
	}
	for _, result := range toolResults(ev) {
		_, _ = fmt.Fprintf(c.Log, "✓ tool\n%s\n", result)
	}
	if err := eventError(ev); err != nil {
		c.logClaudeError(err)
	}
}

func (c Client) logClaudeError(err error) {
	if c.Log != nil && err != nil {
		_, _ = fmt.Fprintf(c.Log, "tao claude error: %v\n", err)
	}
}

func eventError(event event) error {
	typ := eventType(event)
	if typ == "error" || typ == "agent_error" || boolValue(event, "is_error") || jsonmap.String(event, "subtype") == "error" {
		message := jsonmap.FirstString(event, "error", "message", "error_message")
		if nested, ok := event["error"].(map[string]any); ok {
			message = jsonmap.FirstString(nested, "message", "error")
		}
		if message == "" {
			message = "claude agent failed"
		}
		return errors.New("claude agent error: " + message)
	}
	return nil
}

func extractTelemetry(event event, result *Result) {
	if result.SessionID == "" {
		result.SessionID = jsonmap.FirstString(event, "session_id", "sessionId")
	}
	if result.Model == "" {
		result.Model = jsonmap.FirstString(event, "model", "model_id", "modelId")
	}
	if usage, ok := event["usage"].(map[string]any); ok {
		result.Usage = usage
	}
	if cost, ok := numberValue(event, "total_cost_usd", "cost_usd", "cost"); ok {
		result.CostUSD = cost
	}
	if message, ok := event["message"].(map[string]any); ok {
		if result.Model == "" {
			result.Model = jsonmap.FirstString(message, "model", "model_id", "modelId")
		}
		if usage, ok := message["usage"].(map[string]any); ok {
			result.Usage = usage
		}
	}
}

func eventType(event event) string {
	if typ := jsonmap.String(event, "type"); typ != "" {
		return typ
	}
	return jsonmap.String(event, "event")
}

func assistantText(event event) string {
	if role := jsonmap.String(event, "role"); role != "" && role != "assistant" {
		return ""
	}
	if eventType(event) == "result" {
		return ""
	}
	for _, key := range []string{"final_text", "text", "content"} {
		if text := jsonmap.String(event, key); text != "" {
			return text
		}
	}
	if message, ok := event["message"].(map[string]any); ok && jsonmap.String(message, "role") == "assistant" {
		if text := jsonmap.String(message, "content"); text != "" {
			return text
		}
		return contentText(message["content"])
	}
	return ""
}

func resultText(event event) string {
	if eventType(event) != "result" {
		return ""
	}
	return jsonmap.FirstString(event, "result", "text", "content")
}

func contentText(value any) string {
	blocks, ok := value.([]any)
	if !ok {
		return ""
	}
	var b strings.Builder
	for _, block := range blocks {
		blockMap, ok := block.(map[string]any)
		if !ok || jsonmap.String(blockMap, "type") != "text" {
			continue
		}
		b.WriteString(jsonmap.String(blockMap, "text"))
	}
	return b.String()
}

type toolCall struct{ name, input string }

func toolCalls(event event) []toolCall {
	message, _ := event["message"].(map[string]any)
	blocks, _ := message["content"].([]any)
	var calls []toolCall
	for _, block := range blocks {
		blockMap, ok := block.(map[string]any)
		if !ok {
			continue
		}
		typ := jsonmap.String(blockMap, "type")
		if typ != "tool_use" && typ != "toolCall" {
			continue
		}
		name := jsonmap.String(blockMap, "name")
		if name == "" {
			name = "tool"
		}
		input := jsonmap.Stringify(blockMap["input"])
		if input == "" {
			input = jsonmap.Stringify(blockMap["arguments"])
		}
		calls = append(calls, toolCall{name: name, input: input})
	}
	return calls
}

func toolResults(event event) []string {
	message, _ := event["message"].(map[string]any)
	blocks, _ := message["content"].([]any)
	var results []string
	for _, block := range blocks {
		blockMap, ok := block.(map[string]any)
		if !ok || jsonmap.String(blockMap, "type") != "tool_result" {
			continue
		}
		text := jsonmap.String(blockMap, "content")
		if text == "" {
			text = contentText(blockMap["content"])
		}
		results = append(results, text)
	}
	return results
}

func boolValue(values map[string]any, key string) bool {
	value, _ := values[key].(bool)
	return value
}

func numberValue(values map[string]any, keys ...string) (float64, bool) {
	for _, key := range keys {
		switch value := values[key].(type) {
		case float64:
			return value, true
		case float32:
			return float64(value), true
		case int:
			return float64(value), true
		case int64:
			return float64(value), true
		}
	}
	return 0, false
}
