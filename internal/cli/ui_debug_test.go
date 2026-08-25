package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/internal/taodata"
)

func TestUIDebugCollectorIncludesRepositoryRuntimeDefaultsAndDoctorProblems(t *testing.T) {
	for _, name := range runtimeconfig.RuntimeEnvKeys() {
		t.Setenv(name, "")
	}
	t.Setenv("PATH", "")
	t.Setenv("TAO_DATA_HOME", t.TempDir())
	pullRequest := true
	app := App{
		Now: func() time.Time { return time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC) },
		Registry: func() NoteRegistry {
			return &fakeNoteRegistry{current: taodata.Repo{
				ID: "repo-a", RunDefaults: &taodata.RepoRunDefaults{PullRequest: &pullRequest},
			}}
		},
	}
	snapshot, err := (uiDebugCollector{app: app, executable: "/tmp/tao"}).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.CollectedAt.IsZero() || snapshot.SelectedAgent != "pi" {
		t.Fatalf("debug identity = collected %s agent %q", snapshot.CollectedAt, snapshot.SelectedAgent)
	}
	var pullRequestFound bool
	for _, row := range snapshot.RuntimeDefaults {
		if row.Name == runtimeconfig.EnvPullRequest {
			pullRequestFound = row.Value == "true" && row.Source == "repository"
		}
	}
	if !pullRequestFound {
		t.Fatalf("debug defaults did not include repository pull-request override: %+v", snapshot.RuntimeDefaults)
	}
	var missingAgent bool
	for _, problem := range snapshot.DoctorProblems {
		if problem.Category == "agent" && strings.Contains(problem.Detail, "no supported agents") {
			missingAgent = true
		}
	}
	if !missingAgent {
		t.Fatalf("debug doctor problems did not report missing agent: %+v", snapshot.DoctorProblems)
	}
	var executableFound, dataHomeFound bool
	for _, value := range snapshot.System {
		executableFound = executableFound || value.Label == "executable" && value.Value == "/tmp/tao"
		dataHomeFound = dataHomeFound || value.Label == "data home" && value.Value != ""
	}
	if !executableFound || !dataHomeFound {
		t.Fatalf("debug system values missing executable or data home: %+v", snapshot.System)
	}
}
