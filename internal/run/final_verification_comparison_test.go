package run

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/commandrunner"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/testenv"
)

func contractComparisonFixture(t *testing.T) (string, *plan.PlanDetail) {
	t.Helper()
	root, detail := sameHeadRerunFixture(t)
	detail.State.Repo.BaseCommit = strings.TrimSpace(runCommitTestGitOutput(t, root, "rev-parse", "HEAD~1"))
	detail.State.Workspace = &plan.Workspace{HeadSHA: strings.TrimSpace(runCommitTestGitOutput(t, root, "rev-parse", "HEAD")), Branch: strings.TrimSpace(runCommitTestGitOutput(t, root, "branch", "--show-current")), Path: root, Strategy: plan.WorkspaceStrategyWorktree}
	detail.State.Plan.FinalVerificationContract = &plan.FinalVerificationContract{Revision: 1, Command: "make verify", CWD: ".", Purpose: "tests", Coverage: []string{"repository"}, Prerequisites: []plan.FinalVerificationPrerequisite{{Kind: "file", Path: "Makefile"}}, PermittedEffects: []string{"build outputs"}, ComparisonPermitted: true, Provenance: "owner"}
	return root, detail
}

func TestFinalVerificationContractComparison(t *testing.T) {
	testenv.SkipIfShort(t)
	exitErr := exec.Command("sh", "-c", "exit 1").Run()
	if exitErr == nil {
		t.Fatal("expected observed exit")
	}
	for _, tc := range []struct {
		name               string
		calls              int
		kind               plan.FinalVerificationFailureKind
		passed, admissible bool
	}{
		{"regression", 3, plan.FinalVerificationFailureKindCode, false, true},
		{"relative cwd", 3, plan.FinalVerificationFailureKindCode, false, true},
		{"changed executable", 1, plan.FinalVerificationFailureKindUnknown, false, false},
		{"base missing prerequisite", 2, plan.FinalVerificationFailureKindUnknown, false, false},
		{"nonancestor base", 1, plan.FinalVerificationFailureKindUnknown, false, false},
		{"cancelled base", 3, plan.FinalVerificationFailureKindUnknown, false, false},
		{"no permission", 1, plan.FinalVerificationFailureKindUnknown, false, false},
		{"no effects", 1, plan.FinalVerificationFailureKindUnknown, false, false},
		{"no prerequisites", 1, plan.FinalVerificationFailureKindUnknown, false, false},
		{"flake", 2, "", true, false},
		{"baseline", 3, plan.FinalVerificationFailureKindBaseline, false, false},
		{"disjoint baseline", 3, plan.FinalVerificationFailureKindUnknown, false, false},
		{"mismatch", 2, plan.FinalVerificationFailureKindUnknown, false, false},
		{"unsupported", 0, plan.FinalVerificationFailureKindUnknown, false, false},
		{"missing prerequisite", 0, plan.FinalVerificationFailureKindPrerequisiteMissing, false, false},
		{"changed file", 1, plan.FinalVerificationFailureKindUnknown, false, false},
		{"missing cwd", 0, plan.FinalVerificationFailureKindInvalidContract, false, false},
		{"missing base", 1, plan.FinalVerificationFailureKindUnknown, false, false},
		{"equal base", 1, plan.FinalVerificationFailureKindUnknown, false, false},
		{"dirty", 1, plan.FinalVerificationFailureKindUnknown, false, false},
		{"moving head", 1, plan.FinalVerificationFailureKindUnknown, false, false},
		{"cancelled", 2, plan.FinalVerificationFailureKindUnknown, false, false},
		{"cap", 1, plan.FinalVerificationFailureKindUnknown, false, false},
		{"reverify", 1, plan.FinalVerificationFailureKindUnknown, false, false},
		{"invocation boundary", 1, plan.FinalVerificationFailureKindUnknown, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, detail := sameHeadRerunFixture(t)
			head := strings.TrimSpace(runCommitTestGitOutput(t, root, "rev-parse", "HEAD"))
			base := strings.TrimSpace(runCommitTestGitOutput(t, root, "rev-parse", "HEAD~1"))
			branch := strings.TrimSpace(runCommitTestGitOutput(t, root, "branch", "--show-current"))
			detail.State.Repo.BaseCommit = base
			detail.State.Workspace = &plan.Workspace{HeadSHA: head, Branch: branch, Path: root}
			c := &plan.FinalVerificationContract{Revision: 1, Command: "make verify", CWD: ".", Purpose: "tests", Coverage: []string{"repository"}, Prerequisites: []plan.FinalVerificationPrerequisite{{Kind: "file", Path: "Makefile"}}, PermittedEffects: []string{"build outputs"}, ComparisonPermitted: true, Provenance: "owner"}
			detail.State.Plan.FinalVerificationContract = c
			executable := ""
			switch tc.name {
			case "relative cwd":
				c.CWD = "outside"
			case "changed executable":
				bin := t.TempDir()
				executable = filepath.Join(bin, "contract-tool")
				if err := os.WriteFile(executable, []byte("original"), 0700); err != nil {
					t.Fatal(err)
				} // #nosec G306 -- executable prerequisite fixture.
				t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
				c.Prerequisites = []plan.FinalVerificationPrerequisite{{Kind: "executable", Name: "contract-tool"}}
			case "base missing prerequisite":
				c.Prerequisites[0].Path = "owned.txt"
			case "nonancestor base":
				detail.State.Repo.BaseCommit = strings.TrimSpace(runCommitTestGitOutput(t, root, "commit-tree", "HEAD^{tree}", "-m", "unrelated"))
			case "no permission":
				c.ComparisonPermitted = false
			case "no effects":
				c.PermittedEffects = nil
			case "no prerequisites":
				c.Prerequisites = nil
			case "unsupported":
				c.Prerequisites[0].Kind = "service"
			case "missing prerequisite":
				c.Prerequisites[0].Path = "missing"
			case "missing cwd":
				c.CWD = "missing"
			case "missing base":
				detail.State.Repo.BaseCommit = ""
			case "equal base":
				detail.State.Repo.BaseCommit = head
			case "dirty":
				if err := os.WriteFile(filepath.Join(root, "dirty"), []byte("dirty"), 0600); err != nil {
					t.Fatal(err)
				}
			case "cap":
				for i := 0; i < plan.VerificationRepairAttemptCap; i++ {
					detail.Slices.Slices = append(detail.Slices.Slices, plan.Slice{ID: strings.Repeat("v", i+1), Status: plan.StatusCompleted, VerificationRepair: &plan.VerificationRepairBinding{Command: "make verify", HeadSHA: "old", Fingerprint: "old"}})
				}
			}
			calls := 0
			var detachedRoot string
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			runner := func(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
				if name != "sh" {
					return commandrunner.DefaultLocal(ctx, cwd, name, args, stdout, stderr)
				}
				calls++
				if strings.Join(args, " ") != "-c make verify" {
					t.Fatalf("command %v", args)
				}
				if calls <= 2 && cwd != filepath.Join(root, c.CWD) {
					t.Fatalf("head cwd %s", cwd)
				}
				if calls == 3 {
					if cwd == filepath.Join(root, c.CWD) {
						t.Fatal("base gate ran in head checkout")
					}
					detachedRoot = cwd
					if c.CWD != "." {
						detachedRoot = filepath.Dir(cwd)
					}
				}
				if tc.name == "changed executable" {
					if err := os.WriteFile(executable, []byte("replacement"), 0700); err != nil {
						t.Fatal(err)
					}
				} // #nosec G306 -- executable prerequisite fixture.
				if tc.name == "cancelled base" && calls == 3 {
					cancel()
					return context.Canceled
				}
				if tc.name == "changed file" {
					if err := os.WriteFile(filepath.Join(root, "Makefile"), []byte("changed"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if tc.name == "moving head" {
					runCommitTestGitCommand(t, root, "commit", "--allow-empty", "-m", "move")
				}
				if tc.name == "cancelled" && calls == 2 {
					cancel()
					return context.Canceled
				}
				if tc.name == "flake" && calls == 2 {
					return nil
				}
				if calls == 3 && tc.name != "baseline" && tc.name != "disjoint baseline" {
					return nil
				}
				output := "--- FAIL: TestRegression (0.00s)\n"
				if tc.name == "mismatch" && calls == 2 || tc.name == "disjoint baseline" && calls == 3 {
					output = "--- FAIL: TestOther (0.00s)\n"
				}
				_, _ = io.WriteString(stdout, output)
				return exitErr
			}
			execution := testRunExecution(ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicySlice, ExecutionMode: ExecutionModeIsolated}}, RunDependencies{CommandRunner: runner, reviewGitFactory: newReviewGitFactory(runner), PlanRecordFactory: memoryPlanRecordFactory})
			execution.Request.Reverify = tc.name == "reverify"
			f := newFinalizer(io.Discard, execution)
			err := f.verifyCompletedBranchWithComparison(ctx, detail, root, tc.name != "invocation boundary")
			var failure *FinalVerificationError
			if !tc.passed && !errors.As(err, &failure) || tc.passed && err != nil {
				t.Fatalf("result: %v", err)
			}
			if detachedRoot != "" {
				if _, statErr := os.Stat(detachedRoot); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("detached checkout retained: %v", statErr)
				}
			}
			v := detail.State.Plan.FinalVerification
			if v == nil || v.FailureKind != tc.kind || calls != tc.calls {
				t.Fatalf("calls=%d want=%d verification=%+v", calls, tc.calls, v)
			}
			if ok, reason := plan.AutomaticVerificationRepairAdmissible(detail); ok != tc.admissible {
				t.Fatalf("admissible=%v want=%v: %s", ok, tc.admissible, reason)
			}
			if plan.VerificationRepairAttemptCount(detail) != 0 && tc.name != "cap" {
				t.Fatal("comparison consumed repair attempt")
			}
		})
	}
}
