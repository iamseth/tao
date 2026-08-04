// Package agentinput bounds and validates agent-authored input consumed at
// Tao's trust boundary, such as the temporary notes, reason, and evidence
// files written by implementation agents. Callers own what each input means;
// this package only enforces the shared size and shape bounds described in
// docs/agent-input-trust.md.
package agentinput

import (
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	// MaxFileBytes is the default byte bound for one agent-written input file.
	MaxFileBytes int64 = 64 * 1024
	// MaxTextRunes is the default rune bound for one agent-written text value.
	MaxTextRunes = 16 * 1024
)

// BoundedText trims an agent-written text value and rejects values longer
// than MaxTextRunes with an error naming the bounded input.
func BoundedText(value string, label string) (string, error) {
	value = strings.TrimSpace(value)
	if len([]rune(value)) > MaxTextRunes {
		return "", fmt.Errorf("%s exceeds %d rune limit", label, MaxTextRunes)
	}
	return value, nil
}

// CapRunes truncates a value to at most maxRunes runes.
func CapRunes(value string, maxRunes int) string {
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes])
}

// ReadBoundedFile reads an agent-written input file, rejecting content larger
// than maxBytes with an error naming the bounded input.
func ReadBoundedFile(path string, label string, maxBytes int64) ([]byte, error) {
	file, err := os.Open(path) // #nosec G304 -- explicit local file input selected by the caller.
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%s exceeds %d byte limit", label, maxBytes)
	}
	return data, nil
}
