package plan

import (
	"path/filepath"
	"testing"
)

func TestUILaunchLogPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if got, want := UILaunchLogPath(dir), filepath.Join(dir, "ui-launch.log"); got != want {
		t.Fatalf("UILaunchLogPath = %q, want %q", got, want)
	}
}
