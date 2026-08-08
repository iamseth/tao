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

Rerun every verification command declared for the slice, even if the preserved work appears complete. After verification passes, call `tao slice-complete` as instructed below. Never run `git commit` manually: `tao slice-complete` owns the automatic-policy commit transaction.

{{ end -}}
## Branch rules

{{ if eq .ExecutionMode "current" -}}
Use the current branch where `tao run` started. Do not create or switch branches.

Before making edits, confirm the current branch matches the run packet `repo.branch` when that information is available. If the starting branch cannot be determined or the branch changes during the run, stop and report the blocker.

Direct commits to `main` or `master` are allowed only when that is the starting branch and the commit policy explicitly requests a commit. Otherwise, do not commit after successful verification and metadata updates.
{{ else -}}
Create or reuse a single feature branch for the entire plan.

Never commit directly to:

- `main`
- `master`

If currently on `main` or `master`, create a feature branch named:

```text
<plan-id>
```

If already on a non-main branch, continue using it unless it clearly belongs to another unrelated task.
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
- For validation-only or no-edit slices, run the listed verification commands and avoid broad code review unless a command fails or the slice explicitly asks for review.
- If the slice is ambiguous or blocked, write a clear blocker reason to a temporary file outside the repository, run `tao slice-blocked --plan-dir "{{ .PlanDir }}" --slice-id "<selected slice id>" --reason-file "<reason file>"`, and stop.

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

Run every command listed in:

```text
slices[].verification.commands
```

Use `slices[].verification.source`, when present, to understand why commands were selected. The executable source of truth remains `verification.commands`.

for the selected slice.

If a verification command fails:

1. Attempt to fix the issue if it is clearly within scope.
2. Re-run verification.
3. If the original command fails before tests load because of an invalid verification command, such as a missing cwd, missing config path, command not found, `No test files found`, or a package-cwd path mismatch, classify it as a verification-command failure rather than a code failure.
4. If you can infer a mechanically equivalent corrected command, run it once. Record both the original invalid command result and the corrected command result in `verification_results`.
5. If the corrected command passes, continue to successful completion using the corrected result.
6. If still failing, write a clear blocker reason to a temporary file outside the repository.
7. Run `tao slice-blocked --plan-dir "{{ .PlanDir }}" --slice-id "<selected slice id>" --reason-file "<reason file>"`. If the original verification command was invalid, add `--invalid-command "<original command>" --invalid-reason "<why it was invalid>"` and, when applicable, `--corrected-command "<corrected command>"` to that same invocation.
8. Stop. Do not commit broken work unless the user explicitly asked for a WIP commit.

## After successful implementation

After verification passes, write local files for Tao-owned completion bookkeeping to one private temporary directory outside the repository working tree:

- A notes file containing the slice implementation summary.
- A verification results JSON file containing an array of objects with `command`, `cwd` (the absolute path of the directory the command was executed from), `result`, and `details` fields.
{{ if eq .CommitPolicy "slice" -}}
- A commit proposal JSON file containing exactly one object with `type`, `scope`, `summary`, `what`, and `why` string fields. Use the supported Conventional Commit type and narrow lowercase scope that best describe this slice, a lowercase imperative summary of at most 72 characters, and useful non-empty what/why text. Do not add `Tao-*` fields or trailers; Tao alone appends trusted evidence and creates the commit.
{{ end }}
These are throwaway inputs consumed by Tao, not project files: never write them into the repository, and never stage or commit them. Tao deletes them after successful completion.

Then call Tao to complete the slice:

```sh
tao slice-complete --plan-dir "{{ .PlanDir }}" --slice-id "<selected slice id>" --notes-file "<notes file>" --verification-results-file "<verification results file>"{{ if eq .CommitPolicy "slice" }} --commit-proposal-file "<commit proposal file>"{{ end }}
```
{{ if eq .CommitPolicy "slice" -}}
If Tao rejects proposal content, repair the same temporary proposal file in this active implementation session and retry `tao slice-complete`. Do not start another agent or model session and do not use a deterministic fallback. A rejected attempt leaves all temporary inputs available for repair and must not authorize staging or a commit.
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
- Verification commands and result.
{{ if eq .CommitPolicy "slice" -}}
- Commit hash.
{{ else -}}
- Note that changes were not committed.
{{ end -}}
- Next pending slice id, if any.
- Any risks, notes, or follow-up items.
