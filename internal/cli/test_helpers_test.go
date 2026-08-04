package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/internal/taodata"
)

var suiteTaoDataHome string

func TestMain(m *testing.M) {
	for _, key := range testTaoEnvKeys() {
		_ = os.Unsetenv(key)
	}

	dataHome, err := os.MkdirTemp("", "tao-cli-data-home-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "cli test setup: create Tao data home: %v\n", err)
		os.Exit(1)
	}
	if err := os.Setenv("TAO_DATA_HOME", dataHome); err != nil {
		_ = os.RemoveAll(dataHome)
		fmt.Fprintf(os.Stderr, "cli test setup: set TAO_DATA_HOME: %v\n", err)
		os.Exit(1)
	}
	suiteTaoDataHome = dataHome

	installTestPiRPC()
	code := m.Run()
	if err := os.RemoveAll(dataHome); err != nil {
		fmt.Fprintf(os.Stderr, "cli test cleanup: remove Tao data home: %v\n", err)
	}
	os.Exit(code)
}

func TestCLIDataHomeIsSuiteOwned(t *testing.T) {
	if got := taodata.DataHome(); got != suiteTaoDataHome {
		t.Fatalf("default data home = %q, want suite data home %q", got, suiteTaoDataHome)
	}

	t.Run("temporary override", func(t *testing.T) {
		override := t.TempDir()
		t.Setenv("TAO_DATA_HOME", override)
		if got := taodata.DataHome(); got != override {
			t.Fatalf("overridden data home = %q, want %q", got, override)
		}
	})

	if got := taodata.DataHome(); got != suiteTaoDataHome {
		t.Fatalf("restored data home = %q, want suite data home %q", got, suiteTaoDataHome)
	}
}

func installTestPiRPC() {
	dir, err := os.MkdirTemp("", "tao-cli-pi-")
	if err != nil {
		return
	}
	path := filepath.Join(dir, "pi")
	script := `#!/usr/bin/env python3
import json, os, re, sys
line = sys.stdin.readline()
try:
    cmd = json.loads(line or '{}')
except Exception:
    cmd = {}
prompt = cmd.get('message','')
match = re.search(r'Plan directory: '+chr(96)+'([^'+chr(96)+']+)'+chr(96), prompt)
if match:
    plan_dir = match.group(1)
    state_path = os.path.join(plan_dir, 'state.json')
    slices_path = os.path.join(plan_dir, 'slices.json')
    try:
        state = json.load(open(state_path))
        slices = json.load(open(slices_path))
        current = state.get('plan', {}).get('current_slice') or (state.get('plan', {}).get('pending_slices') or [''])[0]
        if current:
            for item in slices.get('slices', []):
                if item.get('id') == current:
                    item['status'] = 'completed'
            plan = state.setdefault('plan', {})
            plan['pending_slices'] = [s for s in plan.get('pending_slices', []) if s != current]
            completed = plan.setdefault('completed_slices', [])
            if current not in completed:
                completed.append(current)
            plan['current_slice'] = None
            if not plan.get('pending_slices'):
                state['status'] = 'completed'
            json.dump(state, open(state_path, 'w'))
            json.dump(slices, open(slices_path, 'w'))
    except Exception:
        pass
print(json.dumps({'type':'message','role':'assistant','text':'pi test output'}), flush=True)
print(json.dumps({'type':'agent_end','session_id':'test-session'}), flush=True)
for line in sys.stdin:
    try:
        cmd = json.loads(line or '{}')
    except Exception:
        cmd = {}
    typ = cmd.get('type')
    if typ == 'get_state':
        print(json.dumps({'type':'state','session_id':'test-session'}), flush=True)
    elif typ == 'get_session_stats':
        print(json.dumps({'type':'session_stats','session_id':'test-session'}), flush=True)
    elif typ == 'abort':
        break
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil { //nolint:gosec // G306: executable test stub needs exec bit
		return
	}
	_ = os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

type fakeRepository struct {
	summaries []plan.PlanSummary
	details   map[string]*plan.PlanDetail
	err       error
}

type notifyingBuffer struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	want   string
	done   chan struct{}
	closed bool
}

func newNotifyingBuffer(want string) *notifyingBuffer {
	return &notifyingBuffer{want: want, done: make(chan struct{})}
}

func (b *notifyingBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n, err := b.buf.Write(p)
	if !b.closed && strings.Contains(b.buf.String(), b.want) {
		close(b.done)
		b.closed = true
	}
	return n, err
}

func (b *notifyingBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (f fakeRepository) ListPlans(ctx context.Context, filter plan.PlanFilter) ([]plan.PlanSummary, error) {
	if f.err != nil {
		return nil, f.err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make([]plan.PlanSummary, 0, len(f.summaries))
	for _, summary := range f.summaries {
		if filter.ActiveOnly && !summary.Active() {
			continue
		}
		out = append(out, summary)
	}
	return out, nil
}

func (f fakeRepository) GetPlan(ctx context.Context, id string) (*plan.PlanDetail, error) {
	if f.err != nil {
		return nil, f.err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return f.details[id], nil
}

func (f fakeRepository) ResolvePlan(ctx context.Context, input string) (*plan.PlanDetail, error) {
	return f.GetPlan(ctx, input)
}

func (f fakeRepository) PlanRecord(detail *plan.PlanDetail) (*plan.PlanRecord, error) {
	dir := detail.Dir
	if dir == "" {
		dir = "/plantest/" + detail.State.Plan.ID
		detail.Dir = dir
	}
	return plan.NewPlanRecordWithStore(f, dir, detail)
}

func (f fakeRepository) ResolvePlanRecord(ctx context.Context, input string) (*plan.PlanRecord, error) {
	detail, err := f.ResolvePlan(ctx, input)
	if err != nil {
		return nil, err
	}
	return f.PlanRecord(detail)
}

func (f fakeRepository) DeletePlan(ctx context.Context, input string, opts plan.DeletePlanOptions) (*plan.DeletePlanResult, error) {
	if f.err != nil {
		return nil, f.err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &plan.DeletePlanResult{ID: input, Dir: input}, nil
}

func (f fakeRepository) OpenLogAppend(_ string) (*os.File, error) {
	return os.OpenFile(os.DevNull, os.O_WRONLY, 0)
}

func (f fakeRepository) ReadLog(_ string) (string, error)            { return "", nil }
func (f fakeRepository) ReadLogTail(_ string, _ int) (string, error) { return "", nil }
func (f fakeRepository) FollowLog(ctx context.Context, _ string, _ io.Writer) error {
	return ctx.Err()
}
func (f fakeRepository) WriteState(_ string, _ plan.State) error       { return nil }
func (f fakeRepository) WriteSlices(_ string, _ plan.SlicesFile) error { return nil }
func (f fakeRepository) AppendEvent(_ string, _ plan.Event) error      { return nil }

func clearTaoEnv(t *testing.T) {
	t.Helper()
	for _, key := range testTaoEnvKeys() {
		t.Setenv(key, "")
	}
}

func testTaoEnvKeys() []string {
	return taoEnvKeys()
}

func taoEnvKeys() []string {
	return runtimeconfig.RuntimeEnvKeys()
}

type runPlanFixture struct {
	root string
	id   string
	dir  string
	t    *testing.T
}

func newRunPlanFixture(t *testing.T, status string, pending []string, completed []string, sliceID string, sliceStatus string) runPlanFixture {
	t.Helper()
	fixture := runPlanFixture{root: t.TempDir(), id: "20260430-1200-run-plan", t: t}
	fixture.write(status, pending, completed, sliceID, sliceStatus)
	return fixture
}

func (f *runPlanFixture) write(status string, pending []string, completed []string, sliceID string, sliceStatus string) {
	f.t.Helper()
	f.dir = writeRunPlan(f.t, f.root, f.id, status, pending, completed, sliceID, sliceStatus)
}

func writeRunPlan(t *testing.T, root, planID, status string, pending []string, completed []string, sliceID string, sliceStatus string) string {
	t.Helper()
	planDir := filepath.Join(root, planID)
	if err := os.MkdirAll(planDir, 0o750); err != nil {
		t.Fatal(err)
	}
	repoRoot, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	pendingJSON := quoteList(pending)
	completedJSON := quoteList(completed)
	state := `{
  "schema":"tao.plan.state.v1",
  "status":"` + status + `",
  "created_at":"2026-04-30T12:00:00Z",
  "updated_at":"2026-04-30T12:00:00Z",
	"repo":{"name":"tao","root":` + fmt.Sprintf("%q", repoRoot) + `,"branch":"feature"},
	"workspace":{"strategy":"current"},
  "plan":{"id":"` + planID + `","title":"Run Plan","current_slice":null,"completed_slices":` + completedJSON + `,"pending_slices":` + pendingJSON + `,"timing":{"started_at":null,"completed_at":null,"last_activity_at":"2026-04-30T12:00:00Z"}},
  "global_invariants":[],"open_questions":[]
}`
	slices := `{
  "schema":"tao.plan.slices.v1",
  "plan_id":"` + planID + `",
  "execution":{"mode":"serial","parallel_safe":false},
  "slices":[{"id":"` + sliceID + `","title":"A","status":"` + sliceStatus + `","depends_on":[],"timing":{"created_at":"2026-04-30T12:00:00Z","started_at":null,"completed_at":null,"updated_at":"2026-04-30T12:00:00Z","last_activity_at":null,"duration_seconds":null},"goal":"","context":"","tasks":[],"expected_files":[],"verification":{"commands":["go test ./internal/cli"],"manual_checks":[]}}]
}`
	if err := os.WriteFile(filepath.Join(planDir, "state.json"), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(planDir, "slices.json"), []byte(slices), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(planDir, "events.jsonl")); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(filepath.Join(planDir, "events.jsonl"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return planDir
}

func quoteList(values []string) string {
	if values == nil {
		return "[]"
	}
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, `"`+value+`"`)
	}
	return "[" + strings.Join(quoted, ",") + "]"
}

func readText(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path) //nolint:gosec // G304: test-controlled file path
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}
