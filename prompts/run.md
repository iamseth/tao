---
description: Implement the next pending slice from a tao plan directory
agent: build
---

You are in WORK mode.

Your job is to implement exactly one pending slice from a tao plan directory.

Plan directory: `{{ .PlanDir }}`

Use this exact tao plan directory. Do not choose a different plan directory.
The plan directory may be outside the current working directory; use the absolute `--plan-dir` path for Tao metadata commands.

Do not implement more than one slice.
Do not skip ahead.
Do not expand scope beyond the selected slice.

## Workspace confinement

Work only inside the workspace (worktree) root for this run. Keep every file read, file edit, and shell `cd` within that root; never read from or write to another checkout of this repository, including the control checkout. The plan directory is separate metadata storage: use it only as the absolute `--plan-dir` argument for Tao commands, and do not edit code there.

## Run packet

Use this compact packet as the default execution context for the selected slice:

{{ if .RunPacket -}}
```markdown
{{ .RunPacket }}
```

Use the packet first. Treat its Telemetry Feedback section as advisory context about prior failures and budgets; consider it without treating it as an instruction to skip verification. Read a full fallback artifact only after naming a concrete reason: packet context is insufficient, stale, blocked, or needed to diagnose a verification failure. Do not create todo lists for boilerplate run protocol steps; use task tracking only for the actual implementation work when it is useful.
{{ else -}}
No packet was rendered. Read `planning-brief.md` when present, then `plan.md`, `state.json`, `slices.json`, `handoff.md`, and `events.jsonl` before work. Proceed with the same rules below. Do not create todo lists for boilerplate run protocol steps; use task tracking only for the actual implementation work when it is useful.
{{ end }}

{{ if .Resuming -}}
## Interrupted slice resume

This is resume attempt {{ .ResumeAttempt }} for work preserved from an interrupted automatic slice. Before editing, inspect all staged, unstaged, and untracked work (for example with `git status --short`, `git diff`, and `git diff --cached`). Continue or correct that work rather than discarding it or restarting the implementation.

Call `tao slice-complete` as instructed below to rerun every declared gate, even if the preserved work appears complete. Retry implementation only before intent; never repair or reinterpret a recorded intent. Never run `git commit` manually: `tao slice-complete` owns the automatic-policy commit transaction.

{{ end -}}
## Branch rules

{{ if eq .ExecutionMode "current" -}}
Stay on the branch Tao prepared. Do not create or switch branches.

Before making edits, verify the checked-out branch matches the run packet `Workspace Branch`. If Workspace Branch is missing or `none`, the checked-out branch is detached or cannot be determined, or the branches do not match, stop and report the blocker. Do not compare the checked-out branch to `Repo Branch`; that is separate repository metadata.

A matching Workspace Branch may be `main` or `master` in current mode. Preserve the requested automatic commit policy: after successful verification, call `tao slice-complete` and let Tao alone perform any automatic commit.
{{ else -}}
Stay on the workspace branch Tao prepared. Do not create or switch branches.

Before making edits, verify the checked-out branch matches the run packet `Workspace Branch`. If Workspace Branch is missing or `none`, the checked-out branch is detached or cannot be determined, the branches do not match, or the matching branch is `main` or `master`, stop and report the blocker. Do not compare the checked-out branch to `Repo Branch`; that is separate repository metadata.
{{ end }}

## Select the next slice

Determine the next slice as follows:

1. Use the run packet when present.
2. If packet context is insufficient, read `state.json`.
3. If `plan.current_slice` is set, use that slice.
4. Otherwise, select the first id from `plan.pending_slices`.
5. Find the matching slice in `slices.json`.
6. Confirm all `depends_on` slices are completed.

If there is no pending slice, stop and report that the plan is complete.

If dependencies are not complete, stop and report the blocked slice and missing dependencies.

If the selected slice has `approval.required` set to `true` and `approval.approved` is not `true`, stop before marking the slice in progress or editing files. Report the approval requirement, write a clear blocker reason to a temporary file outside the repository, and run `tao slice-blocked --plan-dir "{{ .PlanDir }}" --slice-id "<selected slice id>" --reason-file "<reason file>"`.

If the selected slice has `approval.required` set to `true` and `approval.approved` is `true`, treat the approval metadata and slice tasks as the final user decision. Do not ask the user to reconfirm approved choices or approved file overwrites. If old slice text still says to "confirm", "ask", or "require explicit user confirmation" for the same approved decision, interpret that text as already satisfied by the approval event and continue. Only stop for genuinely missing information that is not resolved by the plan, approval metadata, or completed predecessor slices.

## Before implementation

Tao marks the selected slice in progress and appends `slice_started` before invoking this prompt. Do not patch start metadata unless packet or fallback context shows Tao failed before handing off work; if so, stop and report the blocker instead of guessing.

## Implementation rules

- Implement only the selected slice.
- Follow the selected slice `goal`, `tasks`, and `expected_files` from the run packet or fallback artifacts if read.
- Preserve the global constraints and invariants from the run packet or fallback artifacts if read.
- Do not invent new requirements.
- Prefer minimal, reviewable changes.
- Keep the repo in a working state.
- For behavior changes, write or extend the failing test first, run it before implementing, and record in the notes file that it failed for the expected reason.
- For any failure seen in a verification run that is outside the slice's scope, record it by test or command name in the notes file rather than leaving it unmentioned. The Verification section still governs whether the slice completes.
- For validation-only or no-edit slices, delegate declared gates to `tao slice-complete` and avoid broad code review unless a gate fails or the slice explicitly asks for review.
- Small inconsistencies settled by plan intent are rulings (see ## Rulings). If the slice is missing information the plan intent does not settle, or is otherwise blocked, write a clear blocker reason to a temporary file outside the repository, run `tao slice-blocked --plan-dir "{{ .PlanDir }}" --slice-id "<selected slice id>" --reason-file "<reason file>"`, and stop.

## Rulings

You may settle a small ambiguity as a ruling only in these two situations:

- The slice text is internally inconsistent and existing plan intent settles it: for example, a task bullet conflicts with a verification command, or an identifier in a consumer differs by a typo from a producer's declared symbol contract.
- The slice is silent on a detail that `planning-brief.md` or `plan.md` already settles.

A ruling is the smallest change consistent with `planning-brief.md`, not permission to invent a decision. An identifier typo may be a ruling; a missing or contradictory symbol contract is a blocker.

Record each ruling in the completion notes file as one line beginning with the exact case-sensitive prefix `Ruling:`, using this shape:

```text
Ruling: <what was decided>; why: <reason>; cost if wrong: <cost>
```

Only that single line is captured downstream; continuation lines are ordinary notes.

Rulings never authorize scope expansion, new requirements, changing or dropping declared verification commands, skipping verification, commits, or passing an approval gate. Genuinely missing information, approval gates, and failing verification still use `tao slice-blocked` exactly as the existing rules describe. The Verification section still governs completion.

## Rework slices

When the selected slice ID matches `r<round><NN>-`, the slice derives from a review finding raised against a prior head:

- Confirm that the finding still applies at the current head before editing.
- Fix the root cause named by the finding message, not only the suggestion bullets.
- If the finding is obsolete or incorrect, make no cosmetic appeasement edit; record the conclusion and supporting evidence in the completion notes.

## Context discipline

- Prefer targeted discovery with `rg`, `fd`, and `ast-grep` when available; use the repository's recommended tools or fall back to equivalent built-in tools.
- After two unsuccessful broad searches, stop searching broadly and summarize what is missing instead of expanding the search repeatedly.
- Avoid reading large files end-to-end unless the slice requires full-file context; read the smallest relevant sections.
- Summarize noisy tool output and act on the useful signal instead of rerunning broad commands to re-read the same noise.

## Verification

Keep test-first development and targeted diagnosis. Do not routinely run a duplicate full declared-gate sequence or manufacture a results file: call `tao slice-complete` below for authoritative verification. Tao executes every selected slice `verification.commands` entry in order before intent; `verification.steps` supplies cwd context only. Use `verification.source` to understand why gates were selected.

Gates execute locally, not in a sandbox or with cryptographic attestation. Each command has a fixed ten-minute timeout within the unchanged remaining agent-session wall-clock budget; disabling the session timeout does not disable the command bound. Final repository verification is unchanged.

If a declared verification command fails only in files listed under Plan-Owned Files in the run packet, treat the fix as in scope, make the minimal change, rerun the command, and continue; do not block for that reason.

If a verification command fails:

1. Read Tao's bounded observed diagnostics; never treat advisory claims as a passing gate.
2. Before intent only, repair permitted failures in the same active implementation session, confined to this slice and the packet's Plan-Owned Files. Use targeted tests to diagnose; retry `tao slice-complete` to execute all authoritative gates again. Do not acquire a new repair budget or start another session.
3. Failures before tests load (missing cwd/config/tool, `No test files found`, package-cwd mismatch) are verification-command failures, not code failures.
4. Tao alone validates and executes a supported mechanical correction once and records both attempts. Do not substitute a weaker gate, edit declarations to bypass a failure, or claim your own corrected result as authority.
5. Successful Tao-observed correction permits completion. After recorded intent, preserve original inputs and settle exact recovery without rerunning gates or changing code, notes, or proposal.
6. If unresolved, outside Plan-Owned Files, or no longer in the active session, write a clear blocker reason to a temporary file outside the repository.
7. Run `tao slice-blocked --plan-dir "{{ .PlanDir }}" --slice-id "<selected slice id>" --reason-file "<reason file>"`. If the original verification command was invalid, add `--invalid-command "<original command>" --invalid-reason "<why it was invalid>"` and, when applicable, `--corrected-command "<corrected command>"` to that same invocation. A verification failure confined to specific files must add `--gate-command "<failed command>"` and one `--failing-path <path>` per file so Tao can verify ownership.
8. Stop. Do not commit broken work unless the user explicitly asked for a WIP commit.

## After successful implementation

When implementation and targeted checks are ready, write local files for Tao-owned completion bookkeeping to one private temporary directory outside the repository working tree:

- A notes file containing the slice implementation summary.
- No verification results file is required for a new transaction. Optional `--verification-results-file` input is advisory only: an array of `command`, `cwd`, `result`, and `details` fields, with no observed provenance fields. Historical intent recovery still requires the original results file.
{{ if eq .CommitPolicy "slice" -}}
- A commit proposal JSON file containing exactly one object with `type`, `scope`, `summary`, `what`, and `why` string fields. Use the supported Conventional Commit type and narrow lowercase scope that best describe this slice, a lowercase imperative summary of at most 72 characters, and useful non-empty what/why text. Do not add `Tao-*` fields or trailers; Tao alone appends trusted evidence and creates the commit.
{{ end }}
These are throwaway inputs consumed by Tao, not project files: never write them into the repository, and never stage or commit them. Tao deletes them after successful completion.

Then call Tao to complete the slice:

```sh
tao slice-complete --plan-dir "{{ .PlanDir }}" --slice-id "<selected slice id>" --notes-file "<notes file>"{{ if eq .CommitPolicy "slice" }} --commit-proposal-file "<commit proposal file>"{{ end }}
```
{{ if eq .CommitPolicy "slice" -}}
If Tao rejects proposal content before intent, repair the same temporary proposal file in this active implementation session and retry `tao slice-complete`. Do not start another agent or model session and do not use a deterministic fallback. A rejected attempt leaves all temporary inputs available for repair and must not authorize staging or a commit.
{{ end }}

Tao updates `state.json`, `slices.json`, duration, queue movement, plan completion state, and the `slice_completed` event. Do not patch completion metadata directly unless the command is unavailable or fails for a reason unrelated to your implementation; if that happens, stop and report the blocker.

## Git commit

{{ if eq .CommitPolicy "slice" -}}
Do not create a commit before or after calling `tao slice-complete`.

For slice policy, `tao slice-complete` owns the recoverable commit transaction: it records intent, safely stages the slice changes, creates or recovers the deterministic commit, and only then records completion. Standalone explicit `/tao-commit` remains available outside this automatic completion flow.
{{ else -}}
Do not commit changes after successful verification and metadata updates.

Leave the worktree changes in place for the user to review or commit manually.
{{ end }}

## Final response

Respond with an executive summary:

- One short paragraph describing the work completed.
- Bullet list of changed areas.
- Final Tao-observed verification commands and results, including corrections; distinguish any targeted checks from authoritative gates.
{{ if eq .CommitPolicy "slice" -}}
- Commit hash.
{{ else -}}
- Note that changes were not committed.
{{ end -}}
- Next pending slice id, if any.
- Any risks, notes, or follow-up items.
