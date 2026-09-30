package pi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/iamseth/tao/internal/agent/jsonmap"
	"github.com/iamseth/tao/internal/agent/lifecycle"
	"github.com/iamseth/tao/internal/agent/logrecord"
	"github.com/iamseth/tao/internal/agent/streamjson"
)

type command struct {
	ID        string `json:"id,omitempty"`
	Type      string `json:"type"`
	Message   string `json:"message,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	RequestID string `json:"request_id,omitempty"`
	Cancelled bool   `json:"cancelled,omitempty"`
}

type event map[string]any

type readResult struct {
	event event
	err   error
}

func (s *session) readStdout(stdout io.Reader) {
	defer close(s.events)
	if s.readerDone != nil {
		defer close(s.readerDone)
	}
	var parseErr error
	err := streamjson.ReadLines(stdout, func(line int, raw []byte) error {
		var event event
		if err := json.Unmarshal(raw, &event); err != nil {
			parseErr = fmt.Errorf("parse pi rpc jsonl line %d: %w", line, err)
			return parseErr
		}
		select {
		case s.events <- readResult{event: event}:
		case <-s.stopEvents:
		}
		return nil
	})
	if err != nil {
		if parseErr == nil {
			err = fmt.Errorf("read pi rpc stdout: %w", err)
		}
		select {
		case s.events <- readResult{err: err}:
		case <-s.stopEvents:
		}
	}
}

func (s *session) send(ctx context.Context, command command) error {
	_, err := s.sendCommand(ctx, command)
	return err
}

func (s *session) sendPrompt(ctx context.Context, command command) (bool, error) {
	return s.sendCommand(ctx, command)
}

func (s *session) sendCommand(ctx context.Context, command command) (bool, error) {
	select {
	case <-ctx.Done():
		return false, s.abort(ctx.Err())
	default:
	}
	data, err := json.Marshal(command)
	if err != nil {
		return false, err
	}
	done := make(chan error, 1)
	go func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		_, err := s.stdin.Write(append(data, '\n'))
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			return true, fmt.Errorf("send pi rpc %s: %w", command.Type, err)
		}
		return true, nil
	case <-ctx.Done():
		// Abort closes the pipe without acquiring mu if its bounded
		// best-effort command cannot pass the outstanding write.
		err := s.abort(ctx.Err())
		<-done
		return true, err
	}
}

func (s *session) requestMap(ctx context.Context, command command, wantType string) (map[string]any, error) {
	if err := s.send(ctx, command); err != nil {
		return nil, err
	}
	for {
		event, err := s.nextTransport(ctx)
		if err != nil {
			return nil, err
		}
		if err := s.handleUIRequest(ctx, event); err != nil {
			return nil, err
		}
		if err := agentEventError(event); err != nil {
			s.logPiError(err)
			return nil, err
		}
		if jsonmap.EventType(event) == wantType {
			return event, nil
		}
		if jsonmap.EventType(event) != "response" || jsonmap.String(event, "id") != command.ID {
			continue
		}
		success, ok := event["success"].(bool)
		if !ok {
			return nil, fmt.Errorf("pi rpc %s response omitted success", command.Type)
		}
		if !success {
			return nil, s.responseError(event)
		}
		if data, ok := event["data"].(map[string]any); ok {
			return data, nil
		}
		return event, nil
	}
}

func (s *session) verifyReadiness(ctx context.Context) error {
	state, err := s.requestMap(ctx, command{ID: readinessStateID, Type: "get_state"}, "state")
	if err != nil {
		return err
	}
	model, ok := state["model"].(map[string]any)
	if !ok {
		return errors.New("pi has no selected model")
	}
	provider := jsonmap.String(model, "provider")
	modelID := jsonmap.String(model, "id")
	if provider == "" || modelID == "" {
		return errors.New("pi selected model is incomplete")
	}

	available, err := s.requestMap(ctx, command{ID: readinessModelsID, Type: "get_available_models"}, "available_models")
	if err != nil {
		return err
	}
	models, ok := available["models"].([]any)
	if !ok {
		return errors.New("pi available-model response omitted models")
	}
	for _, value := range models {
		candidate, ok := value.(map[string]any)
		if ok && jsonmap.String(candidate, "provider") == provider && jsonmap.String(candidate, "id") == modelID {
			return nil
		}
	}
	return fmt.Errorf("pi selected model %s/%s has no local credentials", provider, modelID)
}

func (s *session) waitForPromptResponse(ctx context.Context, id string) (lifecycle.PromptAcceptance, error) {
	for {
		event, err := s.nextTransport(ctx)
		if err != nil {
			return lifecycle.PromptAcceptanceUnknown, err
		}
		if err := s.handleUIRequest(ctx, event); err != nil {
			return lifecycle.PromptAcceptanceUnknown, err
		}
		if jsonmap.EventType(event) == "extension_ui_request" {
			continue
		}
		if jsonmap.EventType(event) != "response" || jsonmap.String(event, "id") != id {
			s.queuedEvents = append(s.queuedEvents, event)
			continue
		}
		if commandName := jsonmap.String(event, "command"); commandName != "prompt" {
			return lifecycle.PromptAcceptanceUnknown, fmt.Errorf("pi rpc prompt response has command %q", commandName)
		}
		success, ok := event["success"].(bool)
		if !ok {
			return lifecycle.PromptAcceptanceUnknown, errors.New("pi rpc prompt response omitted success")
		}
		if !success {
			return lifecycle.PromptAcceptanceRejected, s.responseError(event)
		}
		return lifecycle.PromptAcceptanceAccepted, nil
	}
}

func (s *session) next(ctx context.Context) (event, error) {
	if len(s.queuedEvents) > 0 {
		event := s.queuedEvents[0]
		s.queuedEvents = s.queuedEvents[1:]
		return event, nil
	}
	for {
		if ctx.Err() != nil {
			return nil, s.abort(ctx.Err())
		}
		// Prefer already-observed terminal/error events over an overdue notice.
		select {
		case result, ok := <-s.events:
			return transportResult(result, ok)
		default:
		}
		select {
		case <-ctx.Done():
			return nil, s.abort(ctx.Err())
		case result, ok := <-s.events:
			return transportResult(result, ok)
		case message, ok := <-s.warningMessages:
			s.warningMessages = nil
			if ok && ctx.Err() == nil && message != "" && len(message) <= 16*1024 {
				s.startWarning(ctx, message)
			}
		}
	}
}

// The ID is unique among commands in this fresh, single-operation process.
const warningID = "tao-session-warning"

func (s *session) warningResponse(event event) bool {
	return s.warningSent && jsonmap.EventType(event) == "response" &&
		jsonmap.String(event, "id") == warningID && jsonmap.String(event, "command") == "steer"
}

func (s *session) startWarning(ctx context.Context, message string) {
	s.warningSent = true
	s.warningWriteDone = make(chan struct{})
	written := make(chan struct{})
	s.warningWritten = written
	started := make(chan struct{})
	go func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		defer close(written)
		close(started)
		if ctx.Err() != nil {
			return
		}
		data, _ := json.Marshal(command{ID: warningID, Type: "steer", Message: message})
		_, _ = s.stdin.Write(append(data, '\n'))
	}()
	go func() {
		defer close(s.warningWriteDone)
		// Backpressure is not a session failure. Keep the serialized write
		// alive until it drains, cancellation, or finishWarning's terminal
		// cleanup; a warning-local timeout must not close shared stdin.
		select {
		case <-written:
		case <-ctx.Done():
			_ = s.stdin.Close()
			<-written
		}
	}()
	<-started
}

func (s *session) finishWarning() {
	s.warningMessages = nil
	if s.warningWriteDone != nil {
		select {
		case <-s.warningWritten:
		default:
			_ = s.stdin.Close()
		}
		<-s.warningWriteDone
	}
}

func transportResult(result readResult, ok bool) (event, error) {
	if !ok {
		return nil, errors.New("pi rpc stdout closed before agent completion")
	}
	return result.event, result.err
}

func (s *session) nextTransport(ctx context.Context) (event, error) {
	select {
	case <-ctx.Done():
		return nil, s.abort(ctx.Err())
	case result, ok := <-s.events:
		if !ok {
			return nil, errors.New("pi rpc stdout closed before agent completion")
		}
		if result.err != nil {
			return nil, result.err
		}
		return result.event, nil
	}
}

func (s *session) handleResponseError(event event) error {
	if jsonmap.EventType(event) != "response" {
		return nil
	}
	if success, ok := event["success"].(bool); !ok || success {
		return nil
	}
	return s.responseError(event)
}

func (s *session) responseError(event event) error {
	message := jsonmap.String(event, "error")
	if message == "" {
		message = "pi rpc command failed"
	}
	if command := jsonmap.String(event, "command"); command != "" {
		return fmt.Errorf("pi rpc %s: %s", command, message)
	}
	return errors.New(message)
}

func (s *session) handleUIRequest(ctx context.Context, event event) error {
	if jsonmap.EventType(event) != "extension_ui_request" {
		return nil
	}
	requestID := jsonmap.String(event, "request_id")
	if requestID == "" {
		requestID = jsonmap.String(event, "id")
	}
	if s.log != nil {
		_ = logrecord.Write(s.log, logrecord.Record{Type: logrecord.TypeDiagnostic, Content: fmt.Sprintf("tao pi warning: cancelled unsupported UI request %q", requestID)})
	}
	return s.send(ctx, command{Type: "extension_ui_response", RequestID: requestID, Cancelled: true})
}

func (s *session) abort(cause error) error {
	s.abortOnce.Do(func() {
		// Abort is advisory: a full stdin pipe must never postpone kill/wait.
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = s.sendWithoutContext(command{Type: "abort"})
		}()
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-done:
		case <-timer.C:
		}
		timer.Stop()
		_ = s.proc.Kill()
		_ = s.stdin.Close()
		<-done
		s.stopReadingEvents()
		s.wait()
	})
	var annotated stderrError
	if cause != nil && s.lastStderr != "" && !errors.As(cause, &annotated) {
		return stderrError{cause: cause, line: s.lastStderr}
	}
	return cause
}

// Preserve the original error classification and annotate nested aborts once.
type stderrError struct {
	cause error
	line  string
}

func (e stderrError) Error() string { return fmt.Sprintf("%v; pi stderr: %s", e.cause, e.line) }
func (e stderrError) Unwrap() error { return e.cause }

func (s *session) sendWithoutContext(command command) error {
	data, err := json.Marshal(command)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.stdin.Write(append(data, '\n'))
	return err
}

func (s *session) close() {
	_ = s.stdin.Close()
	s.stopReadingEvents()
	s.wait()
	if s.warningWriteDone != nil {
		<-s.warningWriteDone
	}
	if s.readerDone != nil {
		<-s.readerDone
	}
}

func (s *session) stopReadingEvents() {
	s.stopOnce.Do(func() {
		if s.stopEvents != nil {
			close(s.stopEvents)
		}
	})
}

func (s *session) wait() {
	s.waitOnce.Do(func() {
		streamjson.WaitForStderr(s.stderrDone)
		_ = s.proc.Wait()
		if s.stderrDone != nil {
			<-s.stderrDone
		}
	})
}
