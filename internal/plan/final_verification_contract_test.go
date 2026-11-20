package plan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func contractFixture() FinalVerificationContract {
	return FinalVerificationContract{Revision: 1, Command: "make verify", CWD: ".", Purpose: "repository gate", Coverage: []string{"tests"}, Prerequisites: []FinalVerificationPrerequisite{{Kind: "file", Path: "Makefile"}}, PermittedEffects: []string{"build outputs"}, Provenance: "owner"}
}

func TestRequireCurrentFinalVerification(t *testing.T) {
	d := completedReopenDetail()
	if err := RequireCurrentFinalVerification(d); err != nil {
		t.Fatal(err)
	}
	c := contractFixture()
	d.State.Plan.FinalVerificationContract = &c
	d.State.Workspace = &Workspace{HeadSHA: "head"}
	if err := RequireCurrentFinalVerification(d); err == nil {
		t.Fatal("missing verification admitted")
	}
	digest, _ := c.Digest()
	d.State.Plan.FinalVerification = &FinalVerification{Result: "passed", HeadSHA: "head", ContractDigest: digest}
	d.State.Plan.Review = &PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictApprove, VerificationContractDigest: digest}
	if err := RequireCurrentFinalVerification(d); err != nil {
		t.Fatal(err)
	}
	if CurrentReview(d) == nil {
		t.Fatal("current review hidden")
	}
	c.Revision++
	if err := RequireCurrentFinalVerification(d); err == nil {
		t.Fatal("same-head stale verification admitted")
	}
	if CurrentReview(d) != nil {
		t.Fatal("same-head stale review admitted")
	}
}

func TestFinalContractRecoveryAndRecordBoundaries(t *testing.T) {
	d := completedReopenDetail()
	d.Dir = t.TempDir()
	c := contractFixture()
	d.State.Plan.FinalVerificationContract = &c
	d.State.Workspace = &Workspace{HeadSHA: "head"}
	if got := DeriveVerificationRecovery(d); got.Kind != PlanActionReverify {
		t.Fatalf("recovery = %+v", got)
	}
	if got := Summarize(d, time.Now()); got.VerificationRecoveryAction != PlanActionReverify {
		t.Fatalf("summary = %+v", got)
	}
	writeStartSliceArtifacts(t, d.Dir, d)
	record := testRecord(d.Dir, d)
	if err := record.RecordReviewCompleted(PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictComment, Head: "head"}, "pi"); err == nil {
		t.Fatal("review admitted without verification")
	}
	if err := record.RecordPullRequestIntent(PullRequest{}, "feature", "head"); err == nil {
		t.Fatal("PR intent admitted")
	}
	if err := record.RecordSingleMergeCommitIntent(SingleMergeCommitIntent{SourceHead: "head"}); err == nil {
		t.Fatal("merge intent admitted")
	}
	digest, _ := c.Digest()
	d.State.Plan.FinalVerification = &FinalVerification{Result: "passed", HeadSHA: "head", ContractDigest: digest}
	writeStartSliceArtifacts(t, d.Dir, d)
	if err := record.RecordReviewCompleted(PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictComment, Head: "head", VerificationContractDigest: "caller-value", ReviewedAt: time.Now()}, "pi"); err != nil {
		t.Fatal(err)
	}
	if d.State.Plan.Review.VerificationContractDigest != digest {
		t.Fatal("review not bound to durable contract")
	}
	// A stale snapshot must not record a review over a new durable revision.
	stale := clonePlanDetail(d)
	d.State.Plan.FinalVerificationContract.Revision++
	digest, _ = d.State.Plan.FinalVerificationContract.Digest()
	d.State.Plan.FinalVerification.ContractDigest = digest
	writeStartSliceArtifacts(t, d.Dir, d)
	if err := testRecord(d.Dir, stale).RecordReviewCompleted(PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictComment, Head: "head", ReviewedAt: time.Now()}, "pi"); err == nil {
		t.Fatal("stale review race admitted")
	}
	if err := requireCurrentFinalApproval(d, "head"); err == nil {
		t.Fatal("stale approval admitted after new verification")
	}
}

func TestAutomaticVerificationRepairRejectsLegacyEvidence(t *testing.T) {
	if ok, _ := AutomaticVerificationRepairAdmissible(nil); ok {
		t.Fatal("nil evidence admitted")
	}
	detail := &PlanDetail{}
	detail.State.Plan.FinalVerification = &FinalVerification{Result: "failed", FailureKind: FinalVerificationFailureKindCode, HeadSHA: "head", Fingerprint: "legacy"}
	if ok, _ := AutomaticVerificationRepairAdmissible(detail); ok {
		t.Fatal("legacy code label admitted automatic repair")
	}
}

func TestAutomaticVerificationRepairComparison(t *testing.T) {
	fixture := func() *PlanDetail {
		d := completedReopenDetail()
		c := contractFixture()
		c.ComparisonPermitted = true
		d.State.Plan.FinalVerificationContract = &c
		digest, err := c.Digest()
		if err != nil {
			t.Fatal(err)
		}
		d.State.Workspace = &Workspace{HeadSHA: "head"}
		d.State.Repo.BaseCommit = "base"
		one, zero := 1, 0
		obs := []FinalVerificationObservation{{Kind: "file", Identity: "Makefile", Digest: strings.Repeat("a", 64)}}
		d.State.Plan.FinalVerification = &FinalVerification{Command: c.Command, HeadSHA: "head", Result: "failed", FailureKind: FinalVerificationFailureKindCode, Fingerprint: "current", ContractDigest: digest, Comparison: &FinalVerificationComparison{ContractDigest: digest, HeadSHA: "head", BaseSHA: "base", Runs: []FinalVerificationComparisonRun{
			{HeadSHA: "head", CWD: ".", Result: "failed", ExitCode: &one, OutputDigest: strings.Repeat("b", 64), Observations: append([]FinalVerificationObservation(nil), obs...)},
			{HeadSHA: "head", CWD: ".", Result: "failed", ExitCode: &one, OutputDigest: strings.Repeat("b", 64), Observations: append([]FinalVerificationObservation(nil), obs...)},
			{HeadSHA: "base", CWD: ".", Result: "passed", ExitCode: &zero, OutputDigest: strings.Repeat("c", 64), Observations: append([]FinalVerificationObservation(nil), obs...)},
		}}}
		return d
	}
	if ok, reason := AutomaticVerificationRepairAdmissible(fixture()); !ok {
		t.Fatal(reason)
	}
	for name, mutate := range map[string]func(*PlanDetail){
		"contract":     func(d *PlanDetail) { d.State.Plan.FinalVerificationContract.Revision++ },
		"head":         func(d *PlanDetail) { d.State.Workspace.HeadSHA = "new" },
		"base":         func(d *PlanDetail) { d.State.Repo.BaseCommit = "new" },
		"legacy":       func(d *PlanDetail) { d.State.Plan.FinalVerification.Comparison = nil },
		"missing exit": func(d *PlanDetail) { d.State.Plan.FinalVerification.Comparison.Runs[1].ExitCode = nil },
		"failure mismatch": func(d *PlanDetail) {
			d.State.Plan.FinalVerification.Comparison.Runs[1].OutputDigest = strings.Repeat("d", 64)
		},
		"prerequisite mismatch": func(d *PlanDetail) {
			d.State.Plan.FinalVerification.Comparison.Runs[2].Observations[0].Digest = strings.Repeat("d", 64)
		},
		"cwd":           func(d *PlanDetail) { d.State.Plan.FinalVerification.Comparison.Runs[2].CWD = "other" },
		"baseline":      func(d *PlanDetail) { d.State.Plan.FinalVerification.Comparison.Runs[2].Result = "failed" },
		"no permission": func(d *PlanDetail) { d.State.Plan.FinalVerificationContract.ComparisonPermitted = false },
	} {
		t.Run(name, func(t *testing.T) {
			d := fixture()
			mutate(d)
			if ok, _ := AutomaticVerificationRepairAdmissible(d); ok {
				t.Fatal("admitted stale or malformed evidence")
			}
		})
	}
}

func TestFinalVerificationContractIdentity(t *testing.T) {
	c := contractFixture()
	digest, err := c.Digest()
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var decoded FinalVerificationContract
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	got, err := decoded.Digest()
	if err != nil || got != digest {
		t.Fatalf("round trip: %s %v", got, err)
	}
	for _, change := range []func(*FinalVerificationContract){
		func(c *FinalVerificationContract) { c.Revision++ }, func(c *FinalVerificationContract) { c.Command += " " }, func(c *FinalVerificationContract) { c.CWD = "sub" }, func(c *FinalVerificationContract) { c.Prerequisites[0].Path = "other" }, func(c *FinalVerificationContract) { c.PermittedEffects[0] = "other" }, func(c *FinalVerificationContract) { c.ComparisonPermitted = true },
	} {
		c := contractFixture()
		change(&c)
		got, err := c.Digest()
		if err != nil || got == digest {
			t.Fatalf("identity unchanged: %s %v", got, err)
		}
	}
}

func TestFinalVerificationContractCopiesAndOmission(t *testing.T) {
	c := contractFixture()
	exit := 1
	original := PlanState{FinalVerificationContract: &c, FinalVerification: &FinalVerification{ContractDigest: "digest", Comparison: &FinalVerificationComparison{Runs: []FinalVerificationComparisonRun{{ExitCode: &exit, Observations: []FinalVerificationObservation{{Kind: "file", Identity: "Makefile", Digest: "hash"}}}}}}, Review: &PlanReview{VerificationContractDigest: "digest"}}
	clone := clonePlanState(original)
	clone.FinalVerificationContract.Coverage[0] = "changed"
	clone.FinalVerificationContract.Prerequisites[0].Path = "changed"
	clone.FinalVerificationContract.PermittedEffects[0] = "changed"
	clone.FinalVerification.Comparison.Runs[0].Observations[0].Digest = "changed"
	*clone.FinalVerification.Comparison.Runs[0].ExitCode = 2
	if c.Coverage[0] != "tests" || c.Prerequisites[0].Path != "Makefile" || c.PermittedEffects[0] != "build outputs" || exit != 1 || original.FinalVerification.Comparison.Runs[0].Observations[0].Digest != "hash" {
		t.Fatal("clone shares mutable evidence")
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var decoded PlanState
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.FinalVerificationContract == nil || decoded.FinalVerification.Comparison.Runs[0].ExitCode == nil || decoded.Review.VerificationContractDigest != "digest" {
		t.Fatal("lost optional fields")
	}
	legacy := clonePlanState(PlanState{FinalVerification: &FinalVerification{}, Review: &PlanReview{}})
	data, err = json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "contract") || strings.Contains(string(data), "comparison") {
		t.Fatalf("legacy fields populated: %s", data)
	}
}

func TestFinalVerificationContractPersistence(t *testing.T) {
	dir := t.TempDir()
	detail := startSliceDetail(dir)
	c := contractFixture()
	detail.State.Plan.FinalVerificationContract = &c
	changes := newArtifactChangeSet(detail)
	exit := 0
	evidence := FinalVerification{ContractDigest: "current", Comparison: &FinalVerificationComparison{ContractDigest: "current", BaseSHA: "base", HeadSHA: "head", Runs: []FinalVerificationComparisonRun{{HeadSHA: "base", CWD: ".", Result: "passed", ExitCode: &exit, Observations: []FinalVerificationObservation{{Kind: "file", Identity: "Makefile", Digest: "hash"}}}}}}
	if err := changes.replaceFinalVerification(evidence); err != nil {
		t.Fatal(err)
	}
	if err := changes.ReplacePlanReview(PlanReview{VerificationContractDigest: "current"}); err != nil {
		t.Fatal(err)
	}
	data, err := prepareJSON(filepath.Join(dir, "state.json"), detail.State, artifactJSONChanges{kind: artifactJSONState, changes: changes})
	if err != nil {
		t.Fatal(err)
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	if state.Plan.FinalVerificationContract == nil || state.Plan.FinalVerification.ContractDigest != "current" || state.Plan.FinalVerification.Comparison.Runs[0].Observations[0].Digest != "hash" || state.Plan.Review.VerificationContractDigest != "current" {
		t.Fatalf("lost evidence: %s", data)
	}
}

func TestFinalVerificationContractReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	old := `{"plan":{"final_verification":{"contract_digest":"old","comparison":{"contract_digest":"old","runs":[]},"unknown":"keep"},"review":{"verification_contract_digest":"old","unknown":"keep"},"unknown":"keep"}}`
	if err := os.WriteFile(path, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	detail := startSliceDetail(dir)
	changes := newArtifactChangeSet(detail)
	if err := changes.replaceFinalVerification(FinalVerification{Result: "passed"}); err != nil {
		t.Fatal(err)
	}
	if err := changes.ReplacePlanReview(PlanReview{Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	data, err := prepareJSON(path, detail.State, artifactJSONChanges{kind: artifactJSONState, changes: changes})
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	p := root["plan"].(map[string]any)
	for _, field := range []string{"final_verification", "review"} {
		object := p[field].(map[string]any)
		for _, key := range []string{"contract_digest", "comparison", "verification_contract_digest"} {
			if _, ok := object[key]; ok {
				t.Fatalf("stale %s: %s", key, data)
			}
		}
		if object["unknown"] != "keep" {
			t.Fatalf("lost unknown: %s", data)
		}
	}
}

func TestFinalVerificationContractValidation(t *testing.T) {
	for _, change := range []func(*FinalVerificationContract){
		func(c *FinalVerificationContract) { c.Revision = 0 }, func(c *FinalVerificationContract) { c.Command = " " }, func(c *FinalVerificationContract) { c.CWD = "../outside" }, func(c *FinalVerificationContract) { c.CWD = "a/../b" }, func(c *FinalVerificationContract) { c.CWD = "/absolute" }, func(c *FinalVerificationContract) { c.Coverage = nil }, func(c *FinalVerificationContract) { c.Purpose = "" }, func(c *FinalVerificationContract) { c.Provenance = "" }, func(c *FinalVerificationContract) { c.Command = strings.Repeat("x", 16385) }, func(c *FinalVerificationContract) { c.Coverage = make([]string, 257) },
	} {
		c := contractFixture()
		change(&c)
		if err := c.Validate(); err == nil {
			t.Fatalf("accepted invalid contract: %+v", c)
		}
	}
	c := contractFixture()
	c.Coverage = []string{strings.Repeat("x", 16000), strings.Repeat("y", 16000), strings.Repeat("z", 16000), strings.Repeat("w", 16000), strings.Repeat("v", 16000)}
	if err := c.Validate(); err == nil {
		t.Fatal("accepted oversized aggregate")
	}
	detail := startSliceDetail(t.TempDir())
	invalid := contractFixture()
	invalid.Revision = 0
	detail.State.Plan.FinalVerificationContract = &invalid
	if !strings.Contains(strings.Join(validateDetail(detail), "\n"), "final_verification_contract is invalid") {
		t.Fatal("missing validation diagnostic")
	}
	c = contractFixture()
	c.Prerequisites[0].Kind = "future-passive-kind"
	if err := c.Validate(); err != nil {
		t.Fatalf("unsupported kinds remain representable: %v", err)
	}
}
