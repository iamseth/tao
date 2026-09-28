package workspace

import (
	"context"
	"io"
	"testing"
)

func TestPrepareDependenciesFallsBackToRunnerErrorWhenStderrEmpty(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/yarn.lock")
	metadata, err := PrepareDependencies(context.Background(), root, DefaultConfig(), func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		return context.Canceled
	}, fixedDependencyClock())
	if err == nil || metadata.Status != "failed" || metadata.FailureReason != context.Canceled.Error() {
		t.Fatalf("expected runner error failure metadata, got %#v err=%v", metadata, err)
	}
}
