---
description: Commit through Tao, locally unless --push is explicit
agent: build
---

Create one local Git commit by proposing message content to Tao's standalone commit boundary. The default is commit-only; remote mutation requires explicit `--push`.

This is a standalone manual command. Automatic `tao run` slice completion is owned by `tao slice-complete` and never falls back to this prompt. Use this active agent session; do not start another agent or model session.

Tao is authoritative for context filtering, validation, staging, exclusions, trailers, commit creation, and publication. Do not run Git directly, including `git push`; do not independently stage files, inspect excluded content, or treat your own validation as authoritative.

Recognize `--push` only as an explicit command flag before the `--` context delimiter, not inside a `--message` value or contextual prose. Forward it only when explicitly supplied: use `tao commit --context --push` for preflight and `tao commit --proposal-file <temporary-directory>/proposal.json --push` for finalization (including the one content repair). Otherwise omit `--push` from every call. Push preflight remains read-only and requires the current branch's configured upstream; never choose a remote or create an upstream. Tao alone publishes the exact newly created SHA to that upstream without force. A no-op never pushes an older commit.

Workflow:
1. Create a private temporary directory outside the repository with `umask 077` and `mktemp -d "${TMPDIR:-/tmp}/tao-commit.XXXXXX"`. Keep `context.json` and `proposal.json` only in that directory.
2. Run `tao commit --context > <temporary-directory>/context.json`. This preflight is read-only. Read only that returned JSON as repository context for the proposal; do not run `git status`, `git diff`, or `git log` yourself.
3. If Tao reports no allowed changes, report that and stop.
4. Write exactly one JSON object to `proposal.json`, copying `context_fingerprint` exactly from `context.json`, with this shape:
   `{"context_fingerprint":"...","type":"...","scope":"...","summary":"...","what":"...","why":"..."}`
   Use a supported Conventional Commit type, the narrowest lowercase scope, a lowercase imperative summary of at most 72 characters, and useful non-empty what/why text. Do not include `Tao-*` fields or trailers.
5. Run `tao commit --proposal-file <temporary-directory>/proposal.json`. Tao will recheck the fingerprint and live repository before any mutation.
6. If Tao rejects the proposal content, repair it once in this same session using Tao's exact error and retry finalization once. Do not use a deterministic fallback. Do not retry stale-context, repository-safety, or push failures. A push failure is partial success: the local commit remains. Preserve Tao's reported SHA, destination, and recovery guidance; tell the user to inspect and resolve the failure, then manually publish that exact commit without force. Do not rerun commit, amend, reset, or automatically retry publication.
7. Best-effort remove both temporary files and their directory after success or failure. Never leave context or proposal files in the repository.
8. Report Tao's final result, distinguishing local commit creation from successful publication or push failure.

If the user explicitly supplies `--message`, pass the exact complete canonical message to `tao commit --message` instead of generating a proposal. Forward explicit push intent here too with `tao commit --message <exact-message> --push`; otherwise omit `--push`. Tao still owns validation, safety, staging, commit creation, and optional publication. A canonical message requires `<type>(<scope>): <summary>`, a non-empty `What:` section, and a non-empty `Why:` section.

Additional requirements from the user:
{{ .Arguments }}
