package taodata

import (
	"path/filepath"
	"testing"
)

func TestResolveDataHomePrecedence(t *testing.T) {
	env := map[string]string{"TAO_DATA_HOME": "/custom/tao", "XDG_DATA_HOME": "/xdg", "HOME": "/home/alice"}
	got := ResolveDataHome(func(key string) string { return env[key] })
	if got != filepath.Clean("/custom/tao") {
		t.Fatalf("ResolveDataHome() = %q", got)
	}

	delete(env, "TAO_DATA_HOME")
	got = ResolveDataHome(func(key string) string { return env[key] })
	if got != filepath.Join("/xdg", "tao") {
		t.Fatalf("ResolveDataHome() with XDG = %q", got)
	}

	delete(env, "XDG_DATA_HOME")
	got = ResolveDataHome(func(key string) string { return env[key] })
	if got != filepath.Join("/home/alice", ".local", "share", "tao") {
		t.Fatalf("ResolveDataHome() with HOME = %q", got)
	}
}
