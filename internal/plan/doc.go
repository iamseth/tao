// Package plan owns Tao plan artifacts, lifecycle rules, and derived state.
//
// The package is the boundary between filesystem-backed plan directories and
// callers such as the CLI. It preserves the on-disk contract
// documented in docs/plan-format.md, keeps legacy artifacts readable where the
// lifecycle allows warnings, and exposes summaries/capabilities so callers do
// not reimplement queue, blocking, or runnable-slice decisions.
//
// What is a plan? Four roles split across package seams:
//   - Raw artifacts (artifact_models.go): the durable on-disk schemas a plan
//     directory holds, such as SlicesFile, written exactly per docs/plan-format.md.
//   - Mutable lifecycle State (lifecycle_models.go): the State the queue advances
//     through, plus the status constants and lifecycle metadata that gate
//     transitions.
//   - Persistence coordination (artifact_io.go and mutation_journal.go): refreshes
//     settled artifacts, prepares exact target payloads, and installs or rolls
//     forward recoverable mutations.
//   - Derived read-side (derive.go): read-only DerivedPlan values (counts,
//     progress, elapsed) computed from the durable artifacts for callers that only
//     read.
//
// Persistence invariant: Tao-owned PlanRecord mutations prepare their complete
// target payloads before installing one durable journal, roll that intent forward,
// and publish the in-memory result only after settlement. Reads settle valid
// pending intent under the same persistence boundary. Plans with neither journal
// nor lock retain a non-mutating legacy read path; historical torn artifacts are
// not synthesized into transactions. Lifecycle rules remain in lifecycle.go;
// docs/plan-mutation-journal.md defines the full persistence protocol.
//
// Navigation map:
//   - models.go points to model groupings; artifact_models.go,
//     lifecycle_models.go, and summary_models.go define artifact schemas,
//     lifecycle metadata, and list-view summaries.
//   - repository.go, record.go, artifact_operations.go, artifact_io.go,
//     resolve.go, and log.go load, resolve, bind mutable records, mutate,
//     delete, and stream plan-directory artifacts. PlanRecord owns complete
//     durable mutation operations and their persistence ordering, including
//     typed workspace milestones and compare-and-set boundary stamps; workspace
//     callers never select generic artifact persistence. lifecycle.go supplies
//     the in-memory transition rules used by those operations.
//   - lifecycle.go and derive.go contain queue transitions, drained-queue
//     predicates, status analysis, progress snapshots, and summary derivation.
//   - Review access is intentionally split: PersistedReview exposes the
//     on-disk State.Plan.Review for comparison/display, CurrentReview applies
//     reopen supersession for "reviewed now" gates, and SetPersistedReview is
//     the sanctioned in-memory writer before persistence.
//   - validate.go and verification*.go check artifact consistency and expose
//     verification-command facades; internal/plan/verification owns command
//     parsing, path analysis, and failure classification.
//   - telemetry.go and run_packet.go summarize agent events and render compact
//     execution context for slice runs.
//   - format.go, duration.go, markdown.go, and clone.go provide local helpers
//     used by the ownership areas above.
//
// Keep schema-level detail in docs/plan-format.md. Comments here should help
// contributors find responsibility boundaries without restating field-by-field
// behavior.
package plan
