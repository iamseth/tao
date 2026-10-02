package settings

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/runtimeconfig"
)

func text(s string) *string { return &s }

func TestServiceRelativeHomeIsFrozen(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	s := NewService("new-home", runtimeconfig.LoadEnv(nil))
	t.Chdir(t.TempDir())
	if err := s.Update(context.Background(), Target{Global: true}, map[string]*string{"agent": text("claude")}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "new-home", "config.json")); err != nil {
		t.Fatal(err)
	}
	view, err := s.Read(context.Background(), Target{Global: true})
	if err != nil || view.Effective.Defaults().Agent != "claude" {
		t.Fatalf("relative read: %+v %v", view, err)
	}
}

func TestServiceEarlyFailuresPreserveEnvironment(t *testing.T) {
	env := runtimeconfig.LoadEnv(func(key string) (string, bool) {
		values := map[string]string{runtimeconfig.EnvAgent: "claude", runtimeconfig.EnvSessionTimeout: "invalid"}
		value, ok := values[key]
		return value, ok
	})
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name   string
		home   string
		ctx    context.Context
		target Target
	}{
		{"empty home", "", context.Background(), Target{Global: true}},
		{"invalid target", t.TempDir(), context.Background(), Target{RepositoryID: "../bad"}},
		{"canceled", t.TempDir(), canceled, Target{Global: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view, err := NewService(tc.home, env).Read(tc.ctx, tc.target)
			if err == nil || view.LoadError == nil || len(view.Diagnostics) == 0 {
				t.Fatalf("missing structural diagnostics: %+v %v", view, err)
			}
			if view.Effective.Defaults().Agent != "claude" || view.Effective.Require(runtimeconfig.EnvSessionTimeout) == nil {
				t.Fatal("captured environment lost")
			}
		})
	}
}

func TestReadWithGlobalFreezesFilesAndRejectsRepositoryScope(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	globalPath := filepath.Join(home, "config.json")
	put(t, globalPath, `{"schema":"tao.config.v1","settings":{"theme":"gruvbox","agent":"invalid"}}`)
	put(t, filepath.Join(home, "repos", "repo", "repo.json"), `{"schema":"tao.repo.v1","id":"repo","name":"repo","root":"/repo","run_defaults":{"theme":"tokyonight","update":"off"}}`)
	s := NewService(home, runtimeconfig.LoadEnv(nil))
	global, err := s.Read(ctx, Target{Global: true})
	if err == nil || global.LoadError != nil {
		t.Fatalf("field error misclassified: %v / %v", err, global.LoadError)
	}
	put(t, globalPath, `{`)
	repo, err := s.ReadWithGlobal(ctx, Target{RepositoryID: "repo"}, global)
	if err == nil || repo.LoadError != nil {
		t.Fatalf("frozen read: %v / %v", err, repo.LoadError)
	}
	if repo.Effective.Defaults().Theme.Name() != "gruvbox" {
		t.Fatal("repository changed global-only theme")
	}
	for _, key := range []string{"theme", "update"} {
		found := false
		for _, row := range repo.Effective.SettingsStatus() {
			if row.Key == key && row.Warning != "" {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing scope rejection for %s", key)
		}
	}
	broken, err := s.Read(ctx, Target{Global: true})
	if err == nil || broken.LoadError == nil {
		t.Fatal("structural failure not classified")
	}
}

func TestServiceInheritanceRepair(t *testing.T) {
	home := t.TempDir()
	s := NewService(home, runtimeconfig.LoadEnv(nil))
	target := Target{Global: true}
	v, err := s.Read(context.Background(), target)
	if err != nil || len(v.Stored) != 0 {
		t.Fatalf("absent: %+v %v", v, err)
	}
	path := filepath.Join(home, "config.json")
	if err := os.WriteFile(path, []byte(`{"schema":"tao.config.v1","metadata":{"keep":true},"settings":{"agent":"bad","pull_request":"bad","future":{"x":1}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	v, err = s.Read(context.Background(), target)
	if err == nil || len(v.Diagnostics) < 2 || string(v.Stored["agent"]) != `"bad"` {
		t.Fatalf("invalid read: %+v %v", v, err)
	}
	if v.Effective.Require(runtimeconfig.EnvAgent) == nil {
		t.Fatal("invalid stored agent silently became an effective default")
	}
	if err := s.Update(context.Background(), target, map[string]*string{"agent": text("pi")}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path) //nolint:gosec // test-owned temporary data home
	if !strings.Contains(string(data), `"future"`) || !strings.Contains(string(data), `"metadata"`) {
		t.Fatal(string(data))
	}
	if err := s.Update(context.Background(), target, map[string]*string{"pull_request": nil, "future": nil}); err != nil {
		t.Fatal(err)
	}
	v, err = s.Read(context.Background(), target)
	if err != nil || string(v.Stored["agent"]) != `"pi"` {
		t.Fatalf("repair: %+v %v", v, err)
	}
}

func TestServiceComposesLayersAndDefersBudgetAdmission(t *testing.T) {
	home := t.TempDir()
	put(t, filepath.Join(home, "repos", "repo", "repo.json"), `{"schema":"tao.repo.v1","id":"repo","name":"repo","root":"/missing","run_defaults":{"pull_request":false}}`)
	env := map[string]string{runtimeconfig.EnvAgent: "claude"}
	captured := runtimeconfig.LoadEnv(func(key string) (string, bool) { value, ok := env[key]; return value, ok })
	s := NewService(home, captured)
	ctx := context.Background()
	env[runtimeconfig.EnvAgent] = "bad"
	changes := map[string]*string{"agent": text("pi"), "pull_request": text("true"), "budget.slice.cost.warn": text("10"), "budget.slice.cost.stop": text("5")}
	if err := s.Update(ctx, Target{Global: true}, changes); err != nil {
		t.Fatal(err)
	}
	global, err := s.Read(ctx, Target{Global: true})
	if err == nil || len(global.Diagnostics) == 0 {
		t.Fatal("missing resolved budget diagnostic")
	}
	if _, err := global.Effective.Budget(); err == nil {
		t.Fatal("invalid resolved budget admitted")
	}
	if err := s.Update(ctx, Target{RepositoryID: "repo"}, map[string]*string{"budget.slice.cost.stop": text("null")}); err != nil {
		t.Fatal(err)
	}
	repo, err := s.Read(ctx, Target{RepositoryID: "repo"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Effective.Budget(); err != nil {
		t.Fatal(err)
	}
	for _, status := range repo.Effective.SettingsStatus() {
		if status.Key == "agent" && (status.Value != "claude" || status.Source != "env" || status.GlobalValue != "pi") {
			t.Fatalf("captured environment: %+v", status)
		}
		if status.Key == "pull_request" && (status.Value != "false" || status.Source != "repository") {
			t.Fatalf("repository precedence: %+v", status)
		}
	}
	// Complete change maps are validated before writing any member.
	path := filepath.Join(home, "config.json")
	before, _ := os.ReadFile(path) //nolint:gosec // test-owned temporary data home
	if err := s.Update(ctx, Target{Global: true}, map[string]*string{"agent": text("claude"), "max_slices": text("-1")}); err == nil {
		t.Fatal("invalid multi-key update accepted")
	}
	after, _ := os.ReadFile(path) //nolint:gosec // test-owned temporary data home
	if string(before) != string(after) {
		t.Fatal("partial update persisted")
	}
	if err := s.Update(ctx, Target{Global: true}, map[string]*string{"budget.slice.cost.warn": text("2"), "budget.slice.cost.stop": text("3")}); err != nil {
		t.Fatal(err)
	}
	global, err = s.Read(ctx, Target{Global: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := global.Effective.Budget(); err != nil {
		t.Fatal(err)
	}
}
