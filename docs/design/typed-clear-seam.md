# Typed clear-vs-preserve persistence seam

## Decision

Tao will use an **explicit typed change set** at the artifact-persistence seam. A writer that wants to preserve an existing value does nothing; a writer that wants to clear or fully replace one of the migrated fields must use a field-specific change-set method. JSON tags no longer decide writer intent.

The change set is bound to one `*PlanDetail` and its methods perform the in-memory mutation and record the corresponding persistence intent together:

```go
type ArtifactChangeSet struct {
    detail *PlanDetail
    // Private, typed state/review/slice intents; no caller-supplied JSON paths.
}

func NewArtifactChangeSet(detail *PlanDetail) *ArtifactChangeSet

func (*ArtifactChangeSet) ClearWorkspaceDependencyFailure()
func (*ArtifactChangeSet) ClearWorkspaceDependencyFingerprint()
func (*ArtifactChangeSet) ClearSliceBlockerNote(sliceID string) error
func (*ArtifactChangeSet) ClearPlanCurrentSlice()
func (*ArtifactChangeSet) ReplacePlanReview(review PlanReview) error
func (*ArtifactChangeSet) ClearPlanReview()
```

`ReplacePlanReview` means replacement of every known field in the review block, not a merge of only its non-zero fields. It sets `State.Plan.Review` and records explicit values for all known review keys. Thus an approval replaced by a non-approval cannot retain findings, a commit proposal, base/head/agent data, or any other known value omitted by the replacement. `ClearPlanReview` emits a null for the whole block. Both operations continue to preserve unknown keys when the block remains an object; clearing the whole block intentionally replaces that block with null.

Ordinary non-zero assignments may remain direct assignments. The clear methods are deliberately verbs: an empty Go value by itself means preserve, while calling `Clear...` means erase the stored value. Internal lifecycle and state-event callbacks receive a change set. Callers that edit state before persistence use `PlanRecord.PersistStateChanges(*ArtifactChangeSet)`; the existing `PersistState()` remains the preserve-only path.

### Rejected alternatives

- **Derive clears from a before/after diff:** concise, but a zero value still ambiguously means either “clear” or “not supplied.” It would recreate an implicit contract, make stale-record rebases infer intent, and allow newly added zero-valued fields to become destructive without a writer decision. Diffs are used only to validate that a writer did not forget a declaration.
- **Typed `PlanRecord` methods for every clear:** strongly typed, but it pushes domain operations such as dependency preparation into a growing persistence API and does not naturally compose with atomic state+slices+events lifecycle mutations. The change set retains typed methods while remaining usable in every existing apply path.
- **Generic string-path patch builder:** flexible but not typed. Renames would compile while silently targeting obsolete JSON keys, and callers could clear arbitrary schema locations. JSON paths remain private to the lowering code.

## Lowering and persistence

The change set travels beside typed values until the last step before payload preparation. It is never stored in `state.json`, `slices.json`, events, or the mutation journal.

`prepareJSON` gains a private typed lowering argument. Its order is:

1. Marshal the typed state or slices value. Migrated zero values are absent because their tags use `omitempty`.
2. Lower the artifact-specific change set into that marshaled JSON object. The lowering table is the only place that knows JSON key names. It inserts the same representations Tao writes today: `null` for cleared pointers/blocks, `""` for strings, `[]` for slices, and numeric zero where applicable. A review replacement inserts every known review field, including `findings: []` and `commit_message: null` when empty.
3. Deep-merge that update over the existing artifact exactly as today. Unknown top-level, nested, and per-slice keys therefore survive.
4. Indent once and return the final bytes.

Those final bytes are prepared once, put in the mutation journal, and installed exactly. The seam does not alter journal creation, settlement, replay, or recovery semantics in `mutation_journal.go`.

### Apply paths

- **`applyArtifactMutationLocked`:** the lifecycle mutation returns state, slices, events, and its change set. For preserving-detail calls, the caller supplies the change set beside `baseline` and `intended`. After any three-way rebase, the typed intent is applied to the rebased values, then state and slices each call `prepareJSON` with their projection of the same change set. The resulting bytes are journaled as one mutation.
- **`applyStateArtifactUpdate` (`PersistState` rebase):** `PersistStateChanges` passes the record baseline, intended state, and state intents. The ordinary field rebase runs first; declared replacement/clear intent is then applied to the rebased state so a concurrently settled non-zero value cannot override an explicit clear. `prepareJSON` receives the state projection. Preserve-only `PersistState` passes an empty change set.
- **`applySlicesArtifactUpdate`:** the callback receives a change set bound to the refreshed working detail. Slice clears are keyed by stable slice ID, not array index. After the semantic callback, the code verifies that each target ID still exists and passes the slices projection to `prepareJSON`.
- **State-plus-event updates:** `applyStateEventMutationLocked`, used by review writers, follows the same state projection and lowering rule as `applyStateArtifactUpdate`; events remain ordinary append-only values.

No path may lower clears after `prepareJSON`, patch a journal payload, or prepare a second copy for installation.

## Rebase and recovered mutations

Clear intent is metadata independent of the Go zero value, so `rebaseArtifact` cannot discard it when `baseline == intended` but a concurrent settled value is non-zero. Rebase proceeds in two phases:

1. `rebaseArtifact` computes the normal three-way value merge and retains its existing slice-ID structural handling.
2. The change set reapplies its declared writer-owned fields to the rebased result. A clear installs the canonical zero value; review replacement installs all known replacement fields; a slice clear resolves its stable ID in the rebased slices.

A concurrently removed clear target is a structural conflict and fails before payload preparation. Concurrent unknown fields are not conflicts because the JSON merge preserves them.

`artifactMutationWasRecovered` continues to match the requested semantic postcondition and events, not transient intent metadata. To form that postcondition, the recovered path reruns the same mutator against the stale pre-attempt detail and applies its change set, including canonical empty slices for `[]` clears. A replayed artifact containing `null`, `""`, or `[]` therefore compares equal to the requested typed result, and already-recorded events retain their current matching rules. The specialized `recoveredArtifactMutationMatch` path (including blocked continuation) likewise reruns the typed clear operation when constructing its expected postcondition. A recovered match returns without preparing or journaling another mutation.

## File and adapter parity

Lowering happens before the store boundary. To avoid losing presence information by decoding and re-marshaling an `omitempty` field, `ArtifactStore`'s state and slices installation methods will accept the already prepared byte payloads rather than typed `State`/`SlicesFile` values. `artStoreAdapter.settleMutationLocked` forwards an unchanged copy of `journal.State.Payload` and `journal.Slices.Payload`; it does not unmarshal them. Events keep their existing typed append contract.

Consequently file-backed settlement and `artStoreAdapter`-backed settlement receive byte-identical state and slices payloads, including explicit clear keys and preserved unknown fields. Contract tests will run the same mutation through a file store and a payload-capturing adapter store and compare the prepared bytes.

## Struct tags and forgotten declarations

Each migrated field flips to `omitempty`, making it **preserve by default**:

- `Workspace.DependencyFailure` and `Workspace.DependencyFingerprint`
- `Slice.BlockerNote`
- `PlanState.CurrentSlice`
- `PlanState.Review` and the known `PlanReview` fields covered by review replacement

Readers remain unchanged, and the emitted explicit clear values are schema- and byte-compatible with current artifacts.

A direct non-zero-to-zero transition of a migrated field without the matching change-set intent is an error before `prepareJSON`. The validator compares the writer baseline/intended values (not the concurrent settled result) and reports the typed field name and writer path. This diff is a guard only; it never manufactures clear intent. Since change-set clear methods both mutate and declare, normal callers cannot split those operations. Unit tests pin the validator so adding a migrated field to the lowering registry without its transition check fails. Per-writer regressions then catch a writer that accidentally keeps using direct zero assignment.

## Migration order and writer coverage

Each group lands with its `docs/plan-format.md` contract update and focused tests before the next group starts.

### 1. Dependency failure and fingerprint

Writers:

- `internal/workspace.ExecutionPreparer` through `recordDependencyMetadata` and its failure/ready `PlanRecord.PersistState` calls clears `Workspace.DependencyFailure` after a successful retry.
- The same preparation flow clears `Workspace.DependencyFingerprint` when successful-install evidence is unknown, while retaining or setting it when evidence exists.

The writer regression seeds a real `state.json` with a prior failure, fingerprint, and a nested unknown workspace key; runs the actual preparation/persistence path for a successful retry with unknown fingerprint; then asserts `dependency_preparation_failure: ""`, `dependency_fingerprint: ""`, and the unknown key unchanged. A companion adapter case compares prepared payload bytes.

### 2. Blocker note

Writer:

- `PlanRecord.ContinueBlocked` / `continueBlockedMutation` / `MarkBlockedContinued` clears `Slice.BlockerNote` through `applyArtifactMutationLocked`.

The regression seeds a blocked slice plus an unknown key on that slice, calls `ContinueBlocked`, and asserts `blocker_note: ""`, the lifecycle transition, and the unknown key unchanged. It also exercises recovered-mutation matching so retry does not append or journal duplicate work.

### 3. Current slice

Writers:

- `PlanRecord.CompleteSlice` and `CompleteSliceWithOutcome` through `MarkSliceCompletedWithOutcome`.
- `PlanRecord.RemoveSlice` and `SkipSlice` through `markPlanEdited` when editing removes the current selection.
- `PlanRecord.Reopen` and `ReopenForced` through `Reopen` when establishing a no-current-slice rework queue.

Each distinct public writer shape gets a table case. The case seeds a non-null `plan.current_slice` and an unknown key under `plan`, invokes the public record operation, and asserts `current_slice: null` plus preservation of the unknown key. Completion also checks its coupled slices/event journal behavior; edit and reopen cases check their own lifecycle postconditions.

### 4. Plan review block

Writers:

- `PlanRecord.RecordReviewCompleted`, reached by the normal review flow in `internal/run/review.go`.
- `PlanRecord.RecordReviewError`, reached by the best-effort finalizer error flow in `internal/run/finalize.go`.
- `SetPersistedReview` remains the in-memory publication helper used after the review creator has already persisted the review. It will delegate to the same typed review-replacement normalization, but it must not independently write or create clear intent.

Regression cases first persist an approval containing findings, commit message, base/head/agent data, and unknown keys both under `review` and under `plan`. They then invoke each durable public writer with a replacement that omits the stale values. Assertions cover every known replacement field, especially `findings: []` and `commit_message: null`, while both unknown keys survive. A separate typed `ClearPlanReview` seam test asserts `review: null`; no current production writer is invented merely to exercise it.

## `clearable_fields_test.go` during migration

`clearable_fields_test.go` remains the registry for fields that still depend on non-`omitempty` emission. After each group migrates:

- remove that group's tag-driven “direct zero clears” round trip;
- assert its JSON tag now contains `omitempty` and a preserve-only direct write does **not** erase the seeded value;
- assert an undeclared non-zero-to-zero transition is rejected by the seam validator; and
- leave the existing round trips intact for non-goal fields such as `LastRunCommitPolicy`, `LastRunStartingDirty`, and `MergeCommitIntent` until a follow-up plan migrates them.

This mid-migration split prevents both regressions: migrated fields cannot silently fall back to tag-driven clearing, and unmigrated clearable fields cannot accidentally gain `omitempty` before they have typed writers.
