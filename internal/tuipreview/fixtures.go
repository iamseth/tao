// Package tuipreview provides deterministic, in-memory data for exercising the
// production terminal UI without reading Tao or repository state.
package tuipreview

import (
	"errors"
	"fmt"
	"time"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/runstatus"
)

const (
	ScenarioMixed  = "mixed"
	ScenarioEmpty  = "empty"
	ScenarioStress = "stress"
)

var fixtureNow = time.Date(2026, 8, 21, 23, 0, 0, 0, time.UTC)

// PlanFixture associates the plan directory used by monitor rows with its
// typed detail projection and in-memory log.
type PlanFixture struct {
	PlanDir string
	Detail  plan.PlanDetail
	Log     string
}

// Scenario is one named preview state. All timestamps and values are fixed.
type Scenario struct {
	Name        string
	Description string
	Now         time.Time
	Snapshot    monitor.Snapshot
	Notes       note.Snapshot
	Plans       []PlanFixture
}

// Scenarios returns a fresh, stable catalog in display order. Callers may
// modify returned values without changing a later catalog lookup.
func Scenarios() []Scenario {
	return []Scenario{mixedScenario(), emptyScenario(), stressScenario()}
}

// Lookup finds a scenario by its stable name.
func Lookup(name string) (Scenario, bool) {
	for _, scenario := range Scenarios() {
		if scenario.Name == name {
			return scenario, true
		}
	}
	return Scenario{}, false
}

func mixedScenario() Scenario {
	now := fixtureNow
	updated := func(age time.Duration) *time.Time {
		value := now.Add(-age)
		return &value
	}
	rows := []monitor.Row{
		previewRow("alpha", "alpha", "blocked", "Blocked database migration", plan.StatusBlocked, updated(4*time.Minute), monitor.AttentionBlocked),
		previewRow("beta", "βeta", "changes", "Review found regressions", plan.StatusChangesRequested, updated(9*time.Minute), monitor.AttentionChangesRequested),
		previewRow("alpha", "alpha", "approval", "Awaiting owner sign-off", plan.StatusPlanned, updated(12*time.Minute), monitor.AttentionApprovalRequired),
		previewRow("beta", "βeta", "completion", "Commit proposal pending", plan.StatusInProgress, updated(15*time.Minute), monitor.AttentionSliceCompletionPending),
		previewRow("alpha", "alpha", "rework", "Repeated review finding", plan.StatusChangesRequested, updated(20*time.Minute), monitor.AttentionReworkStopped),
		previewRow("beta", "βeta", "crashed", "Interrupted provider run", plan.StatusInProgress, updated(24*time.Minute), monitor.AttentionRunCrashed),
		{
			Kind: monitor.RowKindPlan, RepositoryID: "alpha", RepositoryName: "alpha", RepositoryRoot: "/preview/alpha",
			PlanID: "live", PlanTitle: "Live implementation", PlanDir: "fixture://mixed/live", Status: plan.StatusInProgress,
			Liveness: monitor.LivenessLive, Phase: runstatus.Phase("running_slice"), SliceID: "002-render-boundary",
			InvocationDuration: 7*time.Minute + 13*time.Second, HeartbeatAge: 2 * time.Second,
			OriginalCompletedCount: 1, OriginalTotalCount: 3, UpdatedAt: updated(2 * time.Minute),
		},
		{
			Kind: monitor.RowKindPlan, RepositoryID: "beta", RepositoryName: "βeta", RepositoryRoot: "/preview/beta",
			PlanID: "stalled", PlanTitle: "Possibly stalled run", PlanDir: "fixture://mixed/stalled", Status: plan.StatusInProgress,
			Liveness: monitor.LivenessStale, Phase: runstatus.Phase("verify"), InvocationDuration: 91 * time.Minute,
			HeartbeatAge: 45 * time.Second, RunLockPresent: true, RunLockProcessAlive: true,
			OriginalCompletedCount: 2, OriginalTotalCount: 3, ReworkCompletedCount: 1, ReworkTotalCount: 2, UpdatedAt: updated(45 * time.Second),
		},
		previewRow("alpha", "alpha", "planned", "Polish navigation", plan.StatusPlanned, updated(2*time.Hour)),
		previewRow("beta", "βeta", "review", "Ready for review", plan.StatusInReview, updated(3*time.Hour)),
		previewRow("alpha", "alpha", "reviewed", "Approved for merge", plan.StatusReviewed, updated(4*time.Hour)),
		previewRow("beta", "βeta", "complete", "Recently completed", plan.StatusCompleted, updated(6*time.Hour)),
		{
			Kind: monitor.RowKindRepositoryWarning, RepositoryID: "damaged", RepositoryName: "damaged-repo",
			Status: plan.StatusInvalid, Liveness: monitor.LivenessMissing, Warnings: []string{"fixture catalog is unreadable"},
		},
	}
	rows[2].ApprovalSliceID = "001-owner-choice"
	rows[2].ApprovalReason = "Choose the public command spelling"

	created := now.Add(-72 * time.Hour)
	notes := note.Snapshot{
		Notes: []note.CatalogNote{
			{RepositoryID: "alpha", RepositoryName: "alpha", RepositoryRoot: "/preview/alpha", ID: "note-api", Text: "Consider a smaller public API before release.\nKeep the first version read-only.", Tags: []string{"api", "follow-up"}, CreatedAt: created, UpdatedAt: now.Add(-35 * time.Minute)},
			{RepositoryID: "beta", RepositoryName: "βeta", RepositoryRoot: "/preview/beta", ID: "note-unicode", Text: "Verify resize behavior with 日本語, emoji 🧭, and combining é characters.", Tags: []string{"tui", "unicode"}, CreatedAt: created.Add(time.Hour), UpdatedAt: now.Add(-2 * time.Hour)},
			{RepositoryID: "alpha", RepositoryName: "alpha", RepositoryRoot: "/preview/alpha", ID: "note-empty-tags", Text: "A short untagged note.", CreatedAt: created.Add(2 * time.Hour), UpdatedAt: now.Add(-26 * time.Hour)},
		},
		Warnings: []note.CatalogWarning{{Kind: note.CatalogWarningRecord, RepositoryID: "damaged", RepositoryName: "damaged-repo", Path: "fixture://mixed/broken-note", Err: errors.New("invalid fixture note record")}},
	}

	return Scenario{
		Name: ScenarioMixed, Description: "plans and notes across lifecycle, liveness, warning, and approval states",
		Now: now, Snapshot: monitor.Snapshot{CollectedAt: now, Rows: rows}, Notes: notes,
		Plans: planFixturesForRows(mixedPlanFixture(now), rows),
	}
}

func previewRow(repoID, repoName, id, title, status string, updated *time.Time, attention ...monitor.AttentionReason) monitor.Row {
	return monitor.Row{
		Kind: monitor.RowKindPlan, RepositoryID: repoID, RepositoryName: repoName, RepositoryRoot: "/preview/" + repoID,
		PlanID: id, PlanTitle: title, PlanDir: "fixture://mixed/" + id, Status: status, Liveness: monitor.LivenessMissing,
		OriginalCompletedCount: 1, OriginalTotalCount: 3, UpdatedAt: updated, AttentionReasons: attention,
	}
}

func mixedPlanFixture(now time.Time) PlanFixture {
	current := "002-render-boundary"
	started := now.Add(-7 * time.Minute)
	completed := now.Add(-2 * time.Hour)
	duration := int64(312)
	detail := plan.PlanDetail{
		Dir: "fixture://mixed/live",
		State: plan.State{
			Schema: "tao.state.v1", Status: plan.StatusInProgress, CreatedAt: now.Add(-48 * time.Hour), UpdatedAt: now.Add(-2 * time.Minute),
			Repo:             plan.Repo{Name: "alpha", Root: "/preview/alpha", Branch: "main", BaseCommit: "0123456789abcdef"},
			Plan:             plan.PlanState{ID: "live", Title: "Live implementation", CurrentSlice: &current, CompletedSlices: []string{"001-fixtures"}, PendingSlices: []string{current, "003-command"}},
			GlobalInvariants: []string{"No external state", "Keep output deterministic"},
		},
		Slices: plan.SlicesFile{
			Schema: "tao.slices.v1", PlanID: "live", Execution: plan.Execution{Mode: "isolated", ParallelSafe: false},
			Slices: []plan.Slice{
				{ID: "001-fixtures", Title: "Build typed fixtures", Status: plan.StatusCompleted, Goal: "Create deterministic typed values.", Tasks: []string{"Add plan and note fixtures", "Cover warnings"}, ExpectedFiles: []string{"internal/tuipreview/fixtures.go"}, Verification: plan.Verification{Commands: []string{"go test ./internal/tuipreview"}}, Notes: "Fixtures are in memory.", Timing: plan.SliceTiming{CreatedAt: now.Add(-48 * time.Hour), StartedAt: &completed, CompletedAt: &completed, UpdatedAt: completed, DurationSeconds: &duration}, Completion: &plan.SliceCompletionOutcome{Outcome: plan.SliceCompletionCommitted, CommitSHA: "abc123fixture"}},
				{ID: current, Title: "Render deterministic frames", Status: plan.StatusInProgress, Goal: "Reuse production renderers at fixed dimensions.", Context: "The preview must preserve exact-height and ANSI behavior.", Tasks: []string{"Render all current views", "Remove only screen-control prefixes in plain mode"}, DependsOn: []string{"001-fixtures"}, ExpectedFiles: []string{"internal/tuipreview/preview.go"}, RequiredInputs: []plan.RequiredInput{{Path: "internal/tui", Kind: plan.RequiredInputDirectory, Reason: "production renderers"}}, Verification: plan.Verification{Commands: []string{"go test ./internal/tuipreview ./internal/tui"}, ManualChecks: []string{"Inspect colored plain output"}}, Approval: &plan.Approval{Required: true, Approved: true, Reason: "Use the production rendering boundary"}, Timing: plan.SliceTiming{CreatedAt: now.Add(-48 * time.Hour), StartedAt: &started, UpdatedAt: now.Add(-2 * time.Minute)}},
				{ID: "003-command", Title: "Add developer command", Status: plan.StatusPending, Goal: "Run the event loop with fixture boundaries.", Tasks: []string{"Wire a developer-only command"}, DependsOn: []string{current}, ExpectedFiles: []string{"cmd/tui-preview/main.go"}, Verification: plan.Verification{Commands: []string{"go test ./cmd/tui-preview"}}, Approval: &plan.Approval{Required: true, Reason: "Confirm command remains developer-only"}, Timing: plan.SliceTiming{CreatedAt: now.Add(-48 * time.Hour), UpdatedAt: now.Add(-48 * time.Hour)}},
			},
		},
		PlanningBrief: plan.PlanningBriefArtifact{Path: "fixture://mixed/planning-brief.md", Content: "Deterministic preview fixture."},
		Warnings:      []string{"fixture warning retained for detail coverage"},
	}
	log := "agent: loading deterministic run packet\n" +
		"tool: wrote internal/tuipreview/fixtures.go\n" +
		"agent: checking widths with 日本語 and 🧭\n" +
		"tool: go test ./internal/tuipreview ./internal/tui\n"
	return PlanFixture{PlanDir: detail.Dir, Detail: detail, Log: log}
}

func emptyScenario() Scenario {
	return Scenario{
		Name: ScenarioEmpty, Description: "no plans, notes, warnings, details, or logs",
		Now: fixtureNow, Snapshot: monitor.Snapshot{CollectedAt: fixtureNow}, Notes: note.Snapshot{},
	}
}

func stressScenario() Scenario {
	now := fixtureNow
	rows := make([]monitor.Row, 0, 36)
	notes := make([]note.CatalogNote, 0, 30)
	for index := 0; index < 36; index++ {
		updated := now.Add(-time.Duration(index+1) * time.Minute)
		status := plan.StatusPlanned
		liveness := monitor.LivenessMissing
		if index%7 == 0 {
			status = plan.StatusCompleted
		} else if index%5 == 0 {
			status = plan.StatusInProgress
			liveness = monitor.LivenessLive
		}
		rows = append(rows, monitor.Row{
			Kind: monitor.RowKindPlan, RepositoryID: fmt.Sprintf("repo-%02d", index%4), RepositoryName: []string{"very-long-repository-name-alpha", "日本語リポジトリ", "emoji-🧭-workspace", "combining-é-repo"}[index%4],
			RepositoryRoot: fmt.Sprintf("/preview/stress/repo-%02d", index%4), PlanID: fmt.Sprintf("stress-%02d", index),
			PlanTitle: fmt.Sprintf("Long deterministic plan %02d — resize 日本語 🧭 é and an intentionally extended title", index), PlanDir: fmt.Sprintf("fixture://stress/%02d", index),
			Status: status, Liveness: liveness, Phase: runstatus.Phase("running_slice"), SliceID: fmt.Sprintf("%03d-very-long-slice-identifier", index),
			InvocationDuration: time.Duration(index+1) * time.Minute, OriginalCompletedCount: index % 4, OriginalTotalCount: 5, UpdatedAt: &updated,
		})
	}
	created := now.Add(-14 * 24 * time.Hour)
	for index := 0; index < 30; index++ {
		notes = append(notes, note.CatalogNote{
			RepositoryID: fmt.Sprintf("repo-%02d", index%4), RepositoryName: []string{"very-long-repository-name-alpha", "日本語リポジトリ", "emoji-🧭-workspace", "combining-é-repo"}[index%4], RepositoryRoot: fmt.Sprintf("/preview/stress/repo-%02d", index%4),
			ID: fmt.Sprintf("stress-note-%02d-with-a-long-id", index), Text: fmt.Sprintf("Stress note %02d: 日本語 🧭 é. %s", index, "A long line repeats deterministic text to exercise narrow and wide viewports without external data."),
			Tags: []string{"stress", "unicode", fmt.Sprintf("group-%d", index%3)}, CreatedAt: created.Add(time.Duration(index) * time.Hour), UpdatedAt: now.Add(-time.Duration(index+1) * time.Hour),
		})
	}
	return Scenario{
		Name: ScenarioStress, Description: "long plan and note tables with unicode and viewport pressure", Now: now,
		Snapshot: monitor.Snapshot{CollectedAt: now, Rows: rows}, Notes: note.Snapshot{Notes: notes},
		Plans: planFixturesForRows(stressPlanFixture(now), rows),
	}
}

func planFixturesForRows(primary PlanFixture, rows []monitor.Row) []PlanFixture {
	fixtures := []PlanFixture{primary}
	for _, row := range rows {
		if row.Kind != monitor.RowKindPlan || row.PlanDir == "" || row.PlanDir == primary.PlanDir {
			continue
		}
		fixture := primary
		fixture.PlanDir = row.PlanDir
		fixture.Detail = clonePlanDetail(primary.Detail)
		fixture.Detail.Dir = row.PlanDir
		fixture.Detail.State.Status = row.Status
		fixture.Detail.State.Repo.Name = row.RepositoryName
		fixture.Detail.State.Repo.Root = row.RepositoryRoot
		fixture.Detail.State.Plan.ID = row.PlanID
		fixture.Detail.State.Plan.Title = row.PlanTitle
		fixture.Detail.Slices.PlanID = row.PlanID
		fixture.Log = fmt.Sprintf("agent: deterministic fixture log for %s\ntool: no external state accessed\n", row.PlanID)
		fixtures = append(fixtures, fixture)
	}
	return fixtures
}

func stressPlanFixture(now time.Time) PlanFixture {
	fixture := mixedPlanFixture(now)
	fixture.PlanDir = "fixture://stress/00"
	fixture.Detail.Dir = fixture.PlanDir
	fixture.Detail.State.Repo.Name = "日本語リポジトリ🧭"
	fixture.Detail.State.Plan.ID = "stress-00"
	fixture.Detail.State.Plan.Title = "Long deterministic plan 00 — resize 日本語 🧭 é and an intentionally extended title"
	fixture.Detail.Slices.PlanID = "stress-00"
	fixture.Detail.Slices.Slices[1].Context += " " + "日本語とemoji 🧭 remain visible in narrow frames."
	fixture.Log += "agent: long unicode line 日本語日本語日本語 🧭🧭🧭 ééé\n"
	return fixture
}
