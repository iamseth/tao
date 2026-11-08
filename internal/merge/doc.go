// Package merge owns plan integration and the complete durable batch merge
// state machine. Callers construct its services and coordinator, invoke them,
// and render results without sequencing batch phases.
//
// File navigation:
//   - agent_session.go: agent/session orchestration and telemetry.
//   - merge_confinement.go: single-merge filesystem confinement and readiness.
//   - single_resolution.go: single-resolution validation and settlement.
//   - session_boundary.go: shared session-boundary snapshots, checks, and restoration.
package merge
