package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/internal/settings"
	"github.com/iamseth/tao/internal/taodata"
	"github.com/iamseth/tao/internal/term"
)

func writeGlobalSettings(t *testing.T, home, values string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"schema":"tao.config.v1","settings":`+values+`}`), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestSettingsRelativeDataHomePreservesEnvironment(t *testing.T) {
	for _, key := range []string{"TAO_DATA_HOME", "XDG_DATA_HOME"} {
		t.Run(key, func(t *testing.T) {
			t.Chdir(t.TempDir())
			t.Setenv("TAO_DATA_HOME", "")
			t.Setenv(key, ".")
			values := map[string]string{
				runtimeconfig.EnvAgent: "claude", runtimeconfig.EnvSessionTimeout: "1m", "TAO_UPDATE": "off",
				"TAO_BUDGET_SLICE_COST_WARN": "0", "TAO_BUDGET_SLICE_COST_STOP": "0",
			}
			app := (App{RuntimeEnv: snapshotWith(values)}).initializeSettings(context.Background())
			for setting, want := range map[string]string{"agent": "claude", "session_timeout": "1m0s", "update": "off", "budget.slice.cost.stop": "0"} {
				found := false
				for _, row := range app.envSnapshot().SettingsStatus() {
					if row.Key == setting {
						found = true
						if row.Value != want || row.Source != "env" {
							t.Fatalf("%s: %+v", setting, row)
						}
					}
				}
				if !found {
					t.Fatalf("missing %s", setting)
				}
			}
			budget, err := app.envSnapshot().Budget()
			if err != nil || budget.Slice.Cost.Stop == nil || *budget.Slice.Cost.Stop != 0 {
				t.Fatalf("hard budget cap lost: %+v %v", budget, err)
			}
			if err := app.settingsService().Update(context.Background(), settings.Target{Global: true}, map[string]*string{"agent": nil}); err != nil {
				t.Fatalf("relative config update: %v", err)
			}
			values[runtimeconfig.EnvSessionTimeout] = "invalid"
			invalid := (App{RuntimeEnv: snapshotWith(values)}).initializeSettings(context.Background())
			if _, err := invalid.envDefaultsFor(runtimeconfig.EnvSessionTimeout); err == nil {
				t.Fatal("invalid environment admitted")
			}
		})
	}
}

func TestSettingsInvocationFrozenAndRefresh(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	writeGlobalSettings(t, home, `{"models":{"model":"first"}}`)
	app := App{SettingsService: settings.NewService(home, runtimeconfig.LoadEnv(nil))}
	first := app.initializeSettings(ctx)
	if got := first.flagDefaults().Base; got != "first" {
		t.Fatalf("model = %q", got)
	}
	writeGlobalSettings(t, home, `{"models":{"model":"second"}}`)
	if got := first.flagDefaults().Base; got != "first" {
		t.Fatalf("snapshot changed: %q", got)
	}
	second := app.initializeSettings(ctx)
	if got := second.flagDefaults().Base; got != "second" {
		t.Fatalf("new invocation = %q", got)
	}
	refreshed := first.refreshSettings(ctx)
	if got := refreshed.flagDefaults().Base; got != "second" {
		t.Fatalf("refresh = %q", got)
	}
}

func TestSettingsHelpAndCompletionDoNotCreateConfiguration(t *testing.T) {
	home := t.TempDir()
	app := App{Out: io.Discard, Err: io.Discard, SelfUpdater: &fakeSelfUpdater{}, SettingsService: settings.NewService(home, runtimeconfig.LoadEnv(nil)), Repository: func(string) Repository { return fakeRepository{} }}
	for _, args := range [][]string{{"help"}, {"run", "--help"}, {"complete", "plan-ids"}} {
		if err := app.Run(context.Background(), args); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatalf("read-only commands wrote settings: %v %v", entries, err)
	}
}

func TestSettingsGlobalPresentationAndStartup(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	writeGlobalSettings(t, home, `{"theme":"gruvbox","update":"off"}`)
	updater := &fakeSelfUpdater{}
	app := App{Out: io.Discard, Err: io.Discard, SelfUpdater: updater, SettingsService: settings.NewService(home, runtimeconfig.LoadEnv(nil))}
	selected := app.initializeSettings(ctx).withRuntimeTheme()
	if selected.outputTheme().Name() != "gruvbox" {
		t.Fatal("global theme ignored")
	}
	if err := app.Run(ctx, []string{"version"}); err != nil {
		t.Fatal(err)
	}
	if len(updater.startupModes) != 0 {
		t.Fatalf("startup modes: %v", updater.startupModes)
	}
	writeGlobalSettings(t, home, `{"theme":"tokyonight","update":"warn"}`)
	if selected.refreshSettings(ctx).outputTheme().Name() != "tokyonight" {
		t.Fatal("theme did not refresh")
	}
	if err := app.Run(ctx, []string{"version"}); err != nil {
		t.Fatal(err)
	}
	if len(updater.startupModes) != 1 || string(updater.startupModes[0]) != "warn" {
		t.Fatalf("new invocation modes: %v", updater.startupModes)
	}
}

func TestSettingsDiagnosticsRemainAvailable(t *testing.T) {
	for _, contents := range []string{`{`, `{"schema":"tao.config.v1","settings":{"update":"invalid","agent":"invalid"}}`} {
		for _, args := range [][]string{{"help"}, {"run", "--help"}, {"status", "--json"}, {"ui"}, {"repo", "doctor"}} {
			t.Run(contents+strings.Join(args, " "), func(t *testing.T) {
				home := t.TempDir()
				t.Setenv("TAO_DATA_HOME", home)
				if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(contents), 0600); err != nil {
					t.Fatal(err)
				}
				var out testTerminalBuffer
				var warnings bytes.Buffer
				updater := &fakeSelfUpdater{}
				app := App{In: strings.NewReader("q"), Out: &out, Err: &warnings, SelfUpdater: updater,
					SettingsService: settings.NewService(home, runtimeconfig.LoadEnv(nil)),
					Repository:      func(string) Repository { return fakeRepository{} },
					Registry:        func() NoteRegistry { return taodata.NewRegistry(home) },
					MonitorTicker: func(time.Duration) MonitorTicker {
						return &monitorTickerStub{ch: make(chan time.Time), stopped: make(chan struct{})}
					},
					UITerminal: &uiTerminalStub{size: term.Size{Width: 160, Height: 60}},
				}
				if err := app.Run(context.Background(), args); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(warnings.String(), "warning: settings:") {
					t.Fatalf("missing diagnostic: %s", warnings.String())
				}
			})
		}
	}
}

func TestSettingsEarlyFailureStopsExecution(t *testing.T) {
	app := (App{SettingsService: settings.NewService("", runtimeconfig.LoadEnv(nil))}).initializeSettings(context.Background())
	if _, err := app.runEnvDefaults(); err == nil {
		t.Fatal("invalid settings location admitted")
	}
}

func TestSettingsStructuralFailureStopsExecution(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{`), 0600); err != nil {
		t.Fatal(err)
	}
	app := (App{Out: io.Discard, Err: io.Discard, SettingsService: settings.NewService(home, runtimeconfig.LoadEnv(nil))}).initializeSettings(context.Background())
	if _, err := app.runEnvDefaults(); err == nil {
		t.Fatal("corrupt settings admitted")
	}
}

func TestSettingsFieldAdmissionAndFrozenEnvironment(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TAO_DATA_HOME", home)
	t.Setenv(runtimeconfig.EnvModel, "captured")
	writeGlobalSettings(t, home, `{"agent":"bad","models":{"model":"saved"},"update":"off"}`)
	base := App{Out: io.Discard, Err: io.Discard}
	if err := base.Run(context.Background(), []string{"version"}); err != nil {
		t.Fatalf("unrelated invalid field blocked command: %v", err)
	}
	base.RuntimeEnv = snapshotWith(map[string]string{runtimeconfig.EnvModel: "captured"})
	app := base.initializeSettings(context.Background())
	if _, err := app.envDefaultsFor(runtimeconfig.EnvModel); err != nil {
		t.Fatal(err)
	}
	if _, err := app.envDefaultsFor(runtimeconfig.EnvAgent); err == nil {
		t.Fatal("invalid consumed field admitted")
	}
	t.Setenv(runtimeconfig.EnvModel, "later")
	app = app.refreshSettings(context.Background())
	if app.flagDefaults().Base != "captured" {
		t.Fatal("refresh recaptured environment")
	}
}

func TestSettingsRepositoryCopies(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	writeGlobalSettings(t, home, `{"models":{"model":"global"}}`)
	for _, id := range []string{"one", "two"} {
		dir := filepath.Join(home, "repos", id)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		data := `{"schema":"tao.repo.v1","id":"` + id + `","name":"` + id + `","root":"/repo","run_defaults":{"models":{"run_model":"` + id + `"}}}`
		if err := os.WriteFile(filepath.Join(dir, "repo.json"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	app := (App{SettingsService: settings.NewService(home, runtimeconfig.LoadEnv(nil))}).initializeSettings(ctx)
	writeGlobalSettings(t, home, `{"models":{"model":"later"}}`)
	one, err := app.settingsForRepository(ctx, taodata.Repo{ID: "one"})
	if err != nil {
		t.Fatal(err)
	}
	two, err := one.settingsForRepository(ctx, taodata.Repo{ID: "two"})
	if err != nil {
		t.Fatal(err)
	}
	if one.flagDefaults().Run != "one" || two.flagDefaults().Run != "two" || two.flagDefaults().Base != "global" || app.flagDefaults().Run != "" {
		t.Fatalf("copies not independent: %+v %+v %+v", app.flagDefaults().ModelSelection, one.flagDefaults().ModelSelection, two.flagDefaults().ModelSelection)
	}
}
