package agent

import (
	"errors"
	"fmt"
	"testing"
)

type markedTransportError struct {
	err error
}

func (e markedTransportError) Error() string            { return e.err.Error() }
func (e markedTransportError) Unwrap() error            { return e.err }
func (markedTransportError) RetryableTransportFailure() {}

type typedFailure struct {
	message string
}

func (e *typedFailure) Error() string { return e.message }

func TestIsRetryableTransportFailureFindsWrappedMarker(t *testing.T) {
	original := &typedFailure{message: "connection dropped"}
	marked := markedTransportError{err: original}
	err := fmt.Errorf("agent handoff failed: %w", marked)

	if !IsRetryableTransportFailure(err) {
		t.Fatal("expected wrapped marker to be retryable")
	}
	if err.Error() != "agent handoff failed: connection dropped" {
		t.Fatalf("error text = %q", err.Error())
	}
	if !errors.Is(err, original) {
		t.Fatal("expected errors.Is to reach the original error")
	}
	var target *typedFailure
	if !errors.As(err, &target) || target != original {
		t.Fatalf("expected errors.As to reach the original error, got %#v", target)
	}
}

func TestIsRetryableTransportFailureRejectsUnmarkedErrors(t *testing.T) {
	for _, err := range []error{nil, errors.New("WebSocket closed 1006 Connection ended"), fmt.Errorf("wrapped: %w", errors.New("authentication failed"))} {
		if IsRetryableTransportFailure(err) {
			t.Fatalf("expected %v to remain unmarked", err)
		}
	}
}
