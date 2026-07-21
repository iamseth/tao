package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunReturnsExitCodes(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"help"}, strings.NewReader(""), &out, &errOut); code != 0 {
		t.Fatalf("help exit code = %d, stderr=%q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "Usage:") {
		t.Fatalf("expected help output, got %q", out.String())
	}
	out.Reset()
	errOut.Reset()
	if code := run(context.Background(), []string{"does-not-exist"}, strings.NewReader(""), &out, &errOut); code != 1 {
		t.Fatalf("bad command exit code = %d", code)
	}
	if !strings.Contains(errOut.String(), "unknown command") {
		t.Fatalf("expected error output, got %q", errOut.String())
	}
}
