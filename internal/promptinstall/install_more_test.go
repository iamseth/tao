package promptinstall

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/runtimeconfig"
)

func TestDirRejectsUnsupportedAgent(t *testing.T) {
	if _, err := Dir(runtimeconfig.AgentKind("robot")); err == nil || !strings.Contains(err.Error(), "unsupported agent") {
		t.Fatalf("expected unsupported agent error, got %v", err)
	}
}

func TestRemoveManagedOnlyRemovesMatchingMarker(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		removed bool
	}{
		{name: "matching marker", content: "frontmatter\n<!-- tao-managed: web-slice v1 -->\nbody", removed: true},
		{name: "matching CRLF marker", content: "frontmatter\r\n<!-- tao-managed: web-slice v1 -->\r\nbody", removed: true},
		{name: "different prompt marker", content: "<!-- tao-managed: run v1 -->\nuser content"},
		{name: "unmanaged user file", content: "custom web-slice command"},
		{name: "marker-like user text", content: "mentions <!-- tao-managed: web-slice v1 --> inline"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "web-slice.md")
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			removed, err := removeManaged(path, "web-slice")
			if err != nil {
				t.Fatal(err)
			}
			if removed != tc.removed {
				t.Fatalf("removeManaged() = %t, want %t", removed, tc.removed)
			}
			_, statErr := os.Stat(path)
			if tc.removed && !os.IsNotExist(statErr) {
				t.Fatalf("expected managed file removed, stat error = %v", statErr)
			}
			if !tc.removed && statErr != nil {
				t.Fatalf("expected user file preserved, stat error = %v", statErr)
			}
		})
	}
}

func TestInstallAndStatusHandleManagedAndUnmanagedFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "commands", "run.md")
	if status, err := Status(path, "want"); err != nil || status != "missing" {
		t.Fatalf("missing Status = %q, %v", status, err)
	}
	if err := Install(path, "<!-- tao-managed: run v1 -->\nwant", false); err != nil {
		t.Fatal(err)
	}
	if status, err := Status(path, "<!-- tao-managed: run v1 -->\nwant"); err != nil || status != "current" {
		t.Fatalf("current Status = %q, %v", status, err)
	}
	if err := Install(path, "<!-- tao-managed: run v1 -->\nwant", false); err != nil {
		t.Fatalf("same-content install should be idempotent: %v", err)
	}
	if err := os.WriteFile(path, []byte("user content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if status, err := Status(path, "want"); err != nil || status != "unmanaged" {
		t.Fatalf("unmanaged Status = %q, %v", status, err)
	}
	if err := Install(path, "want", false); err == nil || !strings.Contains(err.Error(), "not tao-managed") {
		t.Fatalf("expected unmanaged install refusal, got %v", err)
	}
	if err := Install(path, "<!-- tao-managed: run v1 -->\nnew", true); err != nil {
		t.Fatal(err)
	}
	if status, err := Status(path, "want"); err != nil || status != "stale" {
		t.Fatalf("stale Status = %q, %v", status, err)
	}
}
