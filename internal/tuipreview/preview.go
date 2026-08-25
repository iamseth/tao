package tuipreview

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/tui"
)

// View identifies one production renderer exposed by static preview.
type View string

const (
	ViewPlans       View = "plans"
	ViewNotes       View = "notes"
	ViewSettings    View = "settings"
	ViewDebug       View = "debug"
	ViewPlanDetail  View = "plan-detail"
	ViewNoteDetail  View = "note-detail"
	ViewSliceDetail View = "slice-detail"
)

// RenderOptions fully describes a deterministic static frame. PlanDir and
// SliceID optionally select a specific detail fixture; otherwise the first plan
// fixture and Selection-indexed slice are used.
type RenderOptions struct {
	View          View
	Width         int
	Height        int
	Selection     int
	Color         bool
	Plain         bool
	HideCompleted bool
	ShowShortcuts bool
	SearchQuery   string
	PlanDir       string
	SliceID       string
}

var ErrUnknownPlan = errors.New("unknown preview plan directory")

// Views returns the production renderers available for one-shot previews in a
// stable display order.
func Views() []View {
	return []View{ViewPlans, ViewNotes, ViewSettings, ViewDebug, ViewPlanDetail, ViewNoteDetail, ViewSliceDetail}
}

// LookupView resolves a one-shot view by name.
func LookupView(name string) (View, bool) {
	for _, view := range Views() {
		if string(view) == name {
			return view, true
		}
	}
	return "", false
}

// SnapshotCollector is an immutable in-memory implementation of
// tui.SnapshotCollector.
type SnapshotCollector struct {
	snapshot monitor.Snapshot
}

// NewSnapshotCollector copies snapshot into an in-memory collector.
func NewSnapshotCollector(snapshot monitor.Snapshot) *SnapshotCollector {
	return &SnapshotCollector{snapshot: cloneMonitorSnapshot(snapshot)}
}

// Collect returns a fresh projection or the context error.
func (c *SnapshotCollector) Collect(ctx context.Context) (monitor.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return monitor.Snapshot{}, err
	}
	return cloneMonitorSnapshot(c.snapshot), nil
}

// NoteSnapshotCollector is an immutable in-memory implementation of
// tui.NoteSnapshotCollector.
type NoteSnapshotCollector struct {
	snapshot note.Snapshot
}

// NewNoteSnapshotCollector copies snapshot into an in-memory collector.
func NewNoteSnapshotCollector(snapshot note.Snapshot) *NoteSnapshotCollector {
	return &NoteSnapshotCollector{snapshot: cloneNoteSnapshot(snapshot)}
}

// Collect returns a fresh projection or the context error.
func (c *NoteSnapshotCollector) Collect(ctx context.Context) (note.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return note.Snapshot{}, err
	}
	return cloneNoteSnapshot(c.snapshot), nil
}

// DebugSnapshotCollector is an immutable in-memory diagnostics collector.
type DebugSnapshotCollector struct {
	snapshot tui.DebugSnapshot
}

// NewDebugSnapshotCollector copies snapshot into an in-memory collector.
func NewDebugSnapshotCollector(snapshot tui.DebugSnapshot) *DebugSnapshotCollector {
	return &DebugSnapshotCollector{snapshot: cloneDebugSnapshot(snapshot)}
}

// Collect returns a fresh projection or the context error.
func (c *DebugSnapshotCollector) Collect(ctx context.Context) (tui.DebugSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return tui.DebugSnapshot{}, err
	}
	return cloneDebugSnapshot(c.snapshot), nil
}

// SettingsService is an in-memory repository settings service.
type SettingsService struct {
	mu       sync.Mutex
	snapshot tui.SettingsSnapshot
}

// NewSettingsService copies snapshot into an in-memory service.
func NewSettingsService(snapshot tui.SettingsSnapshot) *SettingsService {
	return &SettingsService{snapshot: cloneSettingsSnapshot(snapshot)}
}

// Collect returns a fresh projection or the context error.
func (s *SettingsService) Collect(ctx context.Context) (tui.SettingsSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return tui.SettingsSnapshot{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneSettingsSnapshot(s.snapshot), nil
}

// SetPullRequestDefault updates one fixture repository without external state.
func (s *SettingsService) SetPullRequestDefault(ctx context.Context, repositoryID string, value *bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.snapshot.Repositories {
		if s.snapshot.Repositories[index].ID != repositoryID {
			continue
		}
		s.snapshot.Repositories[index].PullRequest = cloneBool(value)
		return nil
	}
	return fmt.Errorf("fixture repository %q not found", repositoryID)
}

// DetailRepository is a read-only map of fixture plan directories to typed
// details and logs. It performs no filesystem, Git, or subprocess work.
type DetailRepository struct {
	plans map[string]PlanFixture
}

// NewDetailRepository copies fixtures into a read-only repository.
func NewDetailRepository(fixtures []PlanFixture) *DetailRepository {
	repository := &DetailRepository{plans: make(map[string]PlanFixture, len(fixtures))}
	for _, fixture := range fixtures {
		copy := fixture
		copy.Detail = clonePlanDetail(fixture.Detail)
		repository.plans[fixture.PlanDir] = copy
	}
	return repository
}

// ResolvePlan returns the typed detail mapped to planDir.
func (r *DetailRepository) ResolvePlan(ctx context.Context, planDir string) (*plan.PlanDetail, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fixture, ok := r.plans[planDir]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownPlan, planDir)
	}
	detail := clonePlanDetail(fixture.Detail)
	return &detail, nil
}

// ReadLogTail returns at most the requested number of newest fixture lines.
func (r *DetailRepository) ReadLogTail(planDir string, lines int) (string, error) {
	fixture, ok := r.plans[planDir]
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrUnknownPlan, planDir)
	}
	if lines <= 0 || fixture.Log == "" {
		return "", nil
	}
	trailingNewline := strings.HasSuffix(fixture.Log, "\n")
	parts := strings.Split(strings.TrimSuffix(fixture.Log, "\n"), "\n")
	if len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	result := strings.Join(parts, "\n")
	if trailingNewline {
		result += "\n"
	}
	return result, nil
}

// FollowLog validates planDir and then remains idle until cancellation. Fixture
// logs never change, so following cannot poll files or launch a process.
func (r *DetailRepository) FollowLog(ctx context.Context, planDir string, _ io.Writer) error {
	if _, ok := r.plans[planDir]; !ok {
		return fmt.Errorf("%w: %s", ErrUnknownPlan, planDir)
	}
	<-ctx.Done()
	return ctx.Err()
}

// NewSnapshotCollector returns this scenario's isolated plan collector.
func (s Scenario) NewSnapshotCollector() *SnapshotCollector {
	return NewSnapshotCollector(s.Snapshot)
}

// NewNoteSnapshotCollector returns this scenario's isolated note collector.
func (s Scenario) NewNoteSnapshotCollector() *NoteSnapshotCollector {
	return NewNoteSnapshotCollector(s.Notes)
}

// NewSettingsService returns this scenario's repository settings service.
func (s Scenario) NewSettingsService() *SettingsService {
	return NewSettingsService(s.Settings)
}

// NewDebugSnapshotCollector returns this scenario's diagnostics collector.
func (s Scenario) NewDebugSnapshotCollector() *DebugSnapshotCollector {
	return NewDebugSnapshotCollector(s.Debug)
}

// NewDetailRepository returns this scenario's isolated detail repository.
func (s Scenario) NewDetailRepository() *DetailRepository {
	return NewDetailRepository(s.Plans)
}

// Render builds one frame through the production TUI renderers.
func Render(scenario Scenario, options RenderOptions) (string, error) {
	if options.Width <= 0 {
		return "", errors.New("preview width must be greater than zero")
	}
	if options.Height <= 0 {
		return "", errors.New("preview height must be greater than zero")
	}
	if options.Selection < 0 {
		return "", errors.New("preview selection must not be negative")
	}

	var frame string
	switch options.View {
	case ViewPlans:
		count := visiblePlanCount(scenario.Snapshot, options.HideCompleted, options.SearchQuery)
		if err := validateSelection(options.Selection, count, "plan"); err != nil {
			return "", err
		}
		frame = tui.Render(tui.Model{
			Snapshot: scenario.Snapshot, Page: tui.PagePlans, Selected: options.Selection,
			Width: options.Width, Height: options.Height, Now: scenario.Now,
			HideCompleted: options.HideCompleted, UseColor: options.Color, ShowShortcuts: options.ShowShortcuts, SearchQuery: options.SearchQuery,
		})
	case ViewNotes:
		filteredNotes := tui.FilterNoteSnapshot(scenario.Notes, options.SearchQuery)
		if err := validateSelection(options.Selection, len(filteredNotes.Notes), "note"); err != nil {
			return "", err
		}
		frame = tui.Render(tui.Model{
			Snapshot: scenario.Snapshot, NoteSnapshot: scenario.Notes, Page: tui.PageNotes,
			Selected: options.Selection, Width: options.Width, Height: options.Height,
			Now: scenario.Now, UseColor: options.Color, ShowShortcuts: options.ShowShortcuts, SearchQuery: options.SearchQuery,
		})
	case ViewSettings:
		if options.SearchQuery != "" {
			return "", errors.New("search preview is available only for plans and notes views")
		}
		if err := validateSelection(options.Selection, len(scenario.Settings.Repositories), "repository"); err != nil {
			return "", err
		}
		frame = tui.Render(tui.Model{
			SettingsSnapshot: scenario.Settings, Page: tui.PageSettings, Selected: options.Selection,
			Width: options.Width, Height: options.Height, Now: scenario.Now, UseColor: options.Color, ShowShortcuts: options.ShowShortcuts,
		})
	case ViewDebug:
		if options.SearchQuery != "" {
			return "", errors.New("search preview is available only for plans and notes views")
		}
		frame = tui.Render(tui.Model{
			Snapshot: scenario.Snapshot, NoteSnapshot: scenario.Notes, DebugSnapshot: scenario.Debug, Page: tui.PageDebug,
			Width: options.Width, Height: options.Height, Now: scenario.Now, UseColor: options.Color, ShowShortcuts: options.ShowShortcuts,
		})
	case ViewPlanDetail:
		if options.SearchQuery != "" {
			return "", errors.New("search preview is available only for plans and notes views")
		}
		fixture, err := detailFixture(scenario, options.PlanDir)
		if err != nil {
			return "", err
		}
		slices := orderedFixtureSlices(&fixture.Detail)
		selected, err := selectSlice(slices, options, false)
		if err != nil {
			return "", err
		}
		frame = tui.RenderDetail(tui.DetailModel{
			Plan: &fixture.Detail, Row: detailRow(scenario.Snapshot, fixture), Log: fixture.Log,
			SelectedSliceID: selected, Width: options.Width, Height: options.Height, UseColor: options.Color,
			ShowShortcuts: options.ShowShortcuts,
		})
	case ViewNoteDetail:
		if options.ShowShortcuts {
			return "", errors.New("shortcut popover preview is available only for plans and notes views")
		}
		if options.SearchQuery != "" {
			return "", errors.New("search preview is available only for plans and notes views")
		}
		if len(scenario.Notes.Notes) == 0 {
			return "", errors.New("preview note detail is unavailable because the fixture has no notes")
		}
		if err := validateSelection(options.Selection, len(scenario.Notes.Notes), "note"); err != nil {
			return "", err
		}
		frame = tui.RenderNoteDetail(scenario.Notes.Notes[options.Selection], options.Width, options.Height)
	case ViewSliceDetail:
		if options.SearchQuery != "" {
			return "", errors.New("search preview is available only for plans and notes views")
		}
		fixture, err := detailFixture(scenario, options.PlanDir)
		if err != nil {
			return "", err
		}
		slices := orderedFixtureSlices(&fixture.Detail)
		selected, err := selectSlice(slices, options, true)
		if err != nil {
			return "", err
		}
		frame = tui.RenderSliceDetail(tui.DetailModel{
			Plan: &fixture.Detail, Log: fixture.Log, SelectedSliceID: selected,
			Width: options.Width, Height: options.Height, UseColor: options.Color, ShowShortcuts: options.ShowShortcuts,
		})
	default:
		return "", fmt.Errorf("unknown preview view %q", options.View)
	}
	if options.Plain {
		frame = PlainFrame(frame)
	}
	return frame, nil
}

// PlainFrame removes only leading terminal screen/cursor controls. ANSI SGR
// styling in the visible frame is intentionally preserved.
func PlainFrame(frame string) string {
	prefixes := []string{
		"\x1b[?1049h", // enter alternate screen
		"\x1b[?1049l", // leave alternate screen
		"\x1b[?25l",   // hide cursor
		"\x1b[?25h",   // show cursor
		"\x1b[H",      // cursor home
		"\x1b[2J",     // clear screen
	}
	for {
		changed := false
		for _, prefix := range prefixes {
			if strings.HasPrefix(frame, prefix) {
				frame = strings.TrimPrefix(frame, prefix)
				changed = true
			}
		}
		if !changed {
			return frame
		}
	}
}

func selectSlice(slices []plan.Slice, options RenderOptions, required bool) (string, error) {
	if options.SliceID != "" {
		for _, slice := range slices {
			if slice.ID == options.SliceID {
				return slice.ID, nil
			}
		}
		return "", fmt.Errorf("unknown preview slice %q", options.SliceID)
	}
	if len(slices) == 0 && required {
		return "", errors.New("preview plan has no slice details")
	}
	if err := validateSelection(options.Selection, len(slices), "slice"); err != nil {
		return "", err
	}
	if len(slices) == 0 {
		return "", nil
	}
	return slices[options.Selection].ID, nil
}

func validateSelection(selection, count int, kind string) error {
	if count == 0 {
		if selection == 0 {
			return nil
		}
		return fmt.Errorf("preview %s selection %d is unavailable in an empty fixture", kind, selection)
	}
	if selection >= count {
		return fmt.Errorf("preview %s selection %d is outside 0..%d", kind, selection, count-1)
	}
	return nil
}

func visiblePlanCount(snapshot monitor.Snapshot, hideCompleted bool, searchQuery string) int {
	count := 0
	for _, section := range tui.BuildSections(tui.FilterPlanRows(snapshot.Rows, searchQuery), !hideCompleted) {
		count += len(section.Rows)
	}
	return count
}

func detailFixture(scenario Scenario, planDir string) (PlanFixture, error) {
	if planDir == "" {
		if len(scenario.Plans) == 0 {
			return PlanFixture{}, errors.New("preview scenario has no plan details")
		}
		return scenario.Plans[0], nil
	}
	for _, fixture := range scenario.Plans {
		if fixture.PlanDir == planDir {
			return fixture, nil
		}
	}
	return PlanFixture{}, fmt.Errorf("%w: %s", ErrUnknownPlan, planDir)
}

func detailRow(snapshot monitor.Snapshot, fixture PlanFixture) monitor.Row {
	for _, row := range snapshot.Rows {
		if row.PlanDir == fixture.PlanDir {
			return row
		}
	}
	return monitor.Row{
		Kind: monitor.RowKindPlan, RepositoryName: fixture.Detail.State.Repo.Name,
		RepositoryRoot: fixture.Detail.State.Repo.Root, PlanID: fixture.Detail.State.Plan.ID,
		PlanTitle: fixture.Detail.State.Plan.Title, PlanDir: fixture.PlanDir, Status: fixture.Detail.State.Status,
	}
}

func orderedFixtureSlices(detail *plan.PlanDetail) []plan.Slice {
	byID := make(map[string]plan.Slice, len(detail.Slices.Slices))
	for _, slice := range detail.Slices.Slices {
		byID[slice.ID] = slice
	}
	ids := append([]string(nil), detail.State.Plan.CompletedSlices...)
	ids = append(ids, detail.State.Plan.PendingSlices...)
	if detail.State.Plan.CurrentSlice != nil {
		ids = append(ids, *detail.State.Plan.CurrentSlice)
	}
	seen := make(map[string]bool, len(ids))
	result := make([]plan.Slice, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		if slice, ok := byID[id]; ok {
			result = append(result, slice)
		}
	}
	return result
}

func cloneMonitorSnapshot(source monitor.Snapshot) monitor.Snapshot {
	result := source
	result.Rows = append([]monitor.Row(nil), source.Rows...)
	for index := range result.Rows {
		row := &result.Rows[index]
		if row.UpdatedAt != nil {
			value := *row.UpdatedAt
			row.UpdatedAt = &value
		}
		row.AttentionReasons = append([]monitor.AttentionReason(nil), row.AttentionReasons...)
		row.Warnings = append([]string(nil), row.Warnings...)
	}
	return result
}

func cloneNoteSnapshot(source note.Snapshot) note.Snapshot {
	result := source
	result.Notes = append([]note.CatalogNote(nil), source.Notes...)
	for index := range result.Notes {
		result.Notes[index].Tags = append([]string(nil), result.Notes[index].Tags...)
	}
	result.Warnings = append([]note.CatalogWarning(nil), source.Warnings...)
	return result
}

func cloneSettingsSnapshot(source tui.SettingsSnapshot) tui.SettingsSnapshot {
	result := source
	result.RuntimeDefaults = append([]tui.SettingsRuntimeDefault(nil), source.RuntimeDefaults...)
	result.Repositories = append([]tui.RepositorySetting(nil), source.Repositories...)
	for index := range result.Repositories {
		result.Repositories[index].PullRequest = cloneBool(result.Repositories[index].PullRequest)
	}
	return result
}

func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func cloneDebugSnapshot(source tui.DebugSnapshot) tui.DebugSnapshot {
	result := source
	result.System = append([]tui.DebugValue(nil), source.System...)
	result.InstalledAgents = append([]string(nil), source.InstalledAgents...)
	result.DoctorProblems = append([]tui.DebugProblem(nil), source.DoctorProblems...)
	result.RuntimeDefaults = append([]tui.DebugRuntimeDefault(nil), source.RuntimeDefaults...)
	return result
}

func clonePlanDetail(source plan.PlanDetail) plan.PlanDetail {
	result := source
	result.State.GlobalInvariants = append([]string(nil), source.State.GlobalInvariants...)
	result.State.OpenQuestions = append([]string(nil), source.State.OpenQuestions...)
	result.State.Plan.CompletedSlices = append([]string(nil), source.State.Plan.CompletedSlices...)
	result.State.Plan.PendingSlices = append([]string(nil), source.State.Plan.PendingSlices...)
	result.State.Plan.LastRunStartingDirty = append([]string(nil), source.State.Plan.LastRunStartingDirty...)
	if source.State.Plan.CurrentSlice != nil {
		current := *source.State.Plan.CurrentSlice
		result.State.Plan.CurrentSlice = &current
	}
	result.Slices.Slices = append([]plan.Slice(nil), source.Slices.Slices...)
	for index := range result.Slices.Slices {
		slice := &result.Slices.Slices[index]
		slice.Tags = append([]string(nil), slice.Tags...)
		slice.DependsOn = append([]string(nil), slice.DependsOn...)
		slice.Tasks = append([]string(nil), slice.Tasks...)
		slice.ExpectedFiles = append([]string(nil), slice.ExpectedFiles...)
		slice.RequiredInputs = append([]plan.RequiredInput(nil), slice.RequiredInputs...)
		slice.Verification.Commands = append([]string(nil), slice.Verification.Commands...)
		slice.Verification.Steps = append([]plan.VerificationStep(nil), slice.Verification.Steps...)
		slice.Verification.ManualChecks = append([]string(nil), slice.Verification.ManualChecks...)
		slice.VerificationResults = append([]plan.VerificationRun(nil), slice.VerificationResults...)
		if slice.Approval != nil {
			approval := *slice.Approval
			slice.Approval = &approval
		}
		if slice.Completion != nil {
			completion := *slice.Completion
			slice.Completion = &completion
		}
		slice.Timing.StartedAt = cloneTime(slice.Timing.StartedAt)
		slice.Timing.CompletedAt = cloneTime(slice.Timing.CompletedAt)
		slice.Timing.LastActivityAt = cloneTime(slice.Timing.LastActivityAt)
		if slice.Timing.DurationSeconds != nil {
			duration := *slice.Timing.DurationSeconds
			slice.Timing.DurationSeconds = &duration
		}
	}
	result.Events = append([]plan.Event(nil), source.Events...)
	result.Warnings = append([]string(nil), source.Warnings...)
	return result
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

var (
	_ tui.SnapshotCollector     = (*SnapshotCollector)(nil)
	_ tui.NoteSnapshotCollector = (*NoteSnapshotCollector)(nil)
	_ tui.DetailRepository      = (*DetailRepository)(nil)
)
