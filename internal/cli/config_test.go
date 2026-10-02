package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/internal/settings"
	"github.com/iamseth/tao/internal/taodata"
)

func TestConfigSchemaRoundTrips(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TAO_DATA_HOME", home)
	registry := taodata.NewRegistry(home)
	if err := registry.WriteRepo(taodata.Repo{Schema: taodata.RepoSchema, ID: "repo-a", Name: "example", Root: filepath.Join(home, "missing")}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	app := App{Out: &out, Err: &out, SettingsService: settings.NewService(home, runtimeconfig.LoadEnv(nil))}
	for _, def := range runtimeconfig.SettingDefinitions() {
		for _, scope := range def.Scopes {
			t.Run(scope+"/"+def.Key, func(t *testing.T) {
				value := "2"
				if strings.HasSuffix(def.Key, ".stop") {
					value = "null"
				}
				if def.Kind == "boolean" {
					value = "false"
				}
				if def.Kind == "string" {
					value = "provider/model"
				}
				if def.Key == "session_timeout" {
					value = "5m"
				}
				if len(def.Choices) > 0 {
					value = def.Choices[0]
				}
				selector := []string{"--global"}
				if scope == "repo" {
					selector = []string{"--repo", "repo-a"}
				}
				for _, args := range [][]string{{"set", def.Key, value}, {"get", def.Key}, {"unset", def.Key}, {"unset", def.Key}} {
					out.Reset()
					if err := app.config(context.Background(), append(args, selector...)); err != nil {
						t.Fatalf("%v: %v", args, err)
					}
					if !strings.Contains(out.String(), def.Key+":") {
						t.Fatal(out.String())
					}
				}
			})
		}
	}
}

func TestConfigErrorsRepairAndMasking(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TAO_DATA_HOME", home)
	env := runtimeconfig.LoadEnv(func(key string) (string, bool) { return "true", key == "TAO_PULL_REQUEST" })
	var out bytes.Buffer
	app := App{Out: &out, Err: &out, SettingsService: settings.NewService(home, env)}
	ctx := context.Background()
	for _, args := range [][]string{{"set", "unknown", "x", "--global"}, {"set", "pull_request", "null", "--global"}, {"set", "pull_request", "wrong", "--global"}, {"--global", "--repo", "a"}, {"--repo", ""}, {"--repo", "missing"}} {
		if err := app.config(ctx, args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "config.json")); !os.IsNotExist(err) {
		t.Fatalf("invalid writes created file: %v", err)
	}
	if err := app.config(ctx, []string{"set", "pull_request", "false", "--global"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "masked by environment") {
		t.Fatal(out.String())
	}
	path := filepath.Join(home, "config.json")
	for _, content := range []string{`{"schema":"tao.config.v1","settings":{"pull_request":"bad"}}`, `{`} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		out.Reset()
		if err := app.config(ctx, []string{"get", "--global"}); err == nil {
			t.Fatalf("invalid file accepted: %s", content)
		}
		if !strings.Contains(out.String(), "Diagnostic:") {
			t.Fatal(out.String())
		}
	}
	if err := app.config(ctx, []string{"unset", "pull_request", "--global"}); err == nil || !strings.Contains(err.Error(), "repair the file manually") {
		t.Fatalf("malformed repair: %v", err)
	}
}

func TestConfigRepositoryRepairAndLegacyAtomicity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TAO_DATA_HOME", home)
	registry := taodata.NewRegistry(home)
	repo := taodata.Repo{Schema: taodata.RepoSchema, ID: "repo-a", Name: "example", Root: filepath.Join(home, "missing")}
	if err := registry.WriteRepo(repo); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "repos", repo.ID, "repo.json")
	original, err := os.ReadFile(path) //nolint:gosec // Test-owned repository metadata under t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	invalid := bytes.Replace(original, []byte(`"schema":`), []byte(`"run_defaults":{"pull_request":"bad","models":{"model":"bad model"}},"schema":`), 1)
	if err := os.WriteFile(path, invalid, 0600); err != nil { //nolint:gosec // Test-owned repository metadata under t.TempDir.
		t.Fatal(err)
	}
	var out bytes.Buffer
	app := App{Out: &out, Err: &out, SettingsService: settings.NewService(home, runtimeconfig.LoadEnv(nil))}
	ctx := context.Background()
	if err := app.repoDoctor(ctx, registry); err == nil || !strings.Contains(out.String(), "Settings diagnostic:") {
		t.Fatalf("doctor: %v, %s", err, &out)
	}
	out.Reset()
	if err := app.repoConfig(ctx, registry, []string{"--pull-request", "false", "--model", "bad model", repo.ID}); err == nil {
		t.Fatal("accepted invalid transaction")
	}
	unchanged, err := os.ReadFile(path) //nolint:gosec // Test-owned repository metadata under t.TempDir.
	if err != nil || !bytes.Equal(unchanged, invalid) {
		t.Fatal("partial write", err)
	}
	if err := app.repoConfig(ctx, registry, []string{"--pull-request", "false", "--model", "provider/model", repo.ID}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "pull_request: false") {
		t.Fatal(out.String())
	}
	for _, args := range [][]string{{"set", "theme", "default", "--repo", repo.ID}, {"get", "theme", "--repo", repo.ID}, {"unset", "theme", "--repo", repo.ID}} {
		if err := app.config(ctx, args); err == nil {
			t.Fatalf("accepted wrong scope: %v", args)
		}
	}
	if err := registry.WriteRepo(taodata.Repo{Schema: taodata.RepoSchema, ID: "repo-b", Name: "example"}); err != nil {
		t.Fatal(err)
	}
	if err := app.config(ctx, []string{"--repo", "repo-"}); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous selector: %v", err)
	}
}

func TestConfigUnavailablePath(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, "config.json"), 0700); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	app := App{Out: &out, SettingsService: settings.NewService(home, runtimeconfig.LoadEnv(nil))}
	for _, args := range [][]string{{"get", "--global"}, {"set", "pull_request", "false", "--global"}} {
		if err := app.config(context.Background(), args); err == nil {
			t.Fatalf("accepted unavailable path: %v", args)
		}
	}
}

func TestConfigGlobalRoundTrip(t *testing.T) {
	t.Setenv("TAO_DATA_HOME", t.TempDir())
	t.Setenv("TAO_UPDATE", "off")
	var out bytes.Buffer
	app := App{Out: &out, Err: &out}
	for _, args := range [][]string{{"config", "set", "pull_request", "false", "--global"}, {"config", "get", "pull_request", "--global"}, {"config", "unset", "pull_request", "--global"}} {
		if err := app.Run(context.Background(), args); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	if !strings.Contains(out.String(), "stored=false") {
		t.Fatalf("missing stored false: %s", &out)
	}
}
