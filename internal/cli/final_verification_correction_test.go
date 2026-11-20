package cli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/plantest"
	"github.com/iamseth/tao/internal/testenv"
)

const correctionProposalJSON = `{"expected_digest":"","expected_head":"head","contract":{"revision":1,"command":"never execute this gate","cwd":".","purpose":"tests","coverage":["tests"],"provenance":"owner"},"reason":"correct gate","coverage_removed":["build"],"deferred_checks":["manual build"]}`

func TestFinalCorrectionProposal(t *testing.T) {
	for _, tc := range []struct {
		name, data string
		valid      bool
	}{
		{"valid", correctionProposalJSON, true},
		{"unknown", strings.Replace(correctionProposalJSON, `"reason":`, `"extra":1,"reason":`, 1), false},
		{"timestamp", strings.Replace(correctionProposalJSON, `"reason":`, `"At":"2026-01-01T00:00:00Z","reason":`, 1), false},
		{"confirmation", strings.Replace(correctionProposalJSON, `"reason":`, `"Confirmation":"token","reason":`, 1), false},
		{"duplicate", strings.Replace(correctionProposalJSON, `"reason":`, `"reason":"first","reason":`, 1), false},
		{"nested duplicate", strings.Replace(correctionProposalJSON, `"revision":1`, `"revision":1,"Revision":2`, 1), false},
		{"missing reason", strings.Replace(correctionProposalJSON, `"reason":"correct gate",`, "", 1), false},
		{"missing coverage", strings.Replace(correctionProposalJSON, `"coverage_removed":["build"],`, "", 1), false},
		{"missing deferred", strings.Replace(correctionProposalJSON, `,"deferred_checks":["manual build"]`, "", 1), false},
		{"trailing", correctionProposalJSON + " {}", false},
		{"oversized", strings.Repeat(" ", 65537), false},
		{"null", "null", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "proposal.json")
			if err := os.WriteFile(p, []byte(tc.data), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := readFinalCorrectionProposal(p)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
	t.Run("symlink escape", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "proposal.json")
		if err := os.WriteFile(target, []byte(correctionProposalJSON), 0600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(t.TempDir(), "proposal.json")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if _, err := readFinalCorrectionProposal(link); err == nil {
			t.Fatal("escaped root")
		}
	})
}

func TestFinalVerificationCorrectionProcess(t *testing.T) {
	testenv.SkipIfShort(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	binary := filepath.Join(t.TempDir(), "tao")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "../../cmd/tao") // #nosec G204 -- fixed build target and isolated output.
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	root := newCLICommitRepo(t)
	runCLICommitGit(t, root, "checkout", "-b", "work")
	head := strings.TrimSpace(runCLICommitGit(t, root, "rev-parse", "HEAD"))
	dataHome := t.TempDir()
	invoke := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, binary, args...) // #nosec G204 -- isolated built Tao with test-owned arguments.
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "TAO_DATA_HOME="+dataHome, "TAO_SLICE_COMPLETION_OWNER=")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := invoke("init"); err != nil {
		t.Fatalf("register: %v %s", err, out)
	}
	d := plantest.NewPlanDetail("correction").WithStatus(plan.StatusVerificationFailed).WithRepoRoot(root).WithCompletedSlices("done").AddSlice(plantest.NewSlice("done").WithStatus(plan.StatusCompleted).WithCompletedAt(time.Now().UTC()).Build()).Build()
	d.Dir = filepath.Join(t.TempDir(), "correction")
	if err := os.Mkdir(d.Dir, 0700); err != nil {
		t.Fatal(err)
	}
	d.State.Plan.CurrentSlice = nil
	d.State.Plan.PendingSlices = nil
	d.State.Workspace = &plan.Workspace{Path: root, Branch: "work", HeadSHA: head}
	marker := filepath.Join(root, "gate-was-run")
	d.State.Plan.FinalVerification = &plan.FinalVerification{Command: "touch " + marker, CWD: root, HeadSHA: head, Result: "failed"}
	for name, value := range map[string]any{"state.json": d.State, "slices.json": d.Slices} {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d.Dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	proposal := filepath.Join(t.TempDir(), "proposal.json")
	contents := strings.Replace(correctionProposalJSON, `"head"`, `"`+head+`"`, 1)
	contents = strings.Replace(contents, "never execute this gate", "touch "+marker, 1)
	if err := os.WriteFile(proposal, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"edit", "final-verification", d.Dir, "--proposal-file", proposal}
	out, err := invoke(args...)
	if err != nil {
		t.Fatalf("preview: %v %s", err, out)
	}
	for _, want := range []string{"REMOVED COVERAGE:", "DEFERRED CHECKS:", "not build-equivalent", "Remaining lifetime repair attempts: 2"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q: %s", want, out)
		}
	}
	_, rest, ok := strings.Cut(out, "Preview token: ")
	if !ok {
		t.Fatal(out)
	}
	token := strings.Fields(rest)[0]
	// This process holds the lock while the independently built CLI contends.
	lock, err := plan.AcquireRunLock(d.Dir, d.State.Plan.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	confirm := append(append([]string{}, args...), "--confirm", token)
	if out, err := invoke(confirm...); err == nil {
		t.Fatalf("active run admitted: %s", out)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	if out, err := invoke(confirm...); err != nil {
		t.Fatalf("confirm: %v %s", err, out)
	}
	if out, err := invoke(confirm...); err != nil {
		t.Fatalf("repeat: %v %s", err, out)
	}
	fresh, err := plan.NewFileRepository(filepath.Dir(d.Dir)).ResolvePlan(ctx, d.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh.State.Plan.FinalVerificationCorrections) != 1 || fresh.State.Plan.FinalVerification != nil || plan.RequireCurrentFinalVerification(fresh) == nil {
		t.Fatal("correction did not invalidate authority exactly once")
	}
	if fresh.Slices.Slices[0].Status != plan.StatusCompleted {
		t.Fatal("completed work changed")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("gate invoked: %v", err)
	}
	if got := strings.TrimSpace(runCLICommitGit(t, root, "rev-parse", "HEAD")); got != head {
		t.Fatal("HEAD changed")
	}
}
