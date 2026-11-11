package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/commandrunner"
	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/internal/settings"
	"github.com/iamseth/tao/internal/taodata"
	"github.com/iamseth/tao/internal/testenv"
)

func gateEnvApp(t *testing.T, app App, repo taodata.Repo, raw string) App {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, "repos", repo.ID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	values := map[string]any{}
	if raw != "" {
		values["gate_env"] = json.RawMessage(raw)
	}
	data, err := json.Marshal(map[string]any{"schema": "tao.repo.v1", "id": repo.ID, "name": "selected", "root": repo.Root, "run_defaults": values})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "repo.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	otherDir := filepath.Join(home, "repos", "other")
	if err := os.MkdirAll(otherDir, 0700); err != nil {
		t.Fatal(err)
	}
	otherData, err := json.Marshal(map[string]any{"schema": "tao.repo.v1", "id": "other", "name": "other", "root": repo.Root})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(otherDir, "repo.json"), otherData, 0600); err != nil {
		t.Fatal(err)
	}
	app.SettingsService = settings.NewService(home, app.envSnapshot())
	return app.initializeSettings(context.Background())
}

func testGateEnvForeground(t *testing.T, fix bool) {
	t.Helper()
	testenv.SkipIfShort(t)
	{
		for _, raw := range []string{`{"CI":"selected"}`, `{}`, `{"PATH":"bad"}`, `{"CI":""}`, ""} {
			t.Run(raw+map[bool]string{true: "fix", false: "planning"}[fix], func(t *testing.T) {
				app, repo, item := planningFixture(t)
				var launch func(context.Context) error
				var run *func(*exec.Cmd) error
				if fix {
					a, row := planFixFixture(t)
					app = a
					repo = taodata.Repo{ID: row.RepositoryID, Root: row.RepositoryRoot}
					app = gateEnvApp(t, app, repo, raw)
					l := newUIPlanFixLauncher(app, app.In, app.Out)
					run = &l.run
					launch = func(ctx context.Context) error { return l.Launch(ctx, row) }
				} else {
					app = gateEnvApp(t, app, repo, raw)
					l := newUINotePlanningLauncher(app, app.In, app.Out)
					run = &l.run
					launch = func(ctx context.Context) error { return l.Launch(ctx, item) }
				}
				before := os.Environ()
				calls := 0
				*run = func(cmd *exec.Cmd) error {
					calls++
					child := exec.Command("/bin/sh", "-c", `printf '%s' "$CI"`)
					child.Env = cmd.Env
					got, err := child.Output()
					if err != nil {
						return err
					}
					want := os.Getenv("CI")
					if strings.Contains(raw, "selected") {
						want = "selected"
					}
					if raw == `{"CI":""}` {
						want = ""
					}
					if string(got) != want {
						t.Fatalf("CI=%q want %q", got, want)
					}
					return nil
				}
				err := launch(context.Background())
				invalid := strings.Contains(raw, "PATH")
				if (err != nil) != invalid || (invalid && calls != 0) {
					t.Fatalf("err=%v calls=%d", err, calls)
				}
				if !reflect.DeepEqual(before, os.Environ()) {
					t.Fatal("parent environment changed")
				}
			})
		}
	}
}

func TestGateEnvPersistedProvidersAndGates(t *testing.T) {
	testenv.SkipIfShort(t)
	t.Setenv("CI", "inherited")
	for _, kind := range []string{"pi", "claude"} {
		for _, raw := range []string{`{"CI":"repo-a"}`, `{"CI":"repo-b"}`, `{}`, `{"CI":""}`, `{"PATH":"bad"}`, ""} {
			app, repo, _ := planningFixture(t)
			app.RuntimeEnv = snapshotWith(map[string]string{runtimeconfig.EnvAgent: kind})
			app = gateEnvApp(t, app, repo, raw)
			// Recompose as a detached dashboard Tao invocation does for its selected repository.
			var err error
			app, err = app.settingsForRepository(context.Background(), repo)
			if err != nil {
				t.Fatal(err)
			}
			other, err := app.settingsForRepository(context.Background(), taodata.Repo{ID: "other", Root: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			otherPins, err := other.envSnapshot().GateEnv()
			if err != nil || len(otherPins) != 0 {
				t.Fatalf("repository leak: %v %v", otherPins, err)
			}
			want := "inherited"
			if strings.Contains(raw, "repo-a") {
				want = "repo-a"
			}
			if strings.Contains(raw, "repo-b") {
				want = "repo-b"
			}
			if raw == `{"CI":""}` {
				want = ""
			}
			called := false
			app.ProcessStarter = func(ctx context.Context, _ string, name string, _ []string) (agent.Process, error) {
				if name != kind {
					t.Fatalf("provider = %q want %q", name, kind)
				}
				called = true
				p, err := agent.DefaultProcessStarter(ctx, "", "/bin/sh", []string{"-c", `printf '%s' "$CI"`})
				if err != nil {
					t.Fatal(err)
				}
				out, err := io.ReadAll(p.Stdout())
				if err != nil {
					t.Fatal(err)
				}
				if err := p.Wait(); err != nil {
					t.Fatal(err)
				}
				if string(out) != want {
					t.Fatalf("%s: got %q want %q", kind, out, want)
				}
				return nil, errors.New("observed child")
			}
			generator, err := newReworkTriageTextGenerator(app, nil)
			if strings.Contains(raw, "PATH") {
				if err == nil {
					t.Fatal("invalid pins admitted")
				}
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			before := os.Environ()
			_, _ = generator.GenerateText(context.Background(), repo.Root, "test")
			if !reflect.DeepEqual(before, os.Environ()) {
				t.Fatal("provider changed parent environment")
			}
			if !called {
				t.Fatal("provider not started")
			}
			pins, err := app.envSnapshot().GateEnv()
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := commandrunner.DefaultLocal(commandrunner.WithGateEnv(context.Background(), pins), repo.Root, "/bin/sh", []string{"-c", `printf '%s' "$CI"`}, &out, io.Discard); err != nil {
				t.Fatal(err)
			}
			if out.String() != want {
				t.Fatalf("gate got %q want %q", out.String(), want)
			}
		}
	}
}

func TestDoctorGateEnvRendering(t *testing.T) {
	var out bytes.Buffer
	report := doctorReport{gateEnv: runtimeconfig.SettingStatus{Key: "gate_env", Value: "{\"CI\":\"x\"}\n\x1b[31m", Source: "repository", Warning: "bad\n\x1b[31m"}}
	if err := renderDoctorRoles(&out, report); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "\x1b") || !strings.Contains(out.String(), "gate_env") || !strings.Contains(out.String(), `\n`) {
		t.Fatalf("unsafe or absent pins: %q", out.String())
	}
}
