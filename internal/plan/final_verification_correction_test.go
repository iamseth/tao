package plan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/testenv"
)

func correctionFixture(t *testing.T) (*PlanDetail, FinalVerificationCorrectionRequest) {
	t.Helper()
	d := completedReopenDetail()
	d.Dir = t.TempDir()
	d.State.Status = StatusVerificationFailed
	d.State.Workspace = &Workspace{HeadSHA: "head"}
	d.State.Plan.FinalVerification = &FinalVerification{Command: "make verify", CWD: "/legacy", HeadSHA: "head", Result: "failed", Fingerprint: "old"}
	return d, FinalVerificationCorrectionRequest{ExpectedHead: "head", Contract: FinalVerificationContract{Revision: 1, Command: "make test", CWD: ".", Purpose: "test", Coverage: []string{"test"}, Provenance: "owner"}, Reason: "correct gate", CoverageRemoved: []string{"build"}, DeferredChecks: []string{"manual build"}, At: time.Now().UTC()}
}

func confirmCorrection(t *testing.T, d *PlanDetail, req FinalVerificationCorrectionRequest) FinalVerificationCorrectionRequest {
	t.Helper()
	preview, err := PreviewFinalVerificationCorrection(d, req)
	if err != nil {
		t.Fatal(err)
	}
	req.Confirmation = preview.Token
	return req
}

func TestFinalVerificationCorrectionLegacyRepair(t *testing.T) {
	d, req := correctionFixture(t)
	d.Slices.Slices[0].CommitIntent = &SliceCommitIntent{Policy: "slice", Hash: "frozen", StartingHead: "parent", Message: "unchanged"}
	d.Slices.Slices[0].Completion = &SliceCompletionOutcome{Outcome: SliceCompletionCommitted, CommitSHA: "head"}
	completed := cloneSlices(d.Slices.Slices)
	d.Slices.Slices = append(d.Slices.Slices, Slice{ID: "repair", Status: StatusBlocked, VerificationRepair: &VerificationRepairBinding{Command: "make verify", HeadSHA: "head", Fingerprint: "old"}})
	d.State.Plan.PendingSlices = []string{"repair"}
	d.State.Plan.CurrentSlice = new("repair")
	req = confirmCorrection(t, d, req)
	r := testRecord(d.Dir, d)
	if err := r.CorrectFinalVerification(req); err != nil {
		t.Fatal(err)
	}
	if d.State.Plan.FinalVerification != nil || d.State.Plan.Review != nil || RequireCurrentFinalVerification(d) == nil {
		t.Fatal("old authority retained")
	}
	repair := findSlice(d, "repair")
	if repair.Status != StatusBlocked || repair.Completion != nil || repair.VerificationRepair == nil || !auditedRepairSuperseded(d, repair) {
		t.Fatal("repair history lost")
	}
	if !sliceWorkSettled(d) || VerificationRepairAttemptCount(d) != 1 {
		t.Fatal("obligations or cap incorrect")
	}
	if !reflect.DeepEqual(completed, d.Slices.Slices[:len(completed)]) {
		t.Fatal("completed work changed")
	}
	req.At = req.At.Add(time.Hour)
	if err := r.CorrectFinalVerification(req); err != nil {
		t.Fatal(err)
	}
	if len(d.State.Plan.FinalVerificationCorrections) != 1 {
		t.Fatal("duplicate audit")
	}
	audit := d.State.Plan.FinalVerificationCorrections[0]
	if audit.OldVerification.Command != "make verify" || audit.OldVerification.CWD != "/legacy" || audit.OldContract != nil {
		t.Fatal("legacy evidence lost")
	}
	if err := r.SkipSlice("repair", req.At); err == nil {
		t.Fatal("ordinary skip admitted")
	}
	if err := r.RemoveSlice("repair", req.At); err == nil {
		t.Fatal("ordinary remove admitted")
	}
	// A new explicit historical code failure can use only the remaining attempt.
	d.State.Plan.FinalVerification = &FinalVerification{Command: "make test", HeadSHA: "head", Result: "failed", Fingerprint: "new", FailureKind: FinalVerificationFailureKindCode}
	d.State.Plan.FinalVerification.ContractDigest, _ = d.State.Plan.FinalVerificationContract.Digest()
	// Use a new isolated fixture, not a stale in-memory overwrite of persisted authority.
	d.Dir = t.TempDir()
	r = testRecord(d.Dir, d)
	if err := r.AppendVerificationRepair(VerificationRepairRequest{Binding: VerificationRepairBinding{Command: "make test", HeadSHA: "head", Fingerprint: "new"}, CreatedAt: req.At}); err != nil {
		t.Fatal(err)
	}
	if VerificationRepairAttemptCount(d) != 2 {
		t.Fatal("supersession reset cap")
	}
	if ok, _ := RepairVerificationAdmissible(d); ok {
		t.Fatal("cap not enforced")
	}
}

func TestFinalVerificationCorrectionRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*PlanDetail, *FinalVerificationCorrectionRequest)
	}{
		{"head", func(d *PlanDetail, r *FinalVerificationCorrectionRequest) { r.ExpectedHead = "stale" }},
		{"digest", func(d *PlanDetail, r *FinalVerificationCorrectionRequest) { r.ExpectedDigest = "stale" }},
		{"revision", func(d *PlanDetail, r *FinalVerificationCorrectionRequest) { r.Contract.Revision = 2 }},
		{"empty command", func(d *PlanDetail, r *FinalVerificationCorrectionRequest) { r.Contract.Command = " " }},
		{"empty coverage", func(d *PlanDetail, r *FinalVerificationCorrectionRequest) { r.Contract.Coverage = nil }},
		{"reason", func(d *PlanDetail, r *FinalVerificationCorrectionRequest) { r.Reason = "" }},
		{"bounded checks", func(d *PlanDetail, r *FinalVerificationCorrectionRequest) { r.DeferredChecks = make([]string, 257) }},
		{"abandoned", func(d *PlanDetail, r *FinalVerificationCorrectionRequest) { d.State.Status = StatusAbandoned }},
		{"completed", func(d *PlanDetail, r *FinalVerificationCorrectionRequest) { d.State.Status = StatusCompleted }},
		{"published", func(d *PlanDetail, r *FinalVerificationCorrectionRequest) { d.State.Plan.PullRequest = &PullRequest{} }},
		{"pr intent", func(d *PlanDetail, r *FinalVerificationCorrectionRequest) {
			d.State.Plan.PullRequestIntent = &PullRequest{}
		}},
		{"merge intent", func(d *PlanDetail, r *FinalVerificationCorrectionRequest) {
			d.State.Plan.MergeCommitIntent = &SingleMergeCommitIntent{}
		}},
		{"rebase intent", func(d *PlanDetail, r *FinalVerificationCorrectionRequest) {
			d.State.Workspace.RebaseIntent = &WorkspaceRebaseIntent{}
		}},
		{"ordinary pending", func(d *PlanDetail, r *FinalVerificationCorrectionRequest) {
			d.Slices.Slices = append(d.Slices.Slices, Slice{ID: "pending", Status: StatusPending})
		}},
		{"active repair", func(d *PlanDetail, r *FinalVerificationCorrectionRequest) {
			d.Slices.Slices = append(d.Slices.Slices, Slice{ID: "repair", Status: StatusInProgress, VerificationRepair: &VerificationRepairBinding{}})
		}},
		{"slice intent", func(d *PlanDetail, r *FinalVerificationCorrectionRequest) {
			d.Slices.Slices = append(d.Slices.Slices, Slice{ID: "repair", Status: StatusBlocked, VerificationRepair: &VerificationRepairBinding{}, CommitIntent: &SliceCommitIntent{}})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, r := correctionFixture(t)
			tc.mutate(d, &r)
			before := clonePlanDetail(d)
			if _, err := PreviewFinalVerificationCorrection(d, r); err == nil {
				t.Fatal("admitted unsafe correction")
			}
			if !reflect.DeepEqual(before, d) {
				t.Fatal("preview mutated input")
			}
		})
	}
}

func TestFinalVerificationCorrectionJournalReplay(t *testing.T) {
	testenv.SkipIfShort(t)
	for _, operation := range []string{"journal", "state", "slices", "event-1", "events-sync", "plan-dir-sync", "remove"} {
		t.Run(operation, func(t *testing.T) {
			d, req := correctionFixture(t)
			req = confirmCorrection(t, d, req)
			if err := writeState(d.Dir, d.State); err != nil {
				t.Fatal(err)
			}
			if err := writeSlices(d.Dir, d.Slices); err != nil {
				t.Fatal(err)
			}
			ioStore := &failingMutationJournalIO{delegate: fileMutationJournalIO{}, failOperation: operation}
			store := journalArtifactMutationStore{fileArtifactStore: fileArtifactStore{}, journalIO: ioStore}
			record, err := newPlanRecord(store, d.Dir, d)
			if err != nil {
				t.Fatal(err)
			}
			if err := record.CorrectFinalVerification(req); err == nil || !strings.Contains(err.Error(), "injected ") {
				t.Fatalf("fault: %v", err)
			}
			if len(d.State.Plan.FinalVerificationCorrections) != 0 {
				t.Fatal("failed transaction published memory")
			}
			if err := record.CorrectFinalVerification(req); err != nil {
				t.Fatal(err)
			}
			if err := record.CorrectFinalVerification(req); err != nil {
				t.Fatal(err)
			}
			files, err := loadPlanFiles(d.Dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(files.state.Plan.FinalVerificationCorrections) != 1 || files.state.Plan.FinalVerification != nil || files.state.Plan.Review != nil {
				t.Fatal("audit or invalidation not settled")
			}
			count := 0
			for _, e := range d.Events {
				if e.Type == EventTypeFinalVerificationCorrected {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("correction events = %d", count)
			}
		})
	}
}

func TestFinalVerificationCorrectionConcurrent(t *testing.T) {
	d, req := correctionFixture(t)
	if err := writeState(d.Dir, d.State); err != nil {
		t.Fatal(err)
	}
	if err := writeSlices(d.Dir, d.Slices); err != nil {
		t.Fatal(err)
	}
	other := clonePlanDetail(d)
	second := req
	second.Reason = "other correction"
	req = confirmCorrection(t, d, req)
	second = confirmCorrection(t, other, second)
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() { <-start; results <- testRecord(d.Dir, d).CorrectFinalVerification(req) }()
	go func() { <-start; results <- testRecord(other.Dir, other).CorrectFinalVerification(second) }()
	close(start)
	firstErr, secondErr := <-results, <-results
	if (firstErr == nil) == (secondErr == nil) {
		t.Fatalf("expected one winner, got %v / %v", firstErr, secondErr)
	}
	files, err := loadPlanFiles(d.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files.state.Plan.FinalVerificationCorrections) != 1 {
		t.Fatal("concurrent duplicate audit")
	}
}

func TestFinalVerificationCorrectionStaleAndRevision(t *testing.T) {
	d, req := correctionFixture(t)
	req = confirmCorrection(t, d, req)
	stale := clonePlanDetail(d)
	staleReq := req
	staleReq.Reason = "concurrent decision"
	staleReq = confirmCorrection(t, stale, staleReq)
	if err := testRecord(d.Dir, d).CorrectFinalVerification(req); err != nil {
		t.Fatal(err)
	}
	if err := testRecord(d.Dir, stale).CorrectFinalVerification(staleReq); err == nil {
		t.Fatal("stale request admitted")
	}
	next := req
	next.ExpectedDigest, _ = d.State.Plan.FinalVerificationContract.Digest()
	next.Contract.Revision = 2
	next.Contract.Coverage = []string{"unit tests"}
	next.CoverageRemoved = nil
	if _, err := PreviewFinalVerificationCorrection(d, next); err == nil {
		t.Fatal("unacknowledged coverage removal")
	}
	next.CoverageRemoved = []string{"test"}
	next = confirmCorrection(t, d, next)
	if err := testRecord(d.Dir, d).CorrectFinalVerification(next); err != nil {
		t.Fatal(err)
	}
	if len(d.State.Plan.FinalVerificationCorrections) != 2 || d.State.Plan.FinalVerificationContract.Revision != 2 {
		t.Fatal("revision history lost")
	}
}

func TestFinalVerificationCorrectionReplacement(t *testing.T) {
	d, req := correctionFixture(t)
	old := req.Contract
	old.Prerequisites = []FinalVerificationPrerequisite{{Kind: "file", Path: "old"}}
	old.PermittedEffects = []string{"build outputs"}
	old.ComparisonPermitted = true
	d.State.Plan.FinalVerificationContract = &old
	req.ExpectedDigest, _ = old.Digest()
	req.Contract.Revision = 2
	d.State.Plan.Review = &PlanReview{Head: "head", Status: ReviewStatusCompleted, Verdict: ReviewVerdictApprove, VerificationContractDigest: req.ExpectedDigest}
	if err := writeState(d.Dir, d.State); err != nil {
		t.Fatal(err)
	}
	if err := writeSlices(d.Dir, d.Slices); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(d.Dir, "state.json")
	data, err := os.ReadFile(path) //nolint:gosec // Isolated fixture path under t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	root["unrelated_extension"] = "preserved"
	data, err = json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	req = confirmCorrection(t, d, req)
	if err := testRecord(d.Dir, d).CorrectFinalVerification(req); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path) //nolint:gosec // Isolated fixture path under t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	p := root["plan"].(map[string]any)
	contract := p["final_verification_contract"].(map[string]any)
	if contract["prerequisites"] != nil || contract["permitted_effects"] != nil || contract["comparison_permitted"] != false || p["final_verification"] != nil || p["review"] != nil || root["unrelated_extension"] != "preserved" {
		t.Fatalf("stale fields or unknown data loss: %s", data)
	}
}

func TestFinalVerificationCorrectionTokenAndClone(t *testing.T) {
	d, req := correctionFixture(t)
	p, err := PreviewFinalVerificationCorrection(d, req)
	if err != nil {
		t.Fatal(err)
	}
	req.At = req.At.Add(time.Hour)
	req.Confirmation = "irrelevant"
	same, err := PreviewFinalVerificationCorrection(d, req)
	if err != nil || p.Token != same.Token {
		t.Fatal("timestamp/confirmation affected identity")
	}
	d.State.Plan.Review = &PlanReview{Head: "head"}
	different, err := PreviewFinalVerificationCorrection(d, req)
	if err != nil || different.Token == p.Token {
		t.Fatal("review not bound")
	}
	req.Confirmation = p.Token
	if err := testRecord(d.Dir, d).CorrectFinalVerification(req); err == nil {
		t.Fatal("stale approval state accepted")
	}
	req = confirmCorrection(t, d, req)
	if err := testRecord(d.Dir, d).CorrectFinalVerification(req); err != nil {
		t.Fatal(err)
	}
	clone := clonePlanDetail(d)
	clone.State.Plan.FinalVerificationCorrections[0].NewContract.Coverage[0] = "changed"
	clone.State.Plan.FinalVerificationCorrections[0].OldVerification.Command = "changed"
	clone.State.Plan.FinalVerificationCorrections[0].DeferredChecks[0] = "changed"
	if d.State.Plan.FinalVerificationCorrections[0].NewContract.Coverage[0] == "changed" || d.State.Plan.FinalVerificationCorrections[0].OldVerification.Command == "changed" || d.State.Plan.FinalVerificationCorrections[0].DeferredChecks[0] == "changed" {
		t.Fatal("clone alias")
	}
	d.Slices.Slices = append(d.Slices.Slices, Slice{ID: "forged", Status: StatusPending, VerificationRepair: &VerificationRepairBinding{}, VerificationSupersession: &FinalVerificationSupersession{Token: "event prose", Revision: 1}})
	if auditedRepairSuperseded(d, &d.Slices.Slices[len(d.Slices.Slices)-1]) {
		t.Fatal("unaudited supersession accepted")
	}
	if sliceWorkSettled(d) {
		t.Fatal("unaudited obligation treated as settled")
	}
}
