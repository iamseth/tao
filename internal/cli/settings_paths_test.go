package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/internal/taodata"
)

func TestStatusSavedMaskedAndInvalid(t *testing.T) {
	clearTaoEnv(t)
	registered := taodata.Repo{ID: "repo-a", RunDefaults: &taodata.RepoRunDefaults{PullRequest: new(true), Models: &taodata.RepoModelDefaults{Base: "repo-model"}}}
	home := persistStatusRepository(t, registered)
	writeGlobalSettings(t, home, `{"models":{"model":"global-model"},"session_timeout":"invalid"}`)
	t.Setenv(runtimeconfig.EnvModel, "env-model")
	t.Setenv(runtimeconfig.EnvPullRequest, "false")
	t.Setenv(runtimeconfig.EnvSessionTimeout, "2m")
	var out bytes.Buffer
	app := App{Out: &out, Registry: func() NoteRegistry { return &fakeNoteRegistry{current: registered, repos: []taodata.Repo{registered}} }, Repository: func(string) Repository { return nil }}
	if err := app.Run(context.Background(), []string{"status", "--json"}); err != nil {
		t.Fatal(err)
	}
	var payload statusPayload
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range payload.Settings {
		if row.Key == "models.model" {
			found = true
			if row.Value != "env-model" || row.Source != "env" || row.GlobalValue != "global-model" || row.RepositoryValue != "repo-model" {
				t.Fatal(row)
			}
		}
	}
	if !found {
		t.Fatal("missing model")
	}
	for _, row := range payload.RuntimeEnv {
		if row.Name == runtimeconfig.EnvSessionTimeout && (row.Source != "invalid" || !strings.Contains(row.Warning, "invalid")) {
			t.Fatal(row)
		}
	}
	out.Reset()
	if err := app.Run(context.Background(), []string{"status"}); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"saved global=global-model repository=repo-model", "rejected on consumption", "environment → flags"} {
		if !strings.Contains(out.String(), text) {
			t.Errorf("missing %s", text)
		}
	}
}

func TestSettingsPathsFallbackAndOverrides(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TAO_DATA_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("PI_CODING_AGENT_DIR", filepath.Join(home, "pi"))
	t.Setenv("TAO_CLAUDE_COMMANDS_DIR", filepath.Join(home, "commands"))
	extension := filepath.Join(home, "extension")
	if err := os.MkdirAll(extension, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extension, "package.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TAO_PI_EXTENSION_DIR", extension)
	rows := (App{}).captureSettingsPaths("missing").settingsPaths()
	for _, row := range rows {
		switch row.Name {
		case "data home":
			if row.Value != filepath.Join(home, ".local", "share", "tao") || row.Source != "user home" {
				t.Fatal(row)
			}
		case "Pi prompts":
			if row.Source != "PI_CODING_AGENT_DIR" || row.Warning == "" {
				t.Fatal(row)
			}
		case "Pi extension source":
			if row.Value != extension || !strings.Contains(row.Source, "extension source") {
				t.Fatal(row)
			}
		case "Pi extension install destination":
			if row.Value != filepath.Join(home, "pi", "extensions", "tao") {
				t.Fatal(row)
			}
		}
	}
	t.Setenv("TAO_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "ignored"))
	if row := (App{}).captureSettingsPaths("").settingsPaths()[0]; row.Value != filepath.Join(home, "data") || row.Source != "TAO_DATA_HOME" {
		t.Fatal(row)
	}
	if _, err := os.Stat(filepath.Join(home, "pi")); !os.IsNotExist(err) {
		t.Fatalf("path discovery wrote files: %v", err)
	}
}

func TestSettingsPathsFrozenAndSelected(t *testing.T) {
	clearTaoEnv(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TAO_DATA_HOME", "")
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg"))
	t.Setenv("PI_CODING_AGENT_DIR", "")
	t.Setenv("TAO_CLAUDE_COMMANDS_DIR", "")
	a := App{}
	a = a.captureSettingsPaths("selected-plans")
	t.Setenv("XDG_DATA_HOME", "changed")
	rows := a.settingsPaths()
	want := map[string]string{"data home": filepath.Join(home, "xdg", "tao"), "global config": filepath.Join(home, "xdg", "tao", "config.json"), "plans directory": "selected-plans", "Pi agent": filepath.Join(home, ".pi", "agent"), "Claude commands": filepath.Join(home, ".claude", "commands")}
	for _, row := range rows {
		if value, ok := want[row.Name]; ok {
			if row.Value != value {
				t.Errorf("%s = %s want %s", row.Name, row.Value, value)
			}
			delete(want, row.Name)
		}
		if row.Name == "plans directory" && row.Source != "--plans-dir" {
			t.Fatal(row)
		}
	}
	if len(want) > 0 {
		t.Fatal(want)
	}
	rows[0].Value = "mutated"
	if a.settingsPaths()[0].Value == "mutated" {
		t.Fatal("mutable paths")
	}
}

func TestStatusScopedProjectionCompleteness(t *testing.T) {
	clearTaoEnv(t)
	t.Setenv("TAO_DATA_HOME", t.TempDir())
	var out bytes.Buffer
	app := App{Out: &out, Registry: func() NoteRegistry { return &fakeNoteRegistry{current: taodata.Repo{}} }, Repository: func(string) Repository { return nil }}
	if err := app.Run(context.Background(), []string{"--plans-dir", "missing", "status", "--json"}); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Settings []runtimeconfig.SettingStatus `json:"settings"`
		Paths    []settingsPathRow             `json:"paths"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, row := range payload.Settings {
		counts[row.Key]++
	}
	for _, d := range runtimeconfig.SettingDefinitions() {
		if counts[d.Key] != 1 {
			t.Errorf("%s count %d", d.Key, counts[d.Key])
		}
	}
	if len(payload.Paths) == 0 {
		t.Fatal("missing paths")
	}
}
