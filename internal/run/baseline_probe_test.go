package run

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/commandrunner"
	"github.com/iamseth/tao/internal/gitops"
	"github.com/iamseth/tao/internal/plan"
)

func TestBaselineProbe(t *testing.T) {
	const output = "--- FAIL: TestX (0.00s)\nFAIL\tgithub.com/iamseth/tao/internal/tui\n"
	var packages, paths strings.Builder
	for i := range 64 {
		fmt.Fprintf(&packages, "FAIL\tgithub.com/iamseth/tao/internal/outside%d\n", i)
		fmt.Fprintf(&paths, "internal/outside%d/base.go:2:3: broken\n", i)
	}
	const overlappingBase = "--- FAIL: TestX (0.00s)\nFAIL\tgithub.com/iamseth/tao/internal/outside0\n"
	for _, tc := range []struct {
		name, head, base string
		ownership        string
		baseErr          error
		owned, baseline  bool
		calls            int
	}{
		{name: "base passes", head: output, calls: 1},
		{name: "owned package after extraction cap", head: packages.String() + output, owned: true, base: overlappingBase, baseErr: errors.New("exit 1")},
		{name: "owned path after extraction cap", head: paths.String() + "internal/tui/owned.go:2:3: broken\n" + overlappingBase, owned: true, base: overlappingBase, baseErr: errors.New("exit 1")},
		{name: "saturated packages", head: packages.String(), base: overlappingBase, baseErr: errors.New("exit 1")},
		{name: "saturated paths", head: paths.String() + overlappingBase, base: overlappingBase, baseErr: errors.New("exit 1")},
		{name: "missing ownership base", ownership: "missing", head: output, owned: true, base: output, baseErr: errors.New("exit 1")},
		{name: "failed ownership diff", ownership: "invalid", head: output, owned: true, base: output, baseErr: errors.New("exit 1")},
		{name: "empty ownership diff", ownership: "head", head: output, base: output, baseErr: errors.New("exit 1"), baseline: true, calls: 1},
		{name: "overlap", head: output, base: output, baseErr: errors.New("exit 1"), baseline: true, calls: 1},
		{name: "different signatures", head: output, base: "--- FAIL: TestY (0.00s)\nFAIL\tgithub.com/iamseth/tao/internal/other\n", baseErr: errors.New("exit 1"), calls: 1},
		{name: "deadline", head: output, base: output, baseErr: context.DeadlineExceeded, calls: 1},
		{name: "no package or path", head: "--- FAIL: TestX (0.00s)\n"},
		{name: "owned package", head: output, owned: true},
		{name: "unmappable package", head: "FAIL\texample.org/foreign/pkg\n"},
		{name: "cancelled", head: output, base: output, baseErr: context.Canceled, calls: 1},
		{name: "outside diagnostic path", head: "internal/tui/base.go:2:3: broken\n", calls: 1},
		{name: "owned diagnostic path", head: "ci.yml:2:3: broken\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := initSliceCompletionRepo(t)
			for path, data := range map[string]string{"go.mod": "module github.com/iamseth/tao\n", "Makefile": "verify:\n\tfalse\n", "internal/tui/base.go": "package tui\n"} {
				full := filepath.Join(root, path)
				if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(full, []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			runCommitTestGitCommand(t, root, "add", ".")
			runCommitTestGitCommand(t, root, "commit", "-m", "base")
			git := gitops.NewClient(root, nil)
			base, err := git.RevParse(context.Background(), "HEAD")
			if err != nil {
				t.Fatal(err)
			}
			runCommitTestGitCommand(t, root, "branch", "-f", "main", base)
			branch, err := git.CurrentBranch(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			runCommitTestGitCommand(t, root, "checkout", "-b", "probe-plan")
			changed := "ci.yml"
			if tc.owned {
				changed = "internal/tui/owned.go"
			}
			if err := os.WriteFile(filepath.Join(root, changed), []byte("change"), 0o600); err != nil {
				t.Fatal(err)
			}
			runCommitTestGitCommand(t, root, "add", ".")
			runCommitTestGitCommand(t, root, "commit", "-m", "head")
			detail := completedReviewPlanDetail(t.TempDir())
			detail.State.Repo.Root = root
			detail.State.Repo.BaseCommit = base
			switch tc.ownership {
			case "missing":
				detail.State.Repo.BaseCommit = ""
			case "invalid":
				detail.State.Repo.BaseCommit = "missing-ownership-revision"
			case "head":
				detail.State.Repo.BaseCommit, err = git.RevParse(context.Background(), "HEAD")
				if err != nil {
					t.Fatal(err)
				}
			}
			detail.State.Workspace.Branch = "probe-plan"
			detail.State.Workspace.BaseBranch = branch
			detail.State.Workspace.BaseSHA = base
			calls := 0
			var probeDir string
			var events []plan.Event
			runner := func(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
				if name != "sh" {
					return commandrunner.DefaultLocal(ctx, cwd, name, args, stdout, stderr)
				}
				if strings.Join(args, " ") != "-c make verify" {
					t.Fatalf("command=%v", args)
				}
				if cwd == root {
					_, _ = fmt.Fprint(stdout, tc.head)
					return errors.New("head failed")
				}
				calls++
				probeDir = cwd
				_, _ = fmt.Fprint(stdout, tc.base)
				return tc.baseErr
			}
			f := newFinalizer(io.Discard, testRunExecution(ExecutionConfig{}, RunDependencies{CommandRunner: runner, reviewGitFactory: newReviewGitFactory(runner), PlanRecordFactory: memoryPlanRecordFactory, EventAppender: eventAppenderFunc(func(_ string, e plan.Event) error { events = append(events, e); return nil })}))
			failure := f.verifyCompletedBranch(context.Background(), detail, root)
			if failure == nil {
				t.Fatal("expected failure")
			}
			v := detail.State.Plan.FinalVerification
			if calls != tc.calls {
				t.Fatalf("base calls=%d want %d", calls, tc.calls)
			}
			if tc.baseline {
				if v.FailureKind != plan.FinalVerificationFailureKindBaseline || v.Baseline == nil || v.Baseline.SHA != base || v.Baseline.Source != "live_merge_base" || !slices.Contains(v.Baseline.Signatures, "TestX") {
					t.Fatalf("verification=%+v baseline=%+v", v, v.Baseline)
				}
				if !strings.Contains(failure.Error(), "baseline: also fails at "+base[:12]) {
					t.Fatal(failure)
				}
				_ = f.handleFinalVerificationFailure(context.Background(), 1, detail, root, failure)
				found := false
				for _, e := range events {
					if e.Type == plan.EventTypeVerificationRepairCreated {
						t.Fatal("scheduled repair")
					}
					if e.Type == plan.EventTypeFinalVerification {
						found = true
						if e.Baseline == nil {
							t.Fatal("missing event baseline")
						}
					}
				}
				if !found {
					t.Fatal("missing verification event")
				}
			} else if v.FailureKind != plan.FinalVerificationFailureKindCode || v.Baseline != nil {
				t.Fatalf("verification=%+v", v)
			}
			if probeDir != "" {
				if _, err := os.Stat(probeDir); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("probe remains: %v", err)
				}
			}
		})
	}
}
