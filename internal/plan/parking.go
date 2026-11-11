package plan

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

func (p Parking) Validate() error {
	if err := ValidateAbandonmentReason(p.Reason); err != nil {
		return fmt.Errorf("parking: %w", err)
	}
	if !utf8.ValidString(p.Reason) || strings.ContainsFunc(p.Reason, unicode.IsControl) {
		return fmt.Errorf("parking reason must be valid text without control characters")
	}
	if p.ParkedAt.IsZero() {
		return fmt.Errorf("parking timestamp is required")
	}
	return nil
}

// Park records an owner decision without modifying lifecycle or execution evidence.
// An existing marker is immutable until explicitly unparked.
func (r *PlanRecord) Park(reason string, at time.Time) error {
	marker := Parking{Reason: strings.TrimSpace(reason), ParkedAt: at.UTC()}
	if err := marker.Validate(); err != nil {
		return err
	}
	store, err := r.storeOrDefault()
	if err != nil {
		return err
	}
	return r.applyStateEvent(store, func(detail *PlanDetail, _ *artifactChangeSet) ([]Event, error) {
		if detail.State.Plan.Parking != nil {
			return nil, nil
		}
		// Distinguish reversible operations even at the same supplied time. Audit
		// event deduplication must never suppress a new state transition.
		operationID, err := newArtifactMutationID()
		if err != nil {
			return nil, err
		}
		detail.State.Plan.Parking = &marker
		return []Event{{ParkingOperationID: operationID, Type: EventTypePlanParked, Timestamp: marker.ParkedAt, PlanID: detail.State.Plan.ID, Reason: marker.Reason, Message: "Plan parked"}}, nil
	})
}

// Unpark clears only the explicit marker; ordinary run and merge gates remain intact.
func (r *PlanRecord) Unpark(at time.Time) error {
	if at.IsZero() {
		return fmt.Errorf("unparking timestamp is required")
	}
	store, err := r.storeOrDefault()
	if err != nil {
		return err
	}
	return r.applyStateEvent(store, func(detail *PlanDetail, changes *artifactChangeSet) ([]Event, error) {
		if detail.State.Plan.Parking == nil {
			return nil, nil
		}
		operationID, err := newArtifactMutationID()
		if err != nil {
			return nil, err
		}
		changes.ClearPlanParking()
		return []Event{{ParkingOperationID: operationID, Type: EventTypePlanUnparked, Timestamp: at.UTC(), PlanID: detail.State.Plan.ID, Message: "Plan unparked"}}, nil
	})
}

// RequireNotParked consults state only. Audit events cannot grant or deny admission.
func RequireNotParked(detail *PlanDetail) error {
	if detail == nil {
		return fmt.Errorf("plan detail is nil")
	}
	if detail.State.Plan.Parking != nil {
		return fmt.Errorf("plan is parked; run tao unpark %s before running it", detail.State.Plan.ID)
	}
	return nil
}
