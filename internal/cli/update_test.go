package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/selfupdate"
)

type fakeSelfUpdater struct {
	result          selfupdate.UpdateResult
	err             error
	calls           int
	startupOutcome  selfupdate.StartupOutcome
	startupOutcomes []selfupdate.StartupOutcome
	startupModes    []selfupdate.Mode
}

func (updater *fakeSelfUpdater) Update(context.Context) (selfupdate.UpdateResult, error) {
	updater.calls++
	return updater.result, updater.err
}

func (updater *fakeSelfUpdater) Startup(_ context.Context, mode selfupdate.Mode) selfupdate.StartupOutcome {
	updater.startupModes = append(updater.startupModes, mode)
	if len(updater.startupOutcomes) >= len(updater.startupModes) {
		return updater.startupOutcomes[len(updater.startupModes)-1]
	}
	return updater.startupOutcome
}

func TestUpdateCommandBypassesModeAndCommandCaching(t *testing.T) {
	t.Setenv("TAO_UPDATE", "off")
	updater := &fakeSelfUpdater{result: selfupdate.UpdateResult{
		CurrentVersion: "v1.2.3",
		LatestVersion:  "v1.2.3",
		Comparison:     selfupdate.VersionCurrent,
	}}
	var out bytes.Buffer
	app := App{Out: &out, Err: &out, SelfUpdater: updater}

	for range 2 {
		if err := app.Run(context.Background(), []string{"update"}); err != nil {
			t.Fatal(err)
		}
	}
	if updater.calls != 2 {
		t.Fatalf("explicit updater calls = %d, want 2 uncached calls", updater.calls)
	}
	if strings.Count(out.String(), "already up to date") != 2 {
		t.Fatalf("output = %q", out.String())
	}
}

func TestUpdateCommandReportsCurrentAndSuccessfulOutcomes(t *testing.T) {
	tests := []struct {
		name   string
		result selfupdate.UpdateResult
		want   []string
	}{
		{
			name: "current",
			result: selfupdate.UpdateResult{
				CurrentVersion: "v1.2.3",
				LatestVersion:  "v1.2.3",
				Comparison:     selfupdate.VersionCurrent,
			},
			want: []string{"v1.2.3", "already up to date"},
		},
		{
			name: "installed",
			result: selfupdate.UpdateResult{
				CurrentVersion: "v1.2.2",
				LatestVersion:  "v1.2.3",
				Comparison:     selfupdate.VersionUpdateAvailable,
				Path:           "/usr/local/bin/tao",
				Installed:      true,
			},
			want: []string{"Updated Tao from v1.2.2 to v1.2.3", "/usr/local/bin/tao", "next invocation"},
		},
		{
			name: "concurrent install",
			result: selfupdate.UpdateResult{
				CurrentVersion:   "v1.2.2",
				LatestVersion:    "v1.2.3",
				Comparison:       selfupdate.VersionUpdateAvailable,
				Path:             "/usr/local/bin/tao",
				ConcurrentUpdate: true,
			},
			want: []string{"v1.2.3", "another process", "next invocation"},
		},
		{
			name: "ahead",
			result: selfupdate.UpdateResult{
				CurrentVersion: "v2.0.0",
				LatestVersion:  "v1.2.3",
				Comparison:     selfupdate.VersionAhead,
			},
			want: []string{"v2.0.0", "newer than", "no update was installed"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var out bytes.Buffer
			app := App{Out: &out, Err: &out, SelfUpdater: &fakeSelfUpdater{result: test.result}}
			if err := app.Run(context.Background(), []string{"update"}); err != nil {
				t.Fatal(err)
			}
			for _, want := range test.want {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("output %q does not contain %q", out.String(), want)
				}
			}
		})
	}
}

func TestUpdateCommandReturnsActionableAndPropagatedFailuresWithoutSuccessOutput(t *testing.T) {
	downloadFailure := errors.New("download release archive: connection reset")
	tests := []struct {
		name string
		err  error
		want []string
	}{
		{name: "development build", err: errors.New("development builds cannot self-update"), want: []string{"development builds", "cannot self-update"}},
		{name: "unsupported target", err: errors.New("self-update is unsupported on windows/amd64"), want: []string{"unsupported", "windows/amd64"}},
		{name: "homebrew", err: &selfupdate.HomebrewError{Path: "/opt/homebrew/Cellar/tao/1.2.2/bin/tao"}, want: []string{"Homebrew-managed", "brew upgrade tao"}},
		{name: "download failure", err: downloadFailure, want: []string{"update Tao", "connection reset"}},
		{name: "validation failure", err: errors.New("checksum mismatch for tao_Linux_x86_64.tar.gz"), want: []string{"update Tao", "checksum mismatch"}},
		{name: "permission failure", err: errors.New("chmod replacement: operation not permitted"), want: []string{"update Tao", "operation not permitted"}},
		{name: "replacement failure", err: errors.New("replace Tao executable: read-only file system"), want: []string{"update Tao", "read-only file system"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var out bytes.Buffer
			updater := &fakeSelfUpdater{err: test.err}
			app := App{Out: &out, Err: &out, SelfUpdater: updater}
			err := app.Run(context.Background(), []string{"update"})
			if err == nil {
				t.Fatal("expected update error")
			}
			if !errors.Is(err, test.err) {
				t.Fatalf("error %v does not preserve service failure %v", err, test.err)
			}
			for _, want := range test.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q does not contain %q", err, want)
				}
			}
			if updater.calls != 1 {
				t.Fatalf("updater calls = %d, want 1", updater.calls)
			}
			if out.Len() != 0 {
				t.Fatalf("failure output claimed success: %q", out.String())
			}
		})
	}
}

func TestUpdateCommandRejectsArgumentsAndIncompleteSuccess(t *testing.T) {
	app := App{Out: &bytes.Buffer{}, SelfUpdater: &fakeSelfUpdater{}}
	if err := app.Run(context.Background(), []string{"update", "now"}); err == nil || err.Error() != "usage: tao update" {
		t.Fatalf("argument error = %v", err)
	}

	updater := &fakeSelfUpdater{result: selfupdate.UpdateResult{
		CurrentVersion: "v1.2.2",
		LatestVersion:  "v1.2.3",
		Comparison:     selfupdate.VersionUpdateAvailable,
	}}
	var out bytes.Buffer
	app = App{Out: &out, SelfUpdater: updater}
	if err := app.Run(context.Background(), []string{"update"}); err == nil || !strings.Contains(err.Error(), "without installing") {
		t.Fatalf("incomplete update error = %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("incomplete update claimed success: %q", out.String())
	}
}
