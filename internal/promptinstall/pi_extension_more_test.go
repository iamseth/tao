package promptinstall

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPiExtensionSourceHonorsEnvAndValidatesPackage(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "package.json"), []byte(`{"name":"tao"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TAO_PI_EXTENSION_DIR", source)
	got, err := piExtensionSource()
	if err != nil || got != source {
		t.Fatalf("piExtensionSource = %q, %v; want %q", got, err, source)
	}
}

func TestPiExtensionSourceFindsPackageFromWorkingDirectory(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "extensions", "pi")
	if err := os.MkdirAll(source, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "package.json"), []byte(`{"name":"tao"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(filepath.Join(root, "extensions")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	t.Setenv("TAO_PI_EXTENSION_DIR", "")
	got, err := piExtensionSource()
	if err != nil || !samePath(got, source) {
		t.Fatalf("piExtensionSource from wd = %q, %v; want path equivalent to %q", got, err, source)
	}
}

func TestPiExtensionStatusAndInstallHandleSymlinkStates(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "package.json"), []byte(`{"name":"tao"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TAO_PI_EXTENSION_DIR", source)
	target := filepath.Join(t.TempDir(), "agent", "extensions", "tao")

	status, err := piExtensionStatus(target)
	if err != nil || status != "missing" {
		t.Fatalf("missing status = %q, %v", status, err)
	}
	if err := installPiExtension(target, false); err != nil {
		t.Fatal(err)
	}
	status, err = piExtensionStatus(target)
	if err != nil || status != "current" {
		t.Fatalf("current status = %q, %v", status, err)
	}
	if err := installPiExtension(target, false); err != nil {
		t.Fatalf("reinstalling current symlink should be idempotent: %v", err)
	}

	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	wrong := t.TempDir()
	if err := os.Symlink(wrong, target); err != nil {
		t.Fatal(err)
	}
	status, err = piExtensionStatus(target)
	if err != nil || status != "stale" {
		t.Fatalf("stale status = %q, %v", status, err)
	}
	if err := installPiExtension(target, false); err == nil || !strings.Contains(err.Error(), "not the Tao Pi extension") {
		t.Fatalf("expected stale symlink refusal, got %v", err)
	}
	if err := installPiExtension(target, true); err != nil {
		t.Fatal(err)
	}
	link, err := os.Readlink(target)
	if err != nil || !samePath(link, source) {
		t.Fatalf("forced install target = %q, %v; want %q", link, err, source)
	}

	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("not a symlink"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, err = piExtensionStatus(target)
	if err != nil || status != "unmanaged" {
		t.Fatalf("unmanaged status = %q, %v", status, err)
	}
	if err := installPiExtension(target, false); err == nil || !strings.Contains(err.Error(), "not tao-managed") {
		t.Fatalf("expected unmanaged refusal, got %v", err)
	}
}
