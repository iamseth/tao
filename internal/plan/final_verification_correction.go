package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

// FinalVerificationCorrectionRequest is explicit owner input. The caller must
// hold run ownership and refuse a dirty execution workspace before mutation;
// persisted workspace metadata cannot establish live Git cleanliness.
type FinalVerificationCorrectionRequest struct {
	ExpectedDigest  string
	ExpectedHead    string
	Contract        FinalVerificationContract
	Reason          string
	CoverageRemoved []string
	DeferredChecks  []string
	Confirmation    string
	At              time.Time
}

type FinalVerificationCorrectionPreview struct {
	Token            string
	OldDigest        string
	NewDigest        string
	SupersededSlices []string
}

// FinalVerificationCorrectionAudit retains replaced authority, not a regression
// classification. Bounds apply to the entire retained history before writing.
type FinalVerificationCorrectionAudit struct {
	Token            string                     `json:"token"`
	RequestDigest    string                     `json:"request_digest"`
	Head             string                     `json:"head"`
	OldContract      *FinalVerificationContract `json:"old_contract,omitempty"`
	NewContract      FinalVerificationContract  `json:"new_contract"`
	OldVerification  *FinalVerification         `json:"old_verification,omitempty"`
	OldReview        *PlanReview                `json:"old_review,omitempty"`
	Ruling           string                     `json:"ruling"`
	CoverageRemoved  []string                   `json:"coverage_removed"`
	DeferredChecks   []string                   `json:"deferred_checks"`
	SupersededSlices []string                   `json:"superseded_slices"`
	At               time.Time                  `json:"at"`
}

type FinalVerificationSupersession struct {
	Token    string `json:"token"`
	Revision int    `json:"revision"`
}

func correctionHash(domain string, value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(append([]byte(domain+"\x00"), data...))
	return hex.EncodeToString(digest[:]), nil
}

func correctionRequestDigest(request FinalVerificationCorrectionRequest) (string, error) {
	request.At = time.Time{}
	request.Confirmation = ""
	return correctionHash("tao.final-verification-correction.request.v1", request)
}

// PreviewFinalVerificationCorrection is pure and binds every persisted lifecycle
// field, slice binding and approval/intent state, not diagnostic event prose.
func PreviewFinalVerificationCorrection(detail *PlanDetail, request FinalVerificationCorrectionRequest) (FinalVerificationCorrectionPreview, error) {
	var preview FinalVerificationCorrectionPreview
	if detail == nil || detail.State.Workspace == nil || request.ExpectedHead == "" || len(request.ExpectedHead) > 256 || request.ExpectedHead != detail.State.Workspace.HeadSHA {
		return preview, fmt.Errorf("correction requires unchanged workspace head")
	}
	p := detail.State.Plan
	if detail.State.Status == StatusAbandoned || detail.State.Status == StatusCompleted || anyPlanMergedEvent(detail.Events) || p.PullRequest != nil || p.PullRequestIntent != nil || p.MergeCommitIntent != nil || detail.State.Workspace.RebaseIntent != nil {
		return preview, fmt.Errorf("correction refuses terminal publication or unsettled intent")
	}
	if request.At.IsZero() {
		return preview, fmt.Errorf("correction requires timestamp")
	}
	if err := contractText(request.Reason, "correction reason", true); err != nil {
		return preview, err
	}
	for _, list := range [][]string{request.CoverageRemoved, request.DeferredChecks} {
		if len(list) > 256 {
			return preview, fmt.Errorf("correction list exceeds 256 entries")
		}
		for _, v := range list {
			if err := contractText(v, "correction check", true); err != nil {
				return preview, err
			}
		}
	}
	revision := 1
	if p.FinalVerificationContract != nil {
		var err error
		preview.OldDigest, err = p.FinalVerificationContract.Digest()
		if err != nil {
			return preview, err
		}
		revision = p.FinalVerificationContract.Revision + 1
		for _, coverage := range p.FinalVerificationContract.Coverage {
			if !slices.Contains(request.Contract.Coverage, coverage) && !slices.Contains(request.CoverageRemoved, coverage) {
				return preview, fmt.Errorf("removed coverage must be acknowledged: %s", coverage)
			}
		}
	} else if p.FinalVerification == nil || p.FinalVerification.Result != "failed" || strings.TrimSpace(p.FinalVerification.Command) == "" {
		return preview, fmt.Errorf("legacy correction requires failed command evidence")
	}
	if request.ExpectedDigest != preview.OldDigest {
		return preview, fmt.Errorf("correction contract digest changed")
	}
	if request.Contract.Revision != revision || revision <= 0 {
		return preview, fmt.Errorf("correction requires revision %d", revision)
	}
	var err error
	preview.NewDigest, err = request.Contract.Digest()
	if err != nil {
		return preview, err
	}
	for i := range detail.Slices.Slices {
		s := &detail.Slices.Slices[i]
		if s.CommitIntent != nil && s.Completion == nil {
			return preview, fmt.Errorf("correction refuses slice commit intent: %s", s.ID)
		}
		if s.Status == StatusInProgress {
			return preview, fmt.Errorf("correction refuses in-progress work: %s", s.ID)
		}
		if auditedRepairSuperseded(detail, s) {
			continue
		}
		if s.VerificationSupersession != nil {
			return preview, fmt.Errorf("correction refuses invalid supersession evidence: %s", s.ID)
		}
		if s.Status == StatusCompleted || s.Status == StatusSkipped {
			continue
		}
		if s.VerificationRepair == nil || s.Completion != nil || (s.Status != StatusPending && s.Status != StatusBlocked) {
			return preview, fmt.Errorf("correction refuses unfinished ordinary or unsafe work: %s", s.ID)
		}
		preview.SupersededSlices = append(preview.SupersededSlices, s.ID)
	}
	for _, id := range p.PendingSlices {
		if !slices.Contains(preview.SupersededSlices, id) {
			return preview, fmt.Errorf("correction refuses unsafe pending work: %s", id)
		}
	}
	if p.CurrentSlice != nil && !slices.Contains(preview.SupersededSlices, *p.CurrentSlice) {
		return preview, fmt.Errorf("correction refuses active current slice")
	}
	request.At = time.Time{}
	request.Confirmation = ""
	preview.Token, err = correctionHash("tao.final-verification-correction.preview.v1", struct {
		State   State
		Slices  SlicesFile
		Request FinalVerificationCorrectionRequest
	}{detail.State, detail.Slices, request})
	return preview, err
}

// CorrectFinalVerification recomputes the preview under the existing mutation
// lock. Its single journal transaction preserves history and invalidates all
// old gate/review authority. No shell or Git operation occurs here.
func (r *PlanRecord) CorrectFinalVerification(request FinalVerificationCorrectionRequest) error {
	requestDigest, err := correctionRequestDigest(request)
	if err != nil {
		return err
	}
	return r.apply(func(detail *PlanDetail) (lifecycleMutation, error) {
		for _, audit := range detail.State.Plan.FinalVerificationCorrections {
			if request.Confirmation != "" && audit.Token == request.Confirmation && audit.RequestDigest == requestDigest {
				return lifecycleMutation{State: detail.State, Slices: detail.Slices}, nil
			}
		}
		preview, err := PreviewFinalVerificationCorrection(detail, request)
		if err != nil {
			return lifecycleMutation{}, err
		}
		if request.Confirmation == "" || request.Confirmation != preview.Token {
			return lifecycleMutation{}, fmt.Errorf("correction requires exact current preview confirmation")
		}
		audit := FinalVerificationCorrectionAudit{Token: preview.Token, RequestDigest: requestDigest, Head: request.ExpectedHead, OldContract: cloneFinalVerificationContract(detail.State.Plan.FinalVerificationContract), NewContract: *cloneFinalVerificationContract(&request.Contract), OldVerification: cloneFinalVerification(detail.State.Plan.FinalVerification), OldReview: clonePlanReview(detail.State.Plan.Review), Ruling: request.Reason, CoverageRemoved: slices.Clone(request.CoverageRemoved), DeferredChecks: slices.Clone(request.DeferredChecks), SupersededSlices: slices.Clone(preview.SupersededSlices), At: request.At}
		history := append(cloneCorrectionAudits(detail.State.Plan.FinalVerificationCorrections), audit)
		encoded, err := json.Marshal(history)
		if err != nil {
			return lifecycleMutation{}, err
		}
		if len(history) > 64 || len(encoded) > 1024*1024 {
			return lifecycleMutation{}, fmt.Errorf("correction audit history exceeds bound")
		}
		return applyLifecycleMutation(detail, func(changes *artifactChangeSet) ([]Event, error) {
			changes.ReplaceFinalVerificationCorrection(request.Contract, history)
			changes.ClearPlanReview()
			changes.ClearPlanFinalizationFailure()
			if detail.State.Plan.CurrentSlice != nil {
				changes.ClearPlanCurrentSlice()
			}
			detail.State.Plan.PendingSlices = slices.DeleteFunc(detail.State.Plan.PendingSlices, func(id string) bool { return slices.Contains(preview.SupersededSlices, id) })
			for _, id := range preview.SupersededSlices {
				findSlice(detail, id).VerificationSupersession = &FinalVerificationSupersession{Token: preview.Token, Revision: request.Contract.Revision}
			}
			detail.State.Status = StatusInReview
			detail.State.UpdatedAt = request.At
			detail.State.Plan.Timing.LastActivityAt = new(request.At)
			return []Event{{Type: EventTypeFinalVerificationCorrected, Timestamp: request.At, PlanID: detail.State.Plan.ID, HeadSHA: request.ExpectedHead, Fingerprint: preview.Token, Reason: "old_digest=" + preview.OldDigest + " new_digest=" + preview.NewDigest, Message: "Final verification contract corrected; fresh verification and review required"}}, nil
		})
	})
}

func cloneCorrectionAudits(audits []FinalVerificationCorrectionAudit) []FinalVerificationCorrectionAudit {
	if audits == nil {
		return nil
	}
	out := slices.Clone(audits)
	for i := range out {
		a := &out[i]
		a.OldContract = cloneFinalVerificationContract(a.OldContract)
		a.NewContract = *cloneFinalVerificationContract(&a.NewContract)
		a.OldVerification = cloneFinalVerification(a.OldVerification)
		a.OldReview = clonePlanReview(a.OldReview)
		a.CoverageRemoved = slices.Clone(a.CoverageRemoved)
		a.DeferredChecks = slices.Clone(a.DeferredChecks)
		a.SupersededSlices = slices.Clone(a.SupersededSlices)
	}
	return out
}

func auditedRepairSuperseded(detail *PlanDetail, slice *Slice) bool {
	if detail == nil || slice == nil || slice.VerificationRepair == nil || slice.VerificationSupersession == nil || slice.Completion != nil || slice.CommitIntent != nil || (slice.Status != StatusPending && slice.Status != StatusBlocked) {
		return false
	}
	for _, audit := range detail.State.Plan.FinalVerificationCorrections {
		if len(audit.Token) == 64 && len(audit.RequestDigest) == 64 && audit.NewContract.Validate() == nil && audit.Token == slice.VerificationSupersession.Token && audit.NewContract.Revision == slice.VerificationSupersession.Revision && slices.Contains(audit.SupersededSlices, slice.ID) {
			return true
		}
	}
	return false
}
