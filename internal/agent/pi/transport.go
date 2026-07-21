package pi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/iamseth/tao/internal/agent/jsonmap"
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
	reader := bufio.NewReader(stdout)
	line := 0
	for {
		raw, err := reader.ReadBytes('\n')
		if len(raw) > 0 {
			line++
			raw = trimJSONLLineEnding(raw)
			var event event
			if err := json.Unmarshal(raw, &event); err != nil {
				s.events <- readResult{err: fmt.Errorf("parse pi rpc jsonl line %d: %w", line, err)}
				return
			}
			s.events <- readResult{event: event}
		}
		if err == nil {
			continue
		}
		if errors.Is(err, io.EOF) {
			return
		}
		s.events <- readResult{err: fmt.Errorf("read pi rpc stdout: %w", err)}
		return
	}
}

func trimJSONLLineEnding(raw []byte) []byte {
	if len(raw) > 0 && raw[len(raw)-1] == '\n' {
		raw = raw[:len(raw)-1]
	}
	if len(raw) > 0 && raw[len(raw)-1] == '\r' {
		raw = raw[:len(raw)-1]
	}
	return raw
}

func (s *session) send(ctx context.Context, command command) error {
	select {
	case <-ctx.Done():
		return s.abort(ctx.Err())
	default:
	}
	data, err := json.Marshal(command)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.stdin.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("send pi rpc %s: %w", command.Type, err)
	}
	return nil
}

func (s *session) requestMap(ctx context.Context, command command, wantType string) (map[string]any, error) {
	if err := s.send(ctx, command); err != nil {
		return nil, err
	}
	for {
		event, err := s.next(ctx)
		if err != nil {
			return nil, err
		}
		if err := s.handleResponseError(event); err != nil {
			return nil, err
		}
		if err := s.handleUIRequest(ctx, event); err != nil {
			return nil, err
		}
		if err := agentEventError(event); err != nil {
			s.logPiError(err)
			return nil, err
		}
		if eventType(event) == wantType {
			return event, nil
		}
		if eventType(event) == "response" && jsonmap.String(event, "id") == command.ID {
			if data, ok := event["data"].(map[string]any); ok {
				return data, nil
			}
			return event, nil
		}
	}
}

func (s *session) next(ctx context.Context) (event, error) {
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
	if eventType(event) != "response" {
		return nil
	}
	if success, ok := event["success"].(bool); !ok || success {
		return nil
	}
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
	if eventType(event) != "extension_ui_request" {
		return nil
	}
	requestID := jsonmap.String(event, "request_id")
	if requestID == "" {
		requestID = jsonmap.String(event, "id")
	}
	if s.log != nil {
		_, _ = fmt.Fprintf(s.log, "tao pi warning: cancelled unsupported UI request %q\n", requestID)
	}
	return s.send(ctx, command{Type: "extension_ui_response", RequestID: requestID, Cancelled: true})
}

func (s *session) abort(cause error) error {
	s.abortOnce.Do(func() {
		_ = s.sendWithoutContext(command{Type: "abort"})
		_ = s.proc.Kill()
		_ = s.proc.Wait()
	})
	return cause
}

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
	_ = s.proc.Wait()
}
