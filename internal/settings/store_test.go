package settings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/atomicfile"
	"github.com/iamseth/tao/internal/filelock"
	"github.com/iamseth/tao/internal/runtimeconfig"
)

func put(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestUnsafeDocumentsNeverOverwritten(t *testing.T) {
	for _, data := range []string{
		`{"schema":"future","settings":{}}`,
		`{"schema":"tao.config.v1","settings":[]}`,
		`{"schema":"tao.config.v1","settings":{"models":false}}`,
		`{"schema":"tao.config.v1","settings":{"agent":"pi","agent":"claude"}}`,
		`{"schema":"tao.config.v1","settings":{"models.model":"x"}}`,
		`{"schema":"tao.config.v1","settings":{}} {}`,
		`{"schema":`,
		`null`,
		`{"schema":"tao.config.v1"}`,
	} {
		t.Run(data, func(t *testing.T) {
			home := t.TempDir()
			path := filepath.Join(home, "config.json")
			put(t, path, data)
			s := NewService(home, runtimeconfig.LoadEnv(nil))
			v, err := s.Read(context.Background(), Target{Global: true})
			if err == nil || len(v.Diagnostics) == 0 {
				t.Fatalf("read: %+v %v", v, err)
			}
			if err := s.Update(context.Background(), Target{Global: true}, map[string]*string{"agent": text("pi")}); err == nil {
				t.Fatal("accepted unsafe update")
			}
			after, _ := os.ReadFile(path) //nolint:gosec // test-owned temporary data home
			if string(after) != data {
				t.Fatal("file changed")
			}
		})
	}
}

func TestNativeRoundTripsAndScopes(t *testing.T) {
	home := t.TempDir()
	s := NewService(home, runtimeconfig.LoadEnv(nil))
	ctx := context.Background()
	target := Target{Global: true}
	values := map[string]*string{"pull_request": text("false"), "max_slices": text("0"), "models.model": text("test-model"), "budget.slice.cost.stop": text("null"), "budget.slice.cost.warn": text("2.5"), "session_timeout": text("5m")}
	if err := s.Update(ctx, target, values); err != nil {
		t.Fatal(err)
	}
	v, err := s.Read(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"pull_request", "max_slices", "budget.slice.cost.stop", "budget.slice.cost.warn"} {
		if string(v.Stored[key]) != *values[key] {
			t.Fatalf("%s=%s", key, v.Stored[key])
		}
	}
	info, err := os.Stat(filepath.Join(home, "config.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("mode %v %v", info, err)
	}
	if err := s.Update(ctx, target, map[string]*string{"budget.slice.cost.stop": nil}); err != nil {
		t.Fatal(err)
	}
	v, err = s.Read(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := v.Stored["budget.slice.cost.stop"]; ok {
		t.Fatal("unset retained")
	}
	for _, changes := range []map[string]*string{{"agent": text("null")}, {"max_slices": text("-1")}, {"future": text("x")}} {
		if err := s.Update(ctx, target, changes); err == nil {
			t.Fatal("accepted invalid", changes)
		}
	}
}

func TestRepositoryIdentityLegacyAndMetadata(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "repos", "repo-123", "repo.json")
	put(t, path, `{"schema":"tao.repo.v1","id":"repo-123","name":"repo","root":"/missing/checkout","custom":{"keep":[1,2]},"run_defaults":{"pull_request":false,"models":{"model":"base","future":"keep"},"max_slices":3}}`)
	s := NewService(home, runtimeconfig.LoadEnv(nil))
	ctx := context.Background()
	target := Target{RepositoryID: "repo-123"}
	v, err := s.Read(ctx, target)
	if err == nil || string(v.Stored["pull_request"]) != "false" {
		t.Fatalf("%+v %v", v, err)
	}
	if err := s.Update(ctx, target, map[string]*string{"models.model": text("new"), "agent": text("claude")}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path) //nolint:gosec // test-owned temporary data home
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(root["run_defaults"], []byte(`"future":"keep"`)) && !bytes.Contains(root["run_defaults"], []byte(`"future": "keep"`)) {
		t.Fatal(string(data))
	}
	if string(root["custom"]) == "" {
		t.Fatal("metadata lost")
	}
	if err := s.Update(ctx, target, map[string]*string{"theme": text("dark")}); err == nil {
		t.Fatal("repo theme accepted")
	}
	for _, target := range []Target{{RepositoryID: "../repo-123"}, {RepositoryID: "unregistered"}, {Global: true, RepositoryID: "repo-123"}, {}} {
		if err := s.Update(ctx, target, map[string]*string{"agent": text("pi")}); err == nil {
			t.Fatal("invalid target", target)
		}
	}
	put(t, path, `{"schema":"tao.repo.v1","id":"different","name":"repo","root":"/missing"}`)
	if _, err := s.Read(ctx, Target{RepositoryID: "repo-123"}); err == nil {
		t.Fatal("identity mismatch accepted")
	}
}

func TestConcurrentEditsCancellationAndAtomicFailure(t *testing.T) {
	home := t.TempDir()
	s := NewService(home, runtimeconfig.LoadEnv(nil))
	target := Target{Global: true}
	ctx := context.Background()
	var wg sync.WaitGroup
	for key, value := range map[string]string{"agent": "claude", "pull_request": "false", "max_slices": "7", "models.model": "base"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Update(ctx, target, map[string]*string{key: &value}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	v, err := s.Read(ctx, target)
	if err != nil || len(v.Stored) != 4 {
		t.Fatalf("lost edits %+v %v", v, err)
	}
	path := filepath.Join(home, "config.json")
	old, _ := os.ReadFile(path) //nolint:gosec // test-owned temporary data home
	s.write = func(path string, data []byte, opts atomicfile.Options) error {
		opts.Rename = func(string, string) error { return errors.New("injected rename failure") }
		return atomicfile.Write(path, data, opts)
	}
	if err := s.Update(ctx, target, map[string]*string{"agent": text("pi")}); err == nil {
		t.Fatal("write failure ignored")
	}
	after, _ := os.ReadFile(path) //nolint:gosec // test-owned temporary data home
	if !bytes.Equal(old, after) {
		t.Fatal("failed write changed contents")
	}
	lock, err := os.OpenFile(path+".lock", os.O_RDWR, 0600) //nolint:gosec // test-owned temporary lock
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if err := filelock.Lock(lock); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = filelock.Unlock(lock) }()
	canceled, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	if err := s.Update(canceled, target, map[string]*string{"agent": text("pi")}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock cancellation: %v", err)
	}
	if _, err := os.Stat(path + ".lock"); err != nil {
		t.Fatal("live lock removed", err)
	}
	if _, err := s.Read(canceled, target); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestSymlinkRefusal(t *testing.T) {
	home := t.TempDir()
	original := filepath.Join(home, "original")
	put(t, original, `{"schema":"tao.config.v1","settings":{}}`)
	if err := os.Symlink(original, filepath.Join(home, "config.json")); err != nil {
		t.Fatal(err)
	}
	s := NewService(home, runtimeconfig.LoadEnv(nil))
	if err := s.Update(context.Background(), Target{Global: true}, map[string]*string{"agent": text("pi")}); err == nil {
		t.Fatal("symlink followed")
	}
}
