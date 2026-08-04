package agentinput

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadBoundedFileEnforcesLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input.txt")
	if err := os.WriteFile(path, []byte("small"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := ReadBoundedFile(path, "test input", 16)
	if err != nil || string(data) != "small" {
		t.Fatalf("ReadBoundedFile = %q, %v", data, err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 17)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadBoundedFile(path, "test input", 16); err == nil || !strings.Contains(err.Error(), "test input exceeds 16 byte limit") {
		t.Fatalf("oversized read error = %v", err)
	}
	if _, err := ReadBoundedFile(filepath.Join(t.TempDir(), "missing"), "test input", 16); err == nil {
		t.Fatal("expected missing-file error")
	}
}

func TestBoundedTextTrimsAndBounds(t *testing.T) {
	value, err := BoundedText("  trimmed value \n", "test text")
	if err != nil || value != "trimmed value" {
		t.Fatalf("BoundedText = %q, %v", value, err)
	}
	if _, err := BoundedText(strings.Repeat("x", MaxTextRunes+1), "test text"); err == nil || !strings.Contains(err.Error(), "rune limit") {
		t.Fatalf("oversized text error = %v", err)
	}
}

func TestCapRunesTruncatesByRune(t *testing.T) {
	if got := CapRunes("héllo", 10); got != "héllo" {
		t.Fatalf("under-limit value changed: %q", got)
	}
	if got := CapRunes("héllo", 2); got != "hé" {
		t.Fatalf("CapRunes = %q, want %q", got, "hé")
	}
}
