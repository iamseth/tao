---
description: Commit the current changes locally through Tao
agent: build
---

Create one local Git commit by proposing message content to Tao's standalone commit boundary.

This is a standalone manual command. Automatic `tao run` slice completion is owned by `tao slice-complete` and never falls back to this prompt. Use this active agent session; do not start another agent or model session.

Tao is authoritative for context filtering, validation, staging, exclusions, trailers, and `git commit`. Do not independently stage files, run `git commit`, inspect excluded content, or treat your own validation as authoritative.

Workflow:
1. Create a private temporary directory outside the repository with `umask 077` and `mktemp -d "${TMPDIR:-/tmp}/tao-commit.XXXXXX"`. Keep `context.json` and `proposal.json` only in that directory.
2. Run `tao commit --context > <temporary-directory>/context.json`. This preflight is read-only. Read only that returned JSON as repository context for the proposal; do not run `git status`, `git diff`, or `git log` yourself.
3. If Tao reports no allowed changes, report that and stop.
4. Write exactly one JSON object to `proposal.json`, copying `context_fingerprint` exactly from `context.json`, with this shape:
   `{"context_fingerprint":"...","type":"...","scope":"...","summary":"...","what":"...","why":"..."}`
   Use a supported Conventional Commit type, the narrowest lowercase scope, a lowercase imperative summary of at most 72 characters, and useful non-empty what/why text. Do not include `Tao-*` fields or trailers.
5. Run `tao commit --proposal-file <temporary-directory>/proposal.json`. Tao will recheck the fingerprint and live repository before any mutation.
6. If Tao rejects the proposal content, repair it once in this same session using Tao's exact error and retry finalization once. Do not use a deterministic fallback. Do not retry stale-context or repository-safety failures.
7. Best-effort remove both temporary files and their directory after success or failure. Never leave context or proposal files in the repository.
8. Report Tao's final result. Do not push to a remote.

If the user explicitly supplies `--message`, pass the exact complete canonical message to `tao commit --message` instead of generating a proposal. Tao still owns validation, safety, staging, and commit creation. A canonical message requires `<type>(<scope>): <summary>`, a non-empty `What:` section, and a non-empty `Why:` section.

Additional requirements from the user:
{{ .Arguments }}
