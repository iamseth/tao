// Package runqueue provides a bounded-parallel queue drain with per-plan
// exclusivity. A Manager defaults to at most one active run; callers can
// override that limit with Manager.SetMaxParallelRuns.
//
// Manager is the scheduler: it owns queue persistence and projections, FIFO
// candidate selection, conflict admission, bounded dispatch, and stop/pause
// state. It never interprets plan recovery or drives rework rounds, and it does
// not hold its state mutex while validator or conflict callbacks run. Callers
// observe progress through snapshots rather than reaching into queue internals.
//
// EntryDriver is the synchronous one-entry boundary. It owns plan-level
// recovery, validation, ownership, and execution while delegating ordinary and
// recovered automatic-rework decisions to rework.Driver.Run. Queue snapshots
// remain the durable source of each entry's baseline, attempts, fingerprint,
// and recovery-pending state; progress is persisted before a reopened round is
// executed, under the same retained plan owner. The domain driver owns bounded
// cap and finding-stop decisions.
//
// EntryDriver submits value-only transitions through EntryDriverHost; the host
// serializes each transition, persists it first, and only then publishes
// matching queue and active-status projections. External callbacks remain
// outside that ordering-critical section.
package runqueue
