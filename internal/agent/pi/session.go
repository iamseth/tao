package pi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"

	"github.com/iamseth/tao/internal/agent/jsonmap"
	"github.com/iamseth/tao/internal/agent/logrecord"
)

type session struct {
	proc                   Process
	stdin                  io.WriteCloser
	events                 chan readResult
	queuedEvents           []event
	log                    io.Writer
	stderrDone             chan struct{}
	mu                     sync.Mutex
	pendingToolCalls       map[string]toolCall
	pendingToolCallsByName map[string]toolCall
	loggedToolResults      map[string]bool
	watchdog               noProgressWatchdog
	abortOnce              sync.Once
}

func newSession(proc Process, log io.Writer, noProgressToolLimit int, verificationCommands []string) *session {
	s := &session{
		proc:                   proc,
		stdin:                  proc.Stdin(),
		events:                 make(chan readResult),
		log:                    log,
		pendingToolCalls:       map[string]toolCall{},
		pendingToolCallsByName: map[string]toolCall{},
		loggedToolResults:      map[string]bool{},
		watchdog:               newNoProgressWatchdog(noProgressToolLimit, verificationCommands),
	}
	go s.readStdout(proc.Stdout())
	if stderr := proc.Stderr(); stderr != nil {
		s.stderrDone = make(chan struct{})
		go func() {
			defer close(s.stderrDone)
			drainStderr(stderr, log)
		}()
	}
	return s
}

func drainStderr(stderr io.Reader, log io.Writer) {
	if log == nil {
		_, _ = io.Copy(io.Discard, stderr)
		return
	}
	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
		_ = logrecord.Write(log, logrecord.Record{Type: logrecord.TypeDiagnostic, Content: fmt.Sprintf("tao pi stderr: %s", scanner.Text())})
	}
	if err := scanner.Err(); err != nil {
		_ = logrecord.Write(log, logrecord.Record{Type: logrecord.TypeDiagnostic, Content: fmt.Sprintf("tao pi stderr: %v", err)})
	}
}

func (s *session) queuedResult() Result {
	var result Result
	for _, event := range s.queuedEvents {
		if err := s.logAgentEvent(event); err != nil {
			s.logPiError(err)
		}
		if text := assistantText(event); text != "" {
			if result.Output != "" {
				result.Output += "\n"
			}
			result.Output += text
			result.FinalText = text
		}
		if eventType(event) == "agent_end" {
			result.SessionID = jsonmap.String(event, "session_id")
			result.State = event
		}
	}
	s.queuedEvents = nil
	return result
}

func (s *session) waitForAgentEnd(ctx context.Context) (Result, error) {
	var result Result
	for {
		event, err := s.next(ctx)
		if err != nil {
			return Result{}, err
		}
		if err := s.handleResponseError(event); err != nil {
			return Result{}, err
		}
		if err := s.handleUIRequest(ctx, event); err != nil {
			return Result{}, err
		}
		if err := agentEventError(event); err != nil {
			s.logPiError(err)
			return result, err
		}
		if err := s.logAgentEvent(event); err != nil {
			s.logPiError(err)
			return result, s.abort(err)
		}
		if text := assistantText(event); text != "" {
			if result.Output != "" {
				result.Output += "\n"
			}
			result.Output += text
			result.FinalText = text
		}
		if eventType(event) == "agent_end" {
			result.SessionID = jsonmap.String(event, "session_id")
			result.State = event
			return result, nil
		}
	}
}

func (s *session) logAgentEvent(event event) error {
	message, ok := event["message"].(map[string]any)
	if !ok {
		return nil
	}
	role := jsonmap.String(message, "role")
	switch role {
	case "assistant":
		for _, call := range toolCalls(message["content"]) {
			if call.id == "" {
				s.pendingToolCallsByName[call.name] = call
				continue
			}
			s.pendingToolCalls[call.id] = call
		}
	case "toolResult":
		return s.logToolResult(message)
	}
	return nil
}

func (s *session) logToolResult(message map[string]any) error {
	id := jsonmap.FirstString(message, "toolCallId", "tool_call_id")
	if id != "" && s.loggedToolResults[id] {
		return nil
	}
	name := jsonmap.String(message, "toolName")
	call := toolCall{id: id, name: name}
	if id != "" {
		if pending, ok := s.pendingToolCalls[id]; ok {
			call = pending
			if name == "" {
				name = pending.name
			}
			delete(s.pendingToolCalls, id)
		}
		s.loggedToolResults[id] = true
	}
	if name == "" {
		name = call.name
	}
	if id == "" && name != "" {
		if pending, ok := s.pendingToolCallsByName[name]; ok {
			call = pending
			delete(s.pendingToolCallsByName, name)
		}
	}
	if name == "" {
		name = call.name
	}
	if name == "" {
		name = "tool"
	}
	if call.name == "" {
		call.name = name
	}
	if err := s.watchdog.observe(call); err != nil {
		return err
	}
	if call.name != "" && call.arguments != "" {
		s.logToolCall(call)
	}
	s.logToolResultText(name, message)
	return nil
}

func (s *session) logToolCall(call toolCall) {
	if s.log == nil {
		return
	}
	_ = logrecord.Write(s.log, logrecord.Record{Type: logrecord.TypeToolCall, Name: call.name, Payload: call.arguments})
}

func (s *session) logToolResultText(name string, message map[string]any) {
	if s.log == nil {
		return
	}
	failed, _ := message["isError"].(bool)
	_ = logrecord.Write(s.log, logrecord.Record{Type: logrecord.TypeToolResult, Name: name, Content: contentText(message["content"]), Failed: failed})
}

func (s *session) logPiError(err error) {
	if s.log == nil || err == nil {
		return
	}
	_ = logrecord.Write(s.log, logrecord.Record{Type: logrecord.TypeDiagnostic, Content: fmt.Sprintf("tao pi error: %v", err)})
}

type toolCall struct {
	id        string
	name      string
	arguments string
}

type noProgressWatchdog struct {
	limit                int
	count                int
	verificationCommands map[string]struct{}
}

func newNoProgressWatchdog(limit int, verificationCommands []string) noProgressWatchdog {
	commands := make(map[string]struct{}, len(verificationCommands))
	for _, command := range verificationCommands {
		if normalized := normalizeBashCommand(command); normalized != "" {
			commands[normalized] = struct{}{}
		}
	}
	return noProgressWatchdog{limit: limit, verificationCommands: commands}
}

func (w *noProgressWatchdog) observe(call toolCall) error {
	if w.limit <= 0 {
		return nil
	}
	if w.productiveToolCall(call) {
		w.count = 0
		return nil
	}
	w.count++
	if w.count < w.limit {
		return nil
	}
	return fmt.Errorf("pi no-progress watchdog: %d tool calls without edits, verification, or slice lifecycle activity", w.limit)
}

func (w *noProgressWatchdog) productiveToolCall(call toolCall) bool {
	switch call.name {
	case "edit", "write":
		return true
	case "bash":
		command := splitBashCommand(bashToolCommand(call.arguments))
		for start := range command.segments {
			candidate := command.segments[start]
			if _, ok := w.verificationCommands[candidate]; ok {
				return true
			}
			for end := start + 1; end < len(command.segments); end++ {
				candidate += " " + command.separators[end-1] + " " + command.segments[end]
				if _, ok := w.verificationCommands[candidate]; ok {
					return true
				}
			}
		}
		return slices.ContainsFunc(command.segments, taoSliceLifecycleCommand)
	default:
		return false
	}
}

type bashCommand struct {
	segments   []string
	separators []string
}

func normalizeBashCommand(command string) string {
	parsed := splitBashCommand(command)
	if len(parsed.segments) == 0 {
		return ""
	}
	var normalized strings.Builder
	normalized.WriteString(parsed.segments[0])
	for i, separator := range parsed.separators {
		normalized.WriteString(" " + separator + " " + parsed.segments[i+1])
	}
	return normalized.String()
}

func splitBashCommand(command string) bashCommand {
	var parsed bashCommand
	var segment strings.Builder
	var quote rune
	escaped := false
	pendingSeparator := ""
	flush := func() {
		normalized := strings.Join(strings.Fields(segment.String()), " ")
		segment.Reset()
		if normalized == "" {
			return
		}
		if len(parsed.segments) > 0 {
			parsed.separators = append(parsed.separators, pendingSeparator)
		}
		parsed.segments = append(parsed.segments, normalized)
		pendingSeparator = ""
	}

	runes := []rune(command)
	for i := 0; i < len(runes); i++ {
		current := runes[i]
		if escaped {
			segment.WriteRune(current)
			escaped = false
			continue
		}
		if quote != 0 {
			segment.WriteRune(current)
			if current == '\\' && quote != '\'' {
				escaped = true
			} else if current == quote {
				quote = 0
			}
			continue
		}
		switch current {
		case '\\':
			segment.WriteRune(current)
			escaped = true
		case '\'', '"', '`':
			segment.WriteRune(current)
			quote = current
		case '&', '|':
			flush()
			pendingSeparator = string(current)
			if i+1 < len(runes) && runes[i+1] == current {
				pendingSeparator += string(current)
				i++
			}
		case ';':
			flush()
			pendingSeparator = ";"
		case '\n':
			flush()
			pendingSeparator = ";"
		default:
			segment.WriteRune(current)
		}
	}
	flush()
	return parsed
}

func taoSliceLifecycleCommand(command string) bool {
	fields := strings.Fields(command)
	if len(fields) < 2 || fields[0] != "tao" {
		return false
	}
	return fields[1] == "slice-complete" || fields[1] == "slice-blocked"
}

func bashToolCommand(arguments string) string {
	var values map[string]any
	if err := json.Unmarshal([]byte(arguments), &values); err != nil {
		return arguments
	}
	return jsonmap.String(values, "command")
}

func toolCalls(value any) []toolCall {
	blocks, ok := value.([]any)
	if !ok {
		return nil
	}
	var calls []toolCall
	for _, block := range blocks {
		blockMap, ok := block.(map[string]any)
		if !ok || jsonmap.String(blockMap, "type") != "toolCall" {
			continue
		}
		call := toolCall{id: jsonmap.FirstString(blockMap, "id", "toolCallId", "tool_call_id"), name: jsonmap.String(blockMap, "name")}
		if call.name == "" {
			call.name = "tool"
		}
		if args, ok := blockMap["arguments"]; ok && args != nil {
			switch typed := args.(type) {
			case string:
				call.arguments = typed
			default:
				if data, err := json.Marshal(args); err == nil {
					call.arguments = string(data)
				}
			}
		}
		calls = append(calls, call)
	}
	return calls
}

func agentEventError(event event) error {
	if err := agentMapError(event, eventType(event)); err != nil {
		return err
	}
	if message, ok := event["message"].(map[string]any); ok {
		return agentMapError(message, eventType(event))
	}
	return nil
}

func agentMapError(values map[string]any, typ string) error {
	stopReason := jsonmap.FirstString(values, "stopReason", "stop_reason")
	status := jsonmap.FirstString(values, "status", "result")
	explicitFailure := typ == "agent_error" || typ == "error" || stopReason == "error" || status == "error" || status == "failed"
	if !explicitFailure {
		return nil
	}
	message := jsonmap.FirstString(values, "errorMessage", "error_message", "error", "message")
	diagnosticValue := values["diagnostics"]
	diagnostics := diagnosticSummary(diagnosticValue)
	if message == "" {
		message = diagnostics
	}
	if message == "" && stopReason != "" {
		message = "agent stopped with " + stopReason
	}
	if message == "" {
		message = "pi agent failed"
	}
	if diagnostics != "" && !strings.Contains(message, diagnostics) {
		message += " (" + diagnostics + ")"
	}
	err := fmt.Errorf("pi agent error: %s", message)
	if hasProviderTransportFailure(diagnosticValue) {
		return retryableTransportError{err: err}
	}
	return err
}

type retryableTransportError struct {
	err error
}

func (e retryableTransportError) Error() string            { return e.err.Error() }
func (e retryableTransportError) Unwrap() error            { return e.err }
func (retryableTransportError) RetryableTransportFailure() {}

func hasProviderTransportFailure(value any) bool {
	items, ok := value.([]any)
	if !ok {
		return false
	}
	for _, item := range items {
		values, ok := item.(map[string]any)
		if ok && jsonmap.String(values, "type") == "provider_transport_failure" {
			return true
		}
	}
	return false
}

func diagnosticSummary(value any) string {
	items, ok := value.([]any)
	if !ok || len(items) == 0 {
		return ""
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		values, ok := item.(map[string]any)
		if !ok {
			continue
		}
		label := jsonmap.FirstString(values, "type", "name")
		message := jsonmap.FirstString(values, "message", "errorMessage", "error_message")
		if nested, ok := values["error"].(map[string]any); ok {
			if nestedMessage := jsonmap.FirstString(nested, "message", "error"); nestedMessage != "" {
				message = nestedMessage
			}
		}
		switch {
		case label != "" && message != "":
			parts = append(parts, label+": "+message)
		case label != "":
			parts = append(parts, label)
		case message != "":
			parts = append(parts, message)
		}
	}
	return strings.Join(parts, "; ")
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
	for _, key := range []string{"final_text", "text", "content"} {
		if text := jsonmap.String(event, key); text != "" {
			return text
		}
	}
	if eventType(event) != "" && eventType(event) != "message_end" && eventType(event) != "message" {
		return ""
	}
	if message, ok := event["message"].(map[string]any); ok && jsonmap.String(message, "role") == "assistant" {
		if text := jsonmap.String(message, "content"); text != "" {
			return text
		}
		return contentText(message["content"])
	}
	return ""
}

func contentText(value any) string {
	switch content := value.(type) {
	case []any:
		var text strings.Builder
		for _, block := range content {
			blockMap, ok := block.(map[string]any)
			if !ok || jsonmap.String(blockMap, "type") != "text" {
				continue
			}
			if part := jsonmap.String(blockMap, "text"); part != "" {
				text.WriteString(part)
			}
		}
		return text.String()
	default:
		return ""
	}
}
