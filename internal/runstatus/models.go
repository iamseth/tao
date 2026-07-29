// Package runstatus persists best-effort, repository-scoped run liveness.
// Records are operational observations only; they are never plan lifecycle
// evidence.
package runstatus

import (
	"errors"
	"time"
)

const (
	Schema = "tao.run-status.v1"

	// PublicationInterval is the cadence expected of active publishers.
	PublicationInterval = 5 * time.Second
	// StaleThreshold is the fixed age at which a heartbeat stops being fresh.
	StaleThreshold = 20 * time.Second
)

var (
	ErrInvalidPlanID      = errors.New("invalid runtime status plan id")
	ErrInvalidRecord      = errors.New("invalid runtime status record")
	errInvocationNotOwner = errors.New("runtime status invocation does not own record")
)

// Phase describes the active operation without changing durable plan state.
type Phase string

// SliceDetail optionally identifies the slice involved in the active phase.
type SliceDetail struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
}

// Record is one process's latest operational status for a plan.
type Record struct {
	Schema              string       `json:"schema"`
	RepoID              string       `json:"repo_id"`
	RepoName            string       `json:"repo_name,omitempty"`
	PlanID              string       `json:"plan_id"`
	PlanTitle           string       `json:"plan_title,omitempty"`
	InvocationID        string       `json:"invocation_id"`
	Phase               Phase        `json:"phase"`
	Slice               *SliceDetail `json:"slice,omitempty"`
	InvocationStartedAt time.Time    `json:"invocation_started_at"`
	HeartbeatAt         time.Time    `json:"heartbeat_at"`
}

// Freshness is derived solely from heartbeat age. Stale does not mean failed.
type Freshness string

const (
	FreshnessFresh Freshness = "fresh"
	FreshnessStale Freshness = "stale"
)

// DeriveFreshness classifies a heartbeat without mutating the record. A record
// becomes stale exactly at StaleThreshold; future timestamps remain fresh.
func DeriveFreshness(record Record, now time.Time) Freshness {
	if now.Before(record.HeartbeatAt.Add(StaleThreshold)) {
		return FreshnessFresh
	}
	return FreshnessStale
}
