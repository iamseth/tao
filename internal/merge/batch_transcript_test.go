package merge

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/agent/logrecord"
)

func TestBatchTranscriptWriterLazyAppendAndClose(t *testing.T) {
	store := newTestBatchStore(t)
	var out bytes.Buffer
	at := time.Date(2026, 9, 26, 12, 0, 0, 123, time.UTC)
	writer := NewBatchTranscriptWriter(store, &out, func() time.Time { return at })
	if n, err := io.WriteString(writer, "before batch\n"); n != 13 || err != nil || out.String() != "before batch\n" {
		t.Fatalf("passthrough = %d, %v, %q", n, err, out.String())
	}
	if _, err := os.Stat(store.TranscriptPath("batch-a")); !os.IsNotExist(err) {
		t.Fatalf("opened before active batch: %v", err)
	}
	if _, err := store.Initialize(BatchState{ID: "batch-a", Status: BatchStatusPlanned}, at.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	path := store.TranscriptPath("batch-a")
	if path != filepath.Join(store.batchesDir, "batch-a", "agent-transcript.log") {
		t.Fatalf("path = %s", path)
	}
	// A resumed invocation appends rather than truncates prior audit output.
	if err := os.WriteFile(path, []byte("prior\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	for range 2 {
		if err := logrecord.Write(writer, logrecord.Record{Type: logrecord.TypeAssistant, Content: "working"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	data, err := os.ReadFile(path) //nolint:gosec // Test-owned path beneath t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 4 || lines[0] != "prior" {
		t.Fatalf("transcript = %s", data)
	}
	for i, line := range lines[1:] {
		record, ok := logrecord.Parse(line)
		if !ok || record.Timestamp != at.Format(time.RFC3339Nano) {
			t.Fatalf("record = %#v", record)
		}
		if i == 0 {
			if record.Type != logrecord.TypeSession || !strings.Contains(record.Content, "batch-a") {
				t.Fatalf("session = %#v", record)
			}
		} else if record.Type != logrecord.TypeAssistant || record.Content != "working" {
			t.Fatalf("record = %#v", record)
		}
	}
	if strings.Count(out.String(), "assistant: working\n") != 2 || !strings.Contains(out.String(), "batch-a") || strings.Contains(out.String(), logrecord.Prefix) {
		t.Fatalf("out = %q", out.String())
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", info.Mode())
	}
	// Closing is terminal: later writes must not reopen the audit file.
	if _, err := io.WriteString(writer, "after close\n"); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path) //nolint:gosec // Test-owned path beneath t.TempDir.
	if err != nil || !bytes.Equal(data, after) {
		t.Fatalf("closed transcript changed: %q, %v", after, err)
	}
}

func TestBatchTranscriptWriterOpenFailureIsBestEffortWithoutRetry(t *testing.T) {
	store := newTestBatchStore(t)
	if _, err := store.Initialize(BatchState{ID: "batch-a", Status: BatchStatusPlanned}, "now"); err != nil {
		t.Fatal(err)
	}
	path := store.TranscriptPath("batch-a")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	writer := NewBatchTranscriptWriter(store, &out, nil)
	if _, err := io.WriteString(writer, "first\n"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(writer, "second\n"); err != nil {
		t.Fatal(err)
	}
	if out.String() != "first\nsecond\n" {
		t.Fatalf("out = %q", out.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("retried open: %v", err)
	}
	if err := writer.Close(); err == nil {
		t.Fatal("open error not retained")
	}
}

func TestBatchTranscriptWriterMissingDirectoryAndWriteFailure(t *testing.T) {
	store := newTestBatchStore(t)
	if _, err := store.Initialize(BatchState{ID: "batch-a", Status: BatchStatusPlanned}, "now"); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Dir(store.TranscriptPath("batch-a"))); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	writer := NewBatchTranscriptWriter(store, &out, nil)
	defer func() { _ = writer.Close() }()
	if err := logrecord.Write(writer, logrecord.Record{Type: logrecord.TypeAssistant, Content: "first"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.TranscriptPath("batch-a")); err != nil {
		t.Fatalf("did not recreate transcript directory: %v", err)
	}
	// Simulate a file failure after opening: output must still reach the terminal.
	if err := writer.file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := logrecord.Write(writer, logrecord.Record{Type: logrecord.TypeAssistant, Content: "second"}); err != nil {
		t.Fatalf("audit failure interrupted stream: %v", err)
	}
	if !strings.Contains(out.String(), "assistant: second\n") {
		t.Fatalf("lost presentation: %q", out.String())
	}
	if err := writer.Close(); err == nil {
		t.Fatal("write failure not retained")
	}
}

func TestBatchTranscriptWriterNilOutput(t *testing.T) {
	writer := NewBatchTranscriptWriter(nil, nil, nil)
	if n, err := io.WriteString(writer, "ignored"); n != 7 || err != nil {
		t.Fatalf("nil output = %d, %v", n, err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	store := newTestBatchStore(t)
	if _, err := store.Initialize(BatchState{ID: "batch-a", Status: BatchStatusPlanned}, "now"); err != nil {
		t.Fatal(err)
	}
	writer = NewBatchTranscriptWriter(store, nil, nil)
	if err := logrecord.Write(writer, logrecord.Record{Type: logrecord.TypeAssistant, Content: "log only"}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(store.TranscriptPath("batch-a"))
	if err != nil || !strings.Contains(string(data), "log only") {
		t.Fatalf("log = %q, %v", data, err)
	}
}
