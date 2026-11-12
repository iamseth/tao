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
	"time"

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
	lastStderr             string // Read only after stderrDone closes.
	mu                     sync.Mutex
	pendingToolCalls       map[string]toolCall
	pendingToolCallsByName map[string]toolCall
	loggedToolResults      map[string]bool
	watchdog               noProgressWatchdog
	abortOnce              sync.Once
	waitOnce               sync.Once
	stopEvents             chan struct{}
	readerDone             chan struct{}
	stopOnce               sync.Once
	warningMessages        <-chan string
	warningSent            bool
	warningWriteDone       chan struct{}
	warningWritten         chan struct{}
	warningQueuePending    bool
	settledGrace           time.Duration
	// Last observed assistant message_end, recorded for diagnostics and for
	// later classification of a settled run; never authority on its own.
	lastAssistantStop      string
	lastAssistantHasText   bool
	lastAssistantToolCalls int
	assistantTurnObserved  bool
}

func newSession(proc Process, log io.Writer, noProgressToolLimit int, verificationCommands []string, settledGrace time.Duration) *session {
	s := &session{
		proc:                   proc,
		stdin:                  proc.Stdin(),
		settledGrace:           settledGrace,
		events:                 make(chan readResult),
		stopEvents:             make(chan struct{}),
		readerDone:             make(chan struct{}),
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
			s.lastStderr = drainStderr(stderr, log)
		}()
	}
	return s
}

func drainStderr(stderr io.Reader, log io.Writer) string {
	var last string
	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) != "" {
			last = line
		}
		if log != nil {
			_ = logrecord.Write(log, logrecord.Record{Type: logrecord.TypeDiagnostic, Content: fmt.Sprintf("tao pi stderr: %s", line)})
		}
	}
	if err := scanner.Err(); err != nil {
		if log != nil {
			_ = logrecord.Write(log, logrecord.Record{Type: logrecord.TypeDiagnostic, Content: fmt.Sprintf("tao pi stderr: %v", err)})
		}
		// Keep draining if a line exceeds the scanner's bounded buffer.
		_, _ = io.Copy(io.Discard, stderr)
	}
	return last
}

func (s *session) queuedResult() Result {
	var result Result
	for _, event := range s.queuedEvents {
		collectMessageMetrics(event, &result)
		s.observeAgentEvent(event)
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
		if jsonmap.EventType(event) == "agent_end" {
			result.SessionID = jsonmap.String(event, "session_id")
			result.State = event
		}
	}
	s.queuedEvents = nil
	return result
}

// waitForAgentEnd returns once the agent run has settled. Pi emits agent_end
// after every low-level run, including one it will retry or compact away, and
// agent_settled only after that recovery finishes. A terminal agent_end
// (willRetry false, no queued steer) therefore opens a bounded settling wait:
// agent_settled or a cleanly closed stdout ends it as success, a further
// terminal agent_end restarts the grace, and expiry returns the agent_end
// result so peers that never send agent_settled still terminate. A new
// agent_start cancels the grace until that run's terminal agent_end. Compaction
// suspends the grace until compaction_end reports that no retry is pending;
// the session context continues to bound recovery.
func (s *session) waitForAgentEnd(ctx context.Context) (Result, error) {
	defer s.finishWarning()
	var result Result
	var settling *time.Timer
	var settlingSuspended bool
	defer func() {
		if settling != nil {
			settling.Stop()
		}
	}()
	for {
		var deadline <-chan time.Time
		if settling != nil {
			deadline = settling.C
		}
		event, expired, closed, err := s.nextSettling(ctx, deadline)
		if expired {
			s.logDiagnostic(fmt.Sprintf("tao pi: no agent_settled within %s of agent_end; treating the run as settled", s.settledGrace))
			return s.settled(result)
		}
		if closed {
			s.logDiagnostic("tao pi: stdout closed after agent_end; treating the run as settled")
			return s.settled(result)
		}
		if err != nil {
			return Result{Metrics: result.Metrics}, err
		}
		collectMessageMetrics(event, &result)
		if s.warningResponse(event) {
			// The response proves delivery, but the writer may not yet have
			// returned. Join it before terminal cleanup can close shared stdin.
			select {
			case <-s.warningWritten:
			case <-ctx.Done():
				return Result{Metrics: result.Metrics}, ctx.Err()
			}
			if _, ok := event["success"].(bool); !ok {
				return Result{Metrics: result.Metrics}, fmt.Errorf("pi rpc steer response omitted success")
			}
			continue
		}
		if s.warningSent && jsonmap.EventType(event) == "queue_update" {
			if steering, ok := event["steering"].([]any); ok {
				s.warningQueuePending = len(steering) > 0
			}
		}
		if err := s.handleResponseError(event); err != nil {
			return Result{Metrics: result.Metrics}, err
		}
		if err := s.handleUIRequest(ctx, event); err != nil {
			return Result{Metrics: result.Metrics}, err
		}
		s.observeAgentEvent(event)
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
		switch jsonmap.EventType(event) {
		case "agent_start":
			// Extensions and queued messages can continue after a terminal end.
			// The previous run's grace must not settle active work.
			settlingSuspended = false
			if settling != nil {
				settling.Stop()
				settling = nil
			}
		case "compaction_start":
			// Summarization can outlast the legacy-peer settling grace.
			if settling != nil {
				settlingSuspended = true
				settling.Stop()
				settling = nil
			}
		case "compaction_end":
			// Pre-turn compaction must not open a wait before agent_end.
			if settlingSuspended && !boolField(event, "willRetry", "will_retry") && !s.warningQueuePending {
				if settling == nil {
					settling = time.NewTimer(s.settledGrace)
				} else {
					settling.Reset(s.settledGrace)
				}
			}
			settlingSuspended = false
		case "agent_end":
			settlingSuspended = false
			result.SessionID = jsonmap.String(event, "session_id")
			result.State = event
			// Pi may still retry this run, and a queued steer can start another
			// low-level run after agent_end. Neither is terminal; otherwise open
			// or restart the bounded settling wait.
			if boolField(event, "willRetry", "will_retry") || s.warningQueuePending {
				if settling != nil {
					settling.Stop()
					settling = nil
				}
				continue
			}
			if settling == nil {
				settling = time.NewTimer(s.settledGrace)
			} else {
				settling.Stop()
				settling.Reset(s.settledGrace)
			}
		case "agent_settled":
			return s.settled(result)
		}
	}
}

// settled finalizes a settled run: the Result keeps its metrics, session ID,
// and state, and the returned error is the typed outcome of the final
// assistant turn, if any. The classification is a session outcome only, never
// lifecycle or recovery authority.
func (s *session) settled(result Result) (Result, error) {
	err := s.settledOutcomeError()
	if err != nil {
		s.logPiError(err)
	}
	return result, err
}

// settledOutcomeError classifies the last observed assistant turn of a settled
// run. A turn that stopped with length or aborted, or that produced neither
// text nor tool calls, means Pi settled without usable output; this is a
// retryable session failure that reuses the transport-failure marker so the
// run path's existing retry policy applies. Nothing is classified until an
// assistant message_end was observed, so streams that only emit agent_end
// (readiness probes and legacy fakes) still succeed.
func (s *session) settledOutcomeError() error {
	if !s.assistantTurnObserved {
		return nil
	}
	truncated := s.lastAssistantStop == "length" || s.lastAssistantStop == "aborted"
	empty := !s.lastAssistantHasText && s.lastAssistantToolCalls == 0
	if !truncated && !empty {
		return nil
	}
	stop := s.lastAssistantStop
	if stop == "" || stop == "unknown" {
		stop = "stop"
	}
	return retryableTransportError{err: fmt.Errorf("pi agent error: final assistant turn stopped with %s and produced no usable output", stop)}
}

// nextSettling is next with an optional settling deadline. Without a deadline
// it is exactly next. With one, it reports expired when the deadline fires
// before another event and closed when stdout ends (a peer that exits cleanly
// after agent_end has nothing more to send). Already-observed events always
// take precedence over the deadline so cancellation, errors, and terminal
// events are never delayed by the grace, and a due warning is never sent
// because a terminal agent_end leaves no run to steer.
func (s *session) nextSettling(ctx context.Context, deadline <-chan time.Time) (ev event, expired bool, closed bool, err error) {
	if deadline == nil || len(s.queuedEvents) > 0 {
		ev, err = s.next(ctx)
		return ev, false, false, err
	}
	for {
		if ctx.Err() != nil {
			return nil, false, false, s.abort(ctx.Err())
		}
		select {
		case result, ok := <-s.events:
			return settlingTransportResult(result, ok)
		default:
		}
		select {
		case <-ctx.Done():
			return nil, false, false, s.abort(ctx.Err())
		case result, ok := <-s.events:
			return settlingTransportResult(result, ok)
		case <-deadline:
			return nil, true, false, nil
		}
	}
}

func settlingTransportResult(result readResult, ok bool) (event, bool, bool, error) {
	if !ok {
		return nil, false, true, nil
	}
	ev, err := transportResult(result, ok)
	return ev, false, false, err
}

// observeAgentEvent records diagnostic facts about assistant turns and run
// boundaries. It writes log records only; nothing here is lifecycle authority.
func (s *session) observeAgentEvent(event event) {
	switch jsonmap.EventType(event) {
	case "message_end":
		message, ok := event["message"].(map[string]any)
		if !ok || jsonmap.String(message, "role") != "assistant" {
			return
		}
		stop := jsonmap.FirstString(message, "stopReason", "stop_reason")
		if stop == "" {
			stop = "unknown"
		}
		text := contentText(message["content"])
		calls := len(toolCalls(message["content"]))
		s.assistantTurnObserved = true
		s.lastAssistantStop = stop
		s.lastAssistantHasText = text != ""
		s.lastAssistantToolCalls = calls
		s.logDiagnostic(fmt.Sprintf("tao pi: assistant message stopped with %s (text=%d chars, tool_calls=%d)", stop, len(text), calls))
	case "agent_end":
		s.logDiagnostic(fmt.Sprintf("tao pi: agent_end (willRetry=%t)", boolField(event, "willRetry", "will_retry")))
	case "agent_settled":
		s.logDiagnostic(fmt.Sprintf("tao pi: agent_settled (aborted=%t)", boolField(event, "aborted")))
	}
}

func (s *session) logDiagnostic(content string) {
	if s.log == nil {
		return
	}
	_ = logrecord.Write(s.log, logrecord.Record{Type: logrecord.TypeDiagnostic, Content: content})
}

func boolField(values map[string]any, keys ...string) bool {
	for _, key := range keys {
		if value, ok := values[key].(bool); ok {
			return value
		}
	}
	return false
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
	if err := agentMapError(event, jsonmap.EventType(event)); err != nil {
		return err
	}
	if message, ok := event["message"].(map[string]any); ok {
		return agentMapError(message, jsonmap.EventType(event))
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

func assistantText(event event) string {
	if role := jsonmap.String(event, "role"); role != "" && role != "assistant" {
		return ""
	}
	for _, key := range []string{"final_text", "text", "content"} {
		if text := jsonmap.String(event, key); text != "" {
			return text
		}
	}
	if jsonmap.EventType(event) != "" && jsonmap.EventType(event) != "message_end" && jsonmap.EventType(event) != "message" {
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
