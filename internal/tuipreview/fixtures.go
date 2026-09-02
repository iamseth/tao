// Package tuipreview provides deterministic, in-memory data for exercising the
// production terminal UI without reading Tao or repository state.
package tuipreview

import (
	"bytes"
	"errors"
	"fmt"
	"time"

	"github.com/iamseth/tao/internal/agent/logrecord"
	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/runstatus"
	"github.com/iamseth/tao/internal/tui"
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
	Debug       tui.DebugSnapshot
	Settings    tui.SettingsSnapshot
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
		{
			Kind: monitor.RowKindPlan, RepositoryID: "alpha", RepositoryName: "alpha", RepositoryRoot: "/preview/alpha",
			PlanID: "finalize", PlanTitle: "Recover pull-request handoff", PlanDir: "fixture://mixed/finalize", Status: plan.StatusReviewed,
			Liveness: monitor.LivenessMissing, OriginalCompletedCount: 3, OriginalTotalCount: 3, UpdatedAt: updated(18 * time.Minute),
			AttentionReasons: []monitor.AttentionReason{monitor.AttentionFinalizationFailed}, FinalizationPhase: plan.FinalizationFailurePhasePullRequest,
			FinalizationCategory: "publication_failed", RecommendedAction: plan.PlanAction{Kind: plan.PlanActionRecoverPullRequest, Class: plan.PlanActionClassRecovery, Command: "tao run --pull-request finalize"},
		},
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
		Name: ScenarioMixed, Description: "plans, notes, and diagnostics across lifecycle, liveness, warning, and approval states",
		Now: now, Snapshot: monitor.Snapshot{CollectedAt: now, Rows: rows}, Notes: notes, Debug: debugFixture(now), Settings: settingsFixture(now),
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
	log := fixtureLog(now,
		logrecord.Record{Type: logrecord.TypeSession, Content: "running 001-fixtures"},
		logrecord.Record{Type: logrecord.TypeAssistant, Content: "loading deterministic run packet"},
		logrecord.Record{Type: logrecord.TypeToolResult, Name: "write", Content: "internal/tuipreview/fixtures.go"},
		logrecord.Record{Type: logrecord.TypeSession, Content: "running 002-render-boundary"},
		logrecord.Record{Type: logrecord.TypeAssistant, Content: "checking widths with 日本語 and 🧭"},
		logrecord.Record{Type: logrecord.TypeToolCall, Name: "bash", Payload: `{"command":"go test ./internal/tuipreview ./internal/tui"}`},
	)
	return PlanFixture{PlanDir: detail.Dir, Detail: detail, Log: log}
}

func emptyScenario() Scenario {
	return Scenario{
		Name: ScenarioEmpty, Description: "no plans, notes, warnings, details, or logs",
		Now: fixtureNow, Snapshot: monitor.Snapshot{CollectedAt: fixtureNow}, Notes: note.Snapshot{}, Debug: debugWithoutAnomaliesFixture(fixtureNow), Settings: settingsWithoutOverridesFixture(fixtureNow),
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
		tags := []string{"stress", "unicode", fmt.Sprintf("group-%d", index%3)}
		// Tier tags on a deterministic subset keep both the tiered ordering
		// and the empty TIER cell exercised across the width sweep.
		if index%4 == 0 {
			tags = append(tags, fmt.Sprintf("tier%d", index%3))
		}
		notes = append(notes, note.CatalogNote{
			RepositoryID: fmt.Sprintf("repo-%02d", index%4), RepositoryName: []string{"very-long-repository-name-alpha", "日本語リポジトリ", "emoji-🧭-workspace", "combining-é-repo"}[index%4], RepositoryRoot: fmt.Sprintf("/preview/stress/repo-%02d", index%4),
			ID: fmt.Sprintf("stress-note-%02d-with-a-long-id", index), Text: fmt.Sprintf("Stress note %02d: 日本語 🧭 é. %s", index, "A long line repeats deterministic text to exercise narrow and wide viewports without external data."),
			Tags: tags, CreatedAt: created.Add(time.Duration(index) * time.Hour), UpdatedAt: now.Add(-time.Duration(index+1) * time.Hour),
		})
	}
	return Scenario{
		Name: ScenarioStress, Description: "long plan and note tables with unicode and viewport pressure", Now: now,
		Snapshot: monitor.Snapshot{CollectedAt: now, Rows: rows}, Notes: note.Snapshot{Notes: notes}, Debug: debugFixture(now), Settings: settingsFixture(now),
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
		fixture.Log = fixtureLog(fixtureNow,
			logrecord.Record{Type: logrecord.TypeSession, Content: "running 002-render-boundary"},
			logrecord.Record{Type: logrecord.TypeAssistant, Content: fmt.Sprintf("deterministic fixture log for %s", row.PlanID)},
			logrecord.Record{Type: logrecord.TypeDiagnostic, Content: "no external state accessed"},
		)
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
	fixture.Log += fixtureLog(now.Add(6*time.Second), logrecord.Record{Type: logrecord.TypeAssistant, Content: "long unicode line 日本語日本語日本語 🧭🧭🧭 ééé"})
	return fixture
}

func settingsFixture(now time.Time) tui.SettingsSnapshot {
	explicitFalse := false
	explicitTrue := true
	return tui.SettingsSnapshot{
		CollectedAt: now, InheritedPullRequest: false, DisplayHome: "/preview",
		RuntimeDefaults: []tui.SettingsRuntimeDefault{
			{Name: "TAO_COMMIT_POLICY", Value: "slice", Source: "default"},
			{Name: "TAO_EXECUTION_MODE", Value: "isolated", Source: "default"},
			{Name: "TAO_AGENT", Value: "pi", Source: "default"},
			{Name: "TAO_SESSION_TIMEOUT", Value: "20m", Source: "default"},
			{Name: "TAO_UPDATE", Value: "warn", Source: "default", Warning: "fixture warning"},
			{Name: "TAO_PULL_REQUEST", Value: "false", Source: "env"},
			{Name: "TAO_REVIEW", Value: "true", Source: "default"},
			{Name: "TAO_AUTO_REWORK", Value: "true", Source: "default"},
			{Name: "TAO_MAX_REWORK_ATTEMPTS", Value: "5", Source: "default"},
			{Name: "TAO_DANGEROUSLY_SKIP_PERMISSIONS", Value: "false", Source: "default"},
			{Name: "TAO_MAX_SLICE_OUTPUT_TOKENS", Value: "disabled", Source: "default"},
			{Name: "TAO_MAX_SLICE_COST", Value: "disabled", Source: "default"},
			{Name: "TAO_BUDGET_SLICE_OUTPUT_TOKENS", Value: "40000", Source: "default"},
			{Name: "TAO_BUDGET_SLICE_COST", Value: "5.00", Source: "default"},
			{Name: "TAO_BUDGET_SLICE_TOOL_CALLS", Value: "120", Source: "default"},
			{Name: "TAO_BUDGET_SLICE_ASSISTANT_MESSAGES", Value: "80", Source: "default"},
			{Name: "TAO_BUDGET_SLICE_ERRORED_MESSAGES", Value: "0", Source: "default"},
			{Name: "TAO_BUDGET_PLAN_OUTPUT_TOKENS", Value: "150000", Source: "default"},
			{Name: "TAO_BUDGET_PLAN_COST", Value: "20.000", Source: "default"},
			{Name: "TAO_BUDGET_PLAN_TOOL_CALLS", Value: "400", Source: "default"},
			{Name: "TAO_BUDGET_PLAN_ASSISTANT_MESSAGES", Value: "300", Source: "default"},
			{Name: "TAO_BUDGET_PLAN_ERRORED_MESSAGES", Value: "0", Source: "default"},
		},
		Repositories: []tui.RepositorySetting{
			{ID: "alpha", Name: "alpha", Root: "/preview/alpha", Health: "ok", Finding: "ok", PullRequest: &explicitFalse},
			{ID: "beta", Name: "βeta", Root: "/preview/beta", Health: "ok", Finding: "ok"},
			{ID: "damaged", Name: "damaged-repo", Root: "/preview/missing", Health: "missing_root", Finding: "repo root does not exist", PullRequest: &explicitTrue},
		},
	}
}

func settingsWithoutOverridesFixture(now time.Time) tui.SettingsSnapshot {
	snapshot := settingsFixture(now)
	for index := range snapshot.RuntimeDefaults {
		snapshot.RuntimeDefaults[index].Source = "default"
		snapshot.RuntimeDefaults[index].Warning = ""
	}
	for index := range snapshot.Repositories {
		snapshot.Repositories[index].PullRequest = nil
	}
	return snapshot
}

func debugWithoutAnomaliesFixture(now time.Time) tui.DebugSnapshot {
	snapshot := debugFixture(now)
	rows := snapshot.RuntimeDefaults[:0]
	for _, row := range snapshot.RuntimeDefaults {
		if row.Name == "TAO_REPOSITORY_ONLY" {
			continue
		}
		if row.Name == "TAO_PULL_REQUEST" {
			row.Value = "false"
		}
		row.Warning = ""
		rows = append(rows, row)
	}
	snapshot.RuntimeDefaults = rows
	return snapshot
}

func debugFixture(now time.Time) tui.DebugSnapshot {
	return tui.DebugSnapshot{
		CollectedAt: now,
		System: []tui.DebugValue{
			{Label: "version", Value: "dev"},
			{Label: "commit", Value: "e20b72d"},
			{Label: "build age", Value: "2 hours old"},
			{Label: "go", Value: "go1.26.2"},
			{Label: "platform", Value: "darwin/arm64"},
			{Label: "executable", Value: "/preview/bin/tao"},
			{Label: "data home", Value: "/preview/data/tao"},
			{Label: "working directory", Value: "/preview/alpha"},
		},
		SelectedAgent:   "pi",
		InstalledAgents: []string{"Pi", "Claude Code"},
		DoctorProblems: []tui.DebugProblem{
			{Category: "prompt pi", Name: "tao-run", Status: "stale", Detail: "/preview/.pi/prompts/tao-run.md"},
			{Category: "tool recommended", Name: "jq", Status: "warning", Detail: "missing"},
		},
		RuntimeDefaults: []tui.DebugRuntimeDefault{
			{Name: "TAO_AGENT", Value: "pi", Source: "default"},
			{Name: "TAO_COMMIT_POLICY", Value: "slice", Source: "default"},
			{Name: "TAO_EXECUTION_MODE", Value: "isolated", Source: "default"},
			{Name: "TAO_PULL_REQUEST", Value: "true", Source: "repository"},
			{Name: "TAO_REVIEW", Value: "true", Source: "default"},
			{Name: "TAO_SESSION_TIMEOUT", Value: "20m", Source: "default"},
			{Name: "TAO_UPDATE", Value: "warn", Source: "env", Warning: "fixture warning"},
			{Name: "TAO_REPOSITORY_ONLY", Value: "enabled", Source: "repository"},
		},
	}
}

func fixtureLog(start time.Time, records ...logrecord.Record) string {
	var output bytes.Buffer
	for index, record := range records {
		record.Timestamp = start.Add(time.Duration(index) * time.Second).Format(time.RFC3339)
		_ = logrecord.Write(&output, record)
	}
	return output.String()
}
