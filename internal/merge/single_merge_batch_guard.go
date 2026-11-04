package merge

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// LandingIntentProbe checks whether durable landing intent reached the default branch.
type LandingIntentProbe interface {
	DefaultReachedLandingIntent(context.Context, BatchState) (bool, error)
}

// SingleMergeBatchRefusal identifies pre-landing recovery that a single merge
// would strand. It never authorizes changes to that recovery state.
type SingleMergeBatchRefusal struct {
	BatchID    string
	Status     BatchStatus
	PlanID     string
	Candidate  bool
	Ejected    bool
	RecordOnly bool
}

func (e *SingleMergeBatchRefusal) Error() string {
	membership := "not a candidate"
	if e.Ejected {
		membership = "an ejected candidate"
	} else if e.Candidate {
		membership = "a candidate"
	}
	return fmt.Sprintf("single merge refused: active merge batch %s (status %s) has not landed; plan %s is %s. Resume with `tao merge --all`, or preview `tao merge --all --restart --dry-run` then run `tao merge --all --restart` to discard pre-landing recovery, then retry", e.BatchID, e.Status, e.PlanID, membership)
}

// singleMergeActiveBatchID deliberately rejects invalid identities rather than
// using ActiveID's tolerant semantics for other batch callers.
func singleMergeActiveBatchID(store *BatchStore) (string, error) {
	activeBatchMu.Lock()
	defer activeBatchMu.Unlock()
	store.mu.Lock()
	defer store.mu.Unlock()
	content, err := os.ReadFile(store.activePath) // #nosec G304 -- path is rooted in Tao's repository data directory.
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	identity, valid := decodeActiveBatchIdentity(content)
	if !valid {
		return "", fmt.Errorf("invalid active merge batch identity")
	}
	return identity.BatchID, nil
}

// RequireNoPreLandingBatch reads batch recovery without mutating it. Record-only
// merges are exempt only when the plan is not a candidate of the active batch.
func RequireNoPreLandingBatch(ctx context.Context, store *BatchStore, probe LandingIntentProbe, planID string, recordOnly bool) error {
	if store == nil || probe == nil {
		return fmt.Errorf("single merge batch guard requires a batch store and landing intent probe")
	}
	id, err := singleMergeActiveBatchID(store)
	if err != nil {
		return fmt.Errorf("read active merge batch for single merge: %w", err)
	}
	if id == "" {
		return nil
	}
	state, err := store.Load(id)
	if err != nil {
		return fmt.Errorf("load active merge batch %s for single merge: %w", id, err)
	}
	if state.ID == "" {
		return fmt.Errorf("active merge batch %s has no readable state", id)
	}
	if state.Landing != nil {
		if state.LandedSHA != "" {
			return nil
		}
		reached, err := probe.DefaultReachedLandingIntent(ctx, state)
		if err != nil {
			return fmt.Errorf("probe active merge batch %s landing for single merge: %w", id, err)
		}
		if reached {
			return nil
		}
	}
	candidate := false
	for _, c := range state.Candidates {
		if c.PlanID == planID {
			candidate = true
			break
		}
	}
	ejected := state.Ejection != nil && state.Ejection.PlanID == planID
	if recordOnly && !candidate {
		return nil
	}
	return &SingleMergeBatchRefusal{BatchID: state.ID, Status: state.Status, PlanID: planID, Candidate: candidate, Ejected: ejected, RecordOnly: recordOnly}
}
