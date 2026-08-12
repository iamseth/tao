// Package logrecord defines the framed records written to agent-run.log.
//
// Every untrusted provider value is JSON-escaped onto one physical line so it
// cannot be mistaken for a record boundary by readers of the mixed historical
// log file.
package logrecord

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const Prefix = "@tao-agent-log-v1 "

const (
	TypeAssistant  = "assistant"
	TypeToolCall   = "tool_call"
	TypeToolResult = "tool_result"
	TypeDiagnostic = "diagnostic"
	TypeSession    = "session"
)

// Record is one unambiguous agent-log record. Payload contains tool-call input;
// Content contains assistant text, tool output, or a diagnostic message.
type Record struct {
	Type      string `json:"type"`
	Name      string `json:"name,omitempty"`
	Payload   string `json:"payload,omitempty"`
	Content   string `json:"content,omitempty"`
	Failed    bool   `json:"failed,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
}

// Write appends one framed record to w.
func Write(w io.Writer, record Record) error {
	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	_, err = w.Write(append(append([]byte(Prefix), encoded...), '\n'))
	return err
}

// PresentationWriter converts framed records into human-readable progress.
// Unframed writes are ignored so provider output cannot bypass record parsing.
func PresentationWriter(out io.Writer) io.Writer {
	if out == nil {
		return io.Discard
	}
	return presentationWriter{out: out}
}

type presentationWriter struct {
	out io.Writer
}

func (w presentationWriter) Write(p []byte) (int, error) {
	record, ok := Parse(strings.TrimSuffix(string(p), "\n"))
	if !ok {
		return len(p), nil
	}
	if err := Render(w.out, record); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Render writes one record in the terminal-oriented presentation format.
// Provider controls are made visible only for writers that opt into terminal
// control sanitization, such as the active pinned run-header path. Other
// writers retain the historical byte-for-byte presentation behavior.
func Render(out io.Writer, record Record) error {
	if sanitizer, ok := out.(interface{ SanitizeTerminalControls() bool }); ok && sanitizer.SanitizeTerminalControls() {
		record.Name = terminalSafe(record.Name)
		record.Payload = terminalSafe(record.Payload)
		record.Content = terminalSafe(record.Content)
		record.Timestamp = terminalSafe(record.Timestamp)
	}
	var err error
	switch record.Type {
	case TypeSession:
		_, err = fmt.Fprintf(out, "\n--- %s %s ---\n", record.Timestamp, record.Content)
	case TypeAssistant:
		_, err = fmt.Fprintf(out, "assistant: %s\n", record.Content)
	case TypeToolCall:
		_, err = fmt.Fprintf(out, "→ %s %s\n", record.Name, record.Payload)
	case TypeToolResult:
		prefix := "✓"
		if record.Failed {
			prefix = "✗"
		}
		if record.Content == "" {
			_, err = fmt.Fprintf(out, "%s %s\n", prefix, record.Name)
		} else {
			_, err = fmt.Fprintf(out, "%s %s\n%s\n", prefix, record.Name, record.Content)
		}
	case TypeDiagnostic:
		_, err = fmt.Fprintln(out, record.Content)
	}
	return err
}

func terminalSafe(value string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < ' ' || r == 0x7f || r >= 0x80 && r <= 0x9f {
			return '\uFFFD'
		}
		return r
	}, value)
}

// Parse accepts only a complete framed record with a known record type.
func Parse(line string) (Record, bool) {
	if len(line) <= len(Prefix) || line[:len(Prefix)] != Prefix {
		return Record{}, false
	}
	var record Record
	if err := json.Unmarshal([]byte(line[len(Prefix):]), &record); err != nil {
		return Record{}, false
	}
	switch record.Type {
	case TypeAssistant, TypeToolCall, TypeToolResult, TypeDiagnostic, TypeSession:
		return record, true
	default:
		return Record{}, false
	}
}
