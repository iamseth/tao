package claude

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/agent/process"
)

func TestStreamParsesToolLogsAndMissingTelemetry(t *testing.T) {
	proc := newFakeProcess(t)
	go func() {
		defer proc.finish(nil)
		_, _ = proc.readPrompt()
		proc.writeEvent(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}]}}`)
		proc.writeEvent(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"ok ./..."}]}}`)
		proc.writeEvent(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"finished"}]}}`)
		proc.writeEvent(`{"type":"result","subtype":"success"}`)
	}()
	var log bytes.Buffer
	client := Client{Log: &log, ProcessStarter: func(ctx context.Context, cwd string, name string, args []string) (process.Process, error) {
		return proc, nil
	}}

	result, err := client.RunAgentSession(context.Background(), Request{Prompt: "work"})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalText != "finished" || result.SessionID != "" || result.Model != "" || result.Usage != nil || result.CostUSD != 0 {
		t.Fatalf("expected successful result with missing telemetry, got %#v", result)
	}
	text := log.String()
	for _, want := range []string{`→ Bash {"command":"go test ./..."}`, "✓ tool\nok ./...", "assistant: finished"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in log, got %q", want, text)
		}
	}
}

func TestStreamMalformedJSONReturnsClearError(t *testing.T) {
	proc := newFakeProcess(t)
	go func() {
		defer proc.finish(nil)
		_, _ = proc.readPrompt()
		_, _ = proc.stdoutWriter.Write([]byte("not-json\n"))
	}()
	client := Client{ProcessStarter: func(ctx context.Context, cwd string, name string, args []string) (process.Process, error) {
		return proc, nil
	}}

	_, err := client.RunAgentSession(context.Background(), Request{Prompt: "work"})
	if err == nil || !strings.Contains(err.Error(), "parse claude stream-json line 1") {
		t.Fatalf("expected malformed json error, got %v", err)
	}
	if !proc.killed {
		t.Fatal("expected process kill")
	}
}

func TestStreamResultTextUsedWhenNoAssistantText(t *testing.T) {
	proc := newFakeProcess(t)
	go func() {
		defer proc.finish(nil)
		_, _ = proc.readPrompt()
		proc.writeEvent(`{"type":"result","subtype":"success","result":"final answer","session_id":"s1"}`)
	}()
	client := Client{ProcessStarter: func(ctx context.Context, cwd string, name string, args []string) (process.Process, error) {
		return proc, nil
	}}

	result, err := client.RunAgentSession(context.Background(), Request{Prompt: "work"})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalText != "final answer" || result.Output != "final answer" || result.SessionID != "s1" {
		t.Fatalf("unexpected result: %#v", result)
	}
}
