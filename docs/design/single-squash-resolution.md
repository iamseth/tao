# Single-squash conflict resolution

This document describes single-plan squash resolution mechanics behind the [plan artifact contract](../plan-format.md), alongside the [durable-intent Git drift predicates](durable-intent-drift-predicates.md).

## Decision

Optional `plan.merge_commit_intent` binds `message`, `plan_id`, `source_head`, `default_branch`, `default_parent`, and `created_at` before a single squash mutates Git. `message` is the exact final validated review (or exceptional generated) proposal plus Tao-owned evidence. Matching retries reuse it without another agent call. Historical intents remain exact recovery authority and are not reformatted. For a default squash conflict it may contain an optional `resolution` object.

## Phases

Resolution phases are `requested`, `resolved`, `committed`, `reviewed`, and `rolled_back`; the object durably carries `conflict_files`, `requested_at`, `outcome`, bounded `summary`, exact `changed_paths`, `content_fingerprint`, exact `commit_message`, `resolved_at`, `integration_head`, `committed_at`, an optional independent `review`, and bounded `rollback_reason`/`rolled_back_at` settlement evidence. The review projection has `status`, `verdict`, bounded `summary` and `findings`, exact `base`/`head`, bounded `agent`, and `reviewed_at`; it does not replace the source plan's `review.md` or `plan.review`. Fields not yet reached are present as their zero values after the optional resolution object is created, and plans without resolution evidence retain their legacy meaning.

| Phase | Durable fields at the boundary |
| --- | --- |
| `requested` | `conflict_files`, `requested_at` |
| `resolved` | `outcome`, `summary`, `changed_paths`, `content_fingerprint`, `commit_message`, `resolved_at` |
| `committed` | `integration_head`, `committed_at` |
| `reviewed` | Independent `review`, bound to exact `base`/`head` |
| `rolled_back` | `rollback_reason`, `rolled_back_at` |

Single-squash recovery is phase- and boundary-specific. `resolved` can settle only when the unstaged path set and content fingerprint match the durable proposal at the recorded default parent. `committed` can recover only the exact Tao-created commit with the recorded parent, full message, source ref, default ref, clean worktree, and integration head. `reviewed` authorizes completion only for a completed `approve` bound to that same parent/head; every other verdict is terminal non-authorization.

## Preflight and one-shot request evidence

Before writing `requested`, Tao uses a disposable, read-only confined process to validate the executable, ephemeral configuration projection, RPC initialization, selected model, and local credentials without a model request; this does not prove remote credential validity. A preflight failure sends no attributed prompt, writes no resolution phase, and restores the prepared squash so a later explicit invocation can retry.

## Rearm and consumed-authority predicates

Once written, `requested` is not replayed unless structured `not_transmitted` or explicit rejection evidence proves no prompt was accepted and Tao then restores the exact recorded default/source refs, HEAD, default branch, and clean worktree. An exact compare-and-set clears only that matching provisional resolution and appends bounded `single_merge_resolution_rearmed` diagnostics (`capability`, `prompt_acceptance`, and `failed_at`); the event is history, not authority. Rollback failure, drift, compare-and-set failure, partial transmission, missing response, timeout, post-transmission cancellation, remote authentication rejection, and provider/model execution errors retain `requested` and consume the attempt.

## Rollback

After verification failure or review non-approval, successful exact-parent restoration advances to `rolled_back` and appends durable diagnostic evidence; that inactive phase permits clearing, source-review replacement, rework reopening, and a fresh intent for a changed source. Drift, ambiguous dirt, protected-ref movement, or incomplete evidence is a refusal, not a new baseline. Neither `--force`, telemetry, nor provider output weakens these predicates; rollback moves default only while the exact recorded boundary still matches.
