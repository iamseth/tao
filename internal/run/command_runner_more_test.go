package run

import (
	"bytes"
	"context"
	"testing"
)

func TestDefaultCommandRunnerRunsCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := defaultCommandRunner(context.Background(), "", "true", nil, &stdout, &stderr); err != nil {
		t.Fatalf("true command failed: %v", err)
	}
}
