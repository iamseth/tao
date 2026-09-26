package plan

import (
	"strings"
	"testing"
)

func TestLastLinesEdges(t *testing.T) {
	if got := lastLines("", 2); got != "" {
		t.Fatalf("empty lastLines = %q", got)
	}
	if got := lastLines("a\nb\n", 0); got != "a\nb\n" {
		t.Fatalf("nonpositive lastLines = %q", got)
	}
	if got := lastLines("a\nb", 5); got != "a\nb" {
		t.Fatalf("short lastLines = %q", got)
	}
	if got := lastLines("a\nb\nc", 1); !strings.HasSuffix(got, "c\n") {
		t.Fatalf("expected trailing newline in truncated tail, got %q", got)
	}
}
