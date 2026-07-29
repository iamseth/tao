package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/iamseth/tao/internal/agent/jsonmap"
	"github.com/iamseth/tao/internal/agent/logrecord"
	agentmetrics "github.com/iamseth/tao/internal/agent/metrics"
	"github.com/iamseth/tao/internal/agent/perm"
	"github.com/iamseth/tao/internal/agent/process"
	"github.com/iamseth/tao/internal/agent/streamjson"
)

type Client struct {
	ProcessStarter process.ProcessStarter
	Log            io.Writer
}

type Request struct {
	RepoRoot       string
	Prompt         string
	PermissionMode perm.PermissionMode
}

type Result struct {
	Output    string
	FinalText string
	SessionID string
	// ProviderID is inferred from the `metadata.<provider>` key OpenCode
	// attaches to message parts (e.g. "openai", "anthropic").
	ProviderID string
	// ModelID is currently always empty because the JSON event stream does not
	// carry a model identifier. Future requested-model plumb-through belongs in
	// the openCodeRuntime adapter.
	ModelID string
	// Usage accumulates per-step token counts across the stream.
	Usage TokenUsage
	// UsageObserved is true once at least one step_finish event carried a
	// token object, so a genuinely empty stream can be told apart from one
	// that reported zeroes.
	UsageObserved bool
	Cost          float64
	// Events holds the last raw event observed, mirroring the claude transport.
	Events map[string]any
	// Metrics is the neutral view of session statistics parsed from the stream.
	Metrics agentmetrics.Metrics
	// MetricsWarning explains why typed metrics could not be captured, or is
	// empty when Metrics is usable.
	MetricsWarning string

	loggedToolResults map[string]struct{}
}

func (c Client) RunAgentSession(ctx context.Context, request Request) (Result, error) {
	mode := request.PermissionMode
	if mode == "" {
		mode = perm.PermissionModeAuto
	}
	if !perm.Valid(mode) {
		return Result{}, fmt.Errorf("unsupported opencode permission mode %q", mode)
	}
	args := []string{"run", "--format", "json"}
	if mode == perm.PermissionModeBypassPermissions {
		args = append(args, "--dangerously-skip-permissions")
	}
	result, err := streamjson.RunSession(ctx, streamjson.SessionConfig[Result]{
		Starter:    c.ProcessStarter,
		RepoRoot:   request.RepoRoot,
		Executable: "opencode",
		Args:       args,
		Prompt:     request.Prompt,
		StreamKind: "json",
		Log:        c.Log,
		Handle:     c.handleEvent,
	})
	if !streamjson.IsPreReadError(err) {
		result.Metrics = parseSessionMetrics(result)
		result.MetricsWarning = metricsWarning(result)
	}
	return result, err
}

type event = map[string]any

// handleEvent folds one opencode stream event into result. It is the
// opencode-only part of the stream pump; the process lifecycle lives in
// internal/agent/streamjson.
func (c Client) handleEvent(ev streamjson.Event, result *Result) error {
	result.Events = ev
	part := mapValue(ev, "part")
	if result.SessionID == "" {
		result.SessionID = jsonmap.String(ev, "sessionID")
		if result.SessionID == "" && part != nil {
			result.SessionID = jsonmap.String(part, "sessionID")
		}
	}
	if result.ProviderID == "" && part != nil {
		result.ProviderID = providerFromMetadata(part["metadata"])
	}
	switch jsonmap.String(ev, "type") {
	case "text":
		if part != nil {
			if text := jsonmap.String(part, "text"); text != "" {
				if result.Output != "" {
					result.Output += "\n"
				}
				result.Output += text
				result.FinalText = text
			}
		}
	case "step_finish":
		accumulateTokens(result, part)
	case "error", "agent_error", "session_error":
		if err := eventError(ev, part); err != nil {
			return err
		}
	}
	c.logEvent(ev, part, result)
	return nil
}

func (c Client) logEvent(ev event, part map[string]any, result *Result) {
	if c.Log == nil {
		return
	}
	switch jsonmap.String(ev, "type") {
	case "text":
		if part != nil {
			if text := jsonmap.String(part, "text"); text != "" {
				_ = logrecord.Write(c.Log, logrecord.Record{Type: logrecord.TypeAssistant, Content: text})
			}
		}
	case "tool_use":
		c.logCompletedTool(part, result)
	}
}

func (c Client) logCompletedTool(part map[string]any, result *Result) {
	if part == nil {
		return
	}
	state := mapValue(part, "state")
	status := jsonmap.String(state, "status")
	if status != "completed" && status != "error" && status != "failed" {
		return
	}
	if id := jsonmap.FirstString(part, "callID", "callId", "id"); id != "" {
		if result.loggedToolResults == nil {
			result.loggedToolResults = make(map[string]struct{})
		}
		if _, logged := result.loggedToolResults[id]; logged {
			return
		}
		result.loggedToolResults[id] = struct{}{}
	}
	name := jsonmap.String(part, "tool")
	if name == "" {
		name = "tool"
	}
	_ = logrecord.Write(c.Log, logrecord.Record{Type: logrecord.TypeToolCall, Name: name, Payload: toolInput(part)})
	content := jsonmap.FirstString(state, "output", "error")
	if content == "" {
		value := state["output"]
		if value == nil {
			value = state["error"]
		}
		content = jsonmap.Stringify(value)
	}
	_ = logrecord.Write(c.Log, logrecord.Record{
		Type: logrecord.TypeToolResult, Name: name, Content: content,
		Failed: status == "error" || status == "failed",
	})
}

func accumulateTokens(result *Result, part map[string]any) {
	if part == nil {
		return
	}
	if tokens := mapValue(part, "tokens"); tokens != nil {
		result.UsageObserved = true
		result.Usage.Input += jsonmap.Int64(tokens, "input")
		result.Usage.Output += jsonmap.Int64(tokens, "output")
		result.Usage.Reasoning += jsonmap.Int64(tokens, "reasoning")
		result.Usage.Total += jsonmap.Int64(tokens, "total")
		if cache := mapValue(tokens, "cache"); cache != nil {
			result.Usage.CacheRead += jsonmap.Int64(cache, "read")
			result.Usage.CacheWrite += jsonmap.Int64(cache, "write")
		}
	}
	if cost, ok := numberValue(part, "cost"); ok {
		result.Cost += cost
	}
}

// eventError builds an error from an OpenCode failure event. OpenCode's error
// event shape is not strongly documented, so this reads the message
// defensively from the event or its part, tolerating either a flat string or a
// nested {message} object.
func eventError(ev event, part map[string]any) error {
	message := jsonmap.FirstString(ev, "error", "message")
	if nested := mapValue(ev, "error"); nested != nil {
		message = jsonmap.FirstString(nested, "message", "error")
	}
	if message == "" && part != nil {
		message = jsonmap.FirstString(part, "error", "message")
		if nested := mapValue(part, "error"); nested != nil {
			message = jsonmap.FirstString(nested, "message", "error")
		}
	}
	if message == "" {
		message = "opencode agent failed"
	}
	return errors.New("opencode agent error: " + message)
}

func providerFromMetadata(value any) string {
	meta, ok := value.(map[string]any)
	if !ok || len(meta) == 0 {
		return ""
	}
	keys := make([]string, 0, len(meta))
	for key := range meta {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys[0]
}

func toolInput(part map[string]any) string {
	state := mapValue(part, "state")
	if state == nil {
		return ""
	}
	return jsonmap.Stringify(state["input"])
}

func mapValue(values map[string]any, key string) map[string]any {
	nested, _ := values[key].(map[string]any)
	return nested
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
		case json.Number:
			parsed, err := value.Float64()
			if err == nil {
				return parsed, true
			}
		}
	}
	return 0, false
}
