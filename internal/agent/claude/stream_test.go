package claude

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/agent/logrecord"
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
	for _, want := range []string{`"type":"tool_call"`, `"name":"Bash"`, "go test ./...", `"type":"tool_result"`, "ok ./...", `"type":"assistant","content":"finished"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in log, got %q", want, text)
		}
	}
}

func TestStreamPairsParallelToolResultsByToolUseID(t *testing.T) {
	proc := newFakeProcess(t)
	go func() {
		defer proc.finish(nil)
		_, _ = proc.readPrompt()
		proc.writeEvent(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"bash-1","name":"Bash","input":{"command":"missing-cli --version"}},{"type":"tool_use","id":"read-1","name":"Read","input":{"file_path":"fixture.txt"}}]}}`)
		proc.writeEvent(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"read-1","content":"copied fixture: missing-cli: command not found","is_error":false},{"type":"tool_result","tool_use_id":"bash-1","content":"bash: missing-cli: command not found","is_error":true}]}}`)
		proc.writeEvent(`{"type":"result","subtype":"success"}`)
	}()
	var log bytes.Buffer
	client := Client{Log: &log, ProcessStarter: func(ctx context.Context, cwd string, name string, args []string) (process.Process, error) {
		return proc, nil
	}}

	if _, err := client.RunAgentSession(context.Background(), Request{Prompt: "work"}); err != nil {
		t.Fatal(err)
	}
	var records []logrecord.Record
	for line := range strings.Lines(log.String()) {
		record, ok := logrecord.Parse(strings.TrimSuffix(line, "\n"))
		if !ok {
			t.Fatalf("invalid framed record %q", line)
		}
		records = append(records, record)
	}
	if len(records) != 4 {
		t.Fatalf("got records %#v, want two paired calls and results", records)
	}
	for i, want := range []struct {
		typ, name string
		failed    bool
	}{
		{typ: logrecord.TypeToolCall, name: "Read"},
		{typ: logrecord.TypeToolResult, name: "Read"},
		{typ: logrecord.TypeToolCall, name: "Bash"},
		{typ: logrecord.TypeToolResult, name: "Bash", failed: true},
	} {
		if records[i].Type != want.typ || records[i].Name != want.name || records[i].Failed != want.failed {
			t.Fatalf("record %d = %#v, want type %q name %q failed=%t", i, records[i], want.typ, want.name, want.failed)
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
