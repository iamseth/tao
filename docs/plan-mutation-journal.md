# Plan Mutation Journal

This note specifies Tao's recoverable protocol for Tao-owned mutations whose
target set can include `state.json`, `slices.json`, optional `review.md`, and
lifecycle entries in `events.jsonl`. A journal has at least one target; it does
not require every target on every mutation. The protocol supersedes the earlier
cross-file-journaling non-goal: legacy tolerance remains, but newly journaled
mutations must settle to one complete result.

## Scope and authority

Each plan may have one transaction intent at `<plan-dir>/.mutation.json`. A
persistent `<plan-dir>/.mutation.lock` file provides an exclusive advisory lock
shared by journal writers, full-plan loads, state-only reads, and direct recovery.
Writers create the lock before installing an intent, and the lock file is then
retained so concurrent processes always lock the same inode. Read-only inspection
of a legacy plan with neither journal nor lock does not create the lock; after an
unlocked legacy snapshot, readers recheck for a concurrently created lock and
repeat the snapshot under it when necessary. A second mutation must not replace
an existing journal. While the journal exists
and validates, it is authoritative: its payloads are the intended target bytes
and event entries, regardless of which target files already match them. Recovery
always rolls forward; it does not infer intent from partially installed targets
and does not roll back.

Absence of `.mutation.json` means there is no journal transaction to recover.
This is the compatibility path for all legacy plans, including plans left in the
old tolerated state/slices gap. Required artifact loading and warning-based
validation remain unchanged for those plans.

### Ownership boundary

`internal/plan` alone owns journal schema validation, payload preparation,
installation, replay, and removal. Run, slice-complete, review, rework, and merge
code request domain mutations through plan records or reload plan artifacts; they
do not interpret journal progress or repair target prefixes themselves. Both
full plan loads and state-only reads settle every target in valid pending intent,
including `review.md` when its payload is present, before exposing
required-artifact state. A journal without `review` leaves the existing review
artifact untouched.

The file is internal recovery metadata, not an agent-authored extension point or
a second source of plan semantics. Conforming writers hold the existing plan
lock and the persistence lock from pending-intent recovery through detail
refresh, mutation evaluation, payload preparation, journal installation,
settlement, and in-memory publication. This includes typed state-only workspace operations for preparation milestones,
dependency evidence, rebase transactions, and compare-and-set HEAD advancement,
as well as final verification, starting branch, and slice commit intent.
Workspace and run callers supply operation inputs rather than editing workspace
state and selecting a generic persistence primitive. Plan records refresh the
settled artifact, evaluate each typed operation there, and publish its postimage
only after settlement. Other state-only writers rebase fields changed since
their record baseline over the latest settled artifact, while slices-only
writers re-evaluate their semantic update on that artifact before installing a
one-target journal. A stale metadata stamp therefore cannot erase a concurrent
lifecycle change. When stale writers both initialize pointer-backed metadata
from a nil baseline, their element fields are rebased against a zero-value
baseline so disjoint metadata is retained. Ordinary lifecycle writers always
refresh from the settled artifacts even when no journal was present, so a stale
detail bound after
another writer settles cannot be mistaken for intentional caller edits and
cannot overwrite a non-overlapping mutation. Intentional lifecycle gate
bypasses use explicit operations such as `ReopenForced`, which apply the
override to that refreshed detail; full-artifact initialization uses the
separate preserving-edits path, `PersistArtifacts`. That path retains the
record's binding baseline, snapshots the caller-intended detail before refresh,
always refreshes the latest settled bundle under the persistence lock, and
rebases the snapshotted state and slice edits over it before preparing its own
journal. Slice entries are matched by stable slice ID rather than array index:
concurrent reopen additions and pending-slice removals define the refreshed
structure, while edits to entries that remain present are merged field by field.
A stale full-artifact writer therefore cannot recreate a state/slices mismatch
by replacing a refreshed slice list with its older-length snapshot. If both the
caller and the refreshed bundle made different structural changes to lifecycle
queues, slice identities, statuses, or dependencies, persistence rejects the
ambiguous merge before preparing a journal and requires the caller to reload.
The rebased postimage is not assigned to the bound detail until
settlement succeeds, so a failed attempt leaves the original caller-intended
snapshot available to a same-record retry rather than turning refreshed
concurrent fields into caller intent. This refresh is required even when no
journal remains: a concurrent lifecycle mutation may have fully settled after
record binding. Neither recovery nor a settled concurrent writer can therefore
silently replace edits made between record binding and persistence.
Writers install at most one intent and publish changed in-memory detail only
after settlement. If a state-only writer recovers earlier intent but cannot
install or settle its follow-on journal, it republishes the caller's rebased
pending state over the recovered bundle and retains that recovered bundle as
the record baseline. A same-record retry therefore still carries the caller's
edit without treating recovered lifecycle fields as caller-owned changes that
could overwrite newer work. A retry also publishes any intent recovered at the
mutation boundary before evaluating the requested mutation, so it cannot derive
new target bytes from stale pre-recovery state. Readers hold the same persistence
lock across journal recovery and required-artifact reads;
they treat a valid journal as authoritative and an invalid one as a hard
boundary rather than trusting whichever target happened to be written.

The v1 journal shape is:

```json
{
  "schema": "tao.plan.mutation.v1",
  "mutation_id": "opaque-unique-id",
  "plan_id": "20260720-162730-example",
  "created_at": "2026-07-20T16:30:00Z",
  "state": {"payload": "<base64 bytes>", "sha256": "<lowercase hex>"},
  "slices": {"payload": "<base64 bytes>", "sha256": "<lowercase hex>"},
  "review": {"payload": "<base64 bytes>", "sha256": "<lowercase hex>"},
  "events": [
    {"payload": "<base64 bytes>", "sha256": "<lowercase hex>"}
  ]
}
```

`state`, `slices`, and `review` are optional payload entries, and `events` may be
empty, but at least one file payload or event is required. `payload` fields are
base64 because they carry bytes, not a second mutable JSON object. State and
slices payloads include indentation and their final newline; a review payload is
the exact review-output bytes supplied by the runtime and may be empty; event
payloads are the JSON bytes before the JSONL newline. Every hash is SHA-256 of exactly the
decoded payload bytes. Unknown fields in the journal and payload entries are
retained by decode/re-encode so additive schema data is not erased.

The journal's `plan_id` must match the selected plan directory's known plan ID
and the ID encoded in each present state or slices payload. The opaque review
payload has no embedded plan ID to validate. Each event payload must decode as an
`Event`, have the same `plan_id`, and carry `mutation_id` equal to the journal's
ID.
`mutation_id` is optional in the long-lived event schema for legacy and
non-transactional events, but required for every event in a journal.

## Preparing payloads

State and slices bytes are prepared once. Before preparation, a state-only
writer applies its baseline-to-intended field changes over the latest settled
typed artifact, while slices-only and multi-target lifecycle writers evaluate
their semantic update directly against that latest detail. Preparation then marshals the typed value, deep-merges it over
the current artifact to retain unknown JSON fields, applies the established
clearable-field rules, indents it, and adds one newline. The
same prepared byte slices are hashed into `.mutation.json` and later passed to
atomic installation; targets must not be marshaled or merged again after the
journal becomes durable.

When present, `review.md` is prepared from the runtime's captured review output
as one exact byte slice. It is not marshaled, deep-merged, JSON-validated, or
newline-normalized; its hash binds those bytes, including an empty payload.

Event payloads are likewise marshaled once after assigning the transaction's
mutation ID. Multiple events in one transaction may share that ID, but their
payload hashes must be unique within the journal. Automatic rework uses this
multi-event boundary to install the generated pending slices and the final state
with `plan_reopened` followed by `rework_round`; callers publish a successful
round only after the combined mutation settles. Queue progress is persisted
separately and may lag this authoritative plan evidence after interruption.
During recovery, an event is identified by the pair `(mutation_id, payload
SHA-256)`, so a crash after appending only a prefix cannot cause either
duplicates or skipped later events.

## Write and settlement order

Under both the plan's existing driver lock and the per-plan persistence lock, a
journaled mutation performs these steps in order:

1. Refuse or recover any existing `.mutation.json`.
2. Reload the settled state, slices, events, and optional review when intent was
   recovered or the writer requires a current bundle. For the preserving-edits
   path, always rebase the pre-refresh caller snapshot's state and slice changes
   since the record baseline over that bundle, without publishing the rebased
   result yet.
3. Clone and mutate the refreshed or rebased detail, and capture any supplied
   review bytes, without changing targets.
4. Prepare every present state, slices, review, and event payload; validate all
   applicable IDs and hashes.
5. Atomically write `.mutation.json`, including file sync, rename, and plan
   directory sync. No transaction target may change before this succeeds.
6. If present, atomically install the exact state payload.
7. If present, atomically install the exact slices payload.
8. If present, atomically install the exact `review.md` payload.
9. Append and sync each event not already present by mutation ID and payload
   hash, preserving journal order. After deduplication, sync `events.jsonl`
   again whenever the journal contains events, including when every event was
   already visible from an earlier append whose sync reported failure.
10. Sync the plan directory after all targets and events are installed, making
    newly created target entries durable before recovery intent is removed.
11. Remove `.mutation.json` and sync the plan directory again.
12. Publish the settled typed lifecycle values to the caller's in-memory detail.
    The optional review file is visible through a subsequent artifact reload.

A failure at steps 6–10 or before the step 11 unlink returns an error and leaves
the journal in place. A target that already has the expected hash is complete
and is not rewritten. Repeating all settlement steps is therefore deterministic
and idempotent. Retrying on the same `PlanRecord` first refreshes its intentionally stale detail
from that replay. Before re-evaluating a multi-artifact lifecycle or edit
operation, Tao derives the requested transition from the stale pre-attempt
bundle. For a multi-artifact mutation, an equivalent event in the refreshed
history proves that the earlier transaction settled only when the recovered
state and slices also exactly match the postimage derived from the stale
pre-attempt bundle. Eventless operations such as continuing blocked work use the
same exact state-and-slices postcondition. If the postimage differs, Tao
re-evaluates the request against the recovered detail so immutable execution
boundaries and completion outcomes still reject conflicting retries. Otherwise,
the retry returns the complete refreshed bundle unchanged. Edit and reopen
operations also recognize an already-settled request from its durable
operation-specific postcondition plus an event whose semantic fields match;
event timestamps and mutation IDs are transaction metadata and do not need to
match a later retry. Continuing blocked work is eventless, so its
in-progress/current-slice/unblocked postcondition is accepted only while the
same mutation call recovers pending journal intent derived from a stale blocked
preimage. Once no journal remains, there is no durable evidence distinguishing a
retry from an ordinary call on an in-progress plan, and the blocked-state
lifecycle gate remains authoritative. Event-bearing state-only mutations use
their equivalent-event rule, including
when re-evaluation would project different state over updates that settled
afterward. A retry therefore cannot overwrite the recovered result or append
the same semantic event under a new mutation ID.

Journal unlink is the in-process commit point. The pre-unlink directory sync
makes all target entries durable, including a newly created `events.jsonl`;
directory sync after removal is still attempted so the removal is durable. If
that second sync fails after a successful unlink, settlement is committed and
the newly installed detail is published. Treating it as an ordinary failure
would leave the caller stale with no visible recovery marker. A crash may
resurrect the valid journal, but replay remains safe because targets and events
are installed idempotently before the journal is removed again.

## Recovery

Plan loading or the mutation entry point checks for the persistent lock and
`.mutation.json` before using mutable artifacts. A present lock is acquired before
inspection. A journal without a lock causes the reader to create and acquire the
lock before recovery; absence of both uses the non-mutating legacy read path,
followed by a lock recheck that closes the race with a first writer. Recovery
holds the lock across the complete journal scan, target replay, event
deduplication/appends, and journal removal, then full-plan and state-only readers
read their required artifacts before releasing it. Recovery validates the
complete journal before writing anything, then runs the same settlement steps.
It is valid to observe any subset of the present state, slices, and review file
targets already at their journal hashes and any prefix of events already
appended. A deduplicated event is not sufficient by visibility alone: recovery
syncs the event file before removing the journal.

Event scanning remains tolerant of unrelated malformed legacy JSONL lines. A
valid existing event suppresses an append only when both its non-empty
`mutation_id` and hash of its exact JSON payload match the journal entry. A
matching mutation ID with different bytes is a conflict and stops recovery;
it is not treated as completion.

## Invalid journals

Malformed journal JSON, an unsupported schema, missing required fields, a wrong
plan ID, invalid state/slices/event payload JSON, a missing or wrong file-payload
hash (including `review`), or an event ID mismatch makes the journal unsafe to
replay. Tao must not modify state, slices, review, events, or the journal in that
case. It returns an actionable error naming `.mutation.json` and
the failed check. Automatic deletion, quarantine, rollback, and best-effort
continuation are forbidden because they discard or guess durable intent.

Unknown fields alone are not malformed. The v1 decoder accepts and preserves
them. A schema other than `tao.plan.mutation.v1` is rejected rather than
partially interpreted.

## Legacy compatibility

Plans without a journal load exactly as before. A legacy plan that has no
persistence lock can be inspected from a read-only directory without creating
one. In particular, pre-journal torn state/slices writes remain readable and
warning-only where existing validation already permits them; recovery must not
synthesize a transaction for historical bytes. Generated automatic-rework
slices without a historical `rework_round` remain valid round evidence for
legacy progress reconstruction and are not repaired or migrated. Existing
events without `mutation_id` remain valid and are never used as journal
deduplication evidence.

The protocol changes persistence, not lifecycle decisions. Commit-intent and Git
recovery gates, review and merge safeguards, locking, telemetry best-effort
behavior, public artifact filenames, unknown artifact fields, and clearable-field
semantics remain unchanged.

## Remaining limitations

The journal covers conforming Tao-owned mutations of state, slices, lifecycle
events, and `review.md` when a runtime review supplies that optional payload. It
does not authenticate files or prevent another process with write access from
forging a valid intent or changing targets after settlement. It does not impose
general artifact size quotas, transact other optional Markdown/context files,
operational logs, or legacy planning-session sidecars, coordinate more than one
plan directory, or reconstruct intent for a historical torn write that has no
journal. A malformed journal requires operator diagnosis; automatic quarantine
or deletion would violate the durable-intent contract.

Established Git commit-intent recovery retains its existing evidence and
safeguards even though the slices artifact write now uses the shared journal and
lock boundary. The journal must not become a substitute for Git evidence, plan
locking, or consumer-level lifecycle safeguards.
