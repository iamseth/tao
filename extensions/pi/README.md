# Tao Pi Extension

Repo-local [Pi](https://github.com/iamseth/pi) extension that hosts Tao's fast
standalone `/commit` command (`/tao-commit` remains as a legacy alias). It is a
thin proposal wrapper around the Go-owned `tao commit` boundary:

1. `tao commit --context` performs read-only preflight and returns only filtered
   allowed paths/diff, recent history, exclusions, and a context fingerprint.
2. Pi's already selected model proposes one bounded structured message from that
   safe context. The extension does not launch a child Pi process or a nested
   agent session.
3. `tao commit --proposal-file` rechecks the live fingerprint and repository,
   validates the proposal, stages safe paths, and creates the local commit.

The proposal supplies `type`, lowercase `scope`, lowercase imperative `summary`,
and non-empty `what`/`why`; Tao formats the canonical message and owns Git.
Proposal content must not supply `Tao-*` trailers—only Tao may append trusted
evidence. A content-validation rejection gets one repair through the same
selected model; stale context, safety failures, or a second rejection stop with
no deterministic/title fallback. An explicit `/commit --message` is the only
standalone override and still passes central validation and safety. The command
never pushes.

Automatic slice completion, review-backed merge, and active merge-resolution
flows do not call this extension: their already active implementation/review or
resolver agent supplies the proposal directly to the owning Tao transaction.
When Pi is selected, `tao install-prompts` routes the `commit` prompt to this
extension instead of installing a Markdown prompt.

## Layout

- `src/index.ts` — extension entrypoint; default export registers the `commit`
  command via Pi's `registerCommand` API.
- `src/commit.ts` — wrapper workflow (Tao context/finalization calls, selected-
  model proposal and one repair, private temporary-file cleanup). It must not
  duplicate Tao's validation, staging, exclusions, trailers, or Git authority.
- `src/pi-api.ts` — type definitions for the Pi extension API.
- `test/*.test.ts` — Node test-runner suites.

`package.json` declares the entrypoint under `pi.extensions`; Pi's loader reads
TypeScript modules directly, so there is no compile/bundle step.

## Requirements

- Node `>=22.19.0` (see `engines.node`). The extension and its tests run
  TypeScript directly through Node's `--experimental-strip-types` flag.

## Test

The extension suite is outside the repository's Go/Make test gate. Run it after
any extension, prompt-install, or standalone commit-flow change and during the
final repository verification slice.

Run from this directory (`extensions/pi`):

```sh
npm test
# or:
node --experimental-strip-types --test test/*.test.ts
```

From the repo root, point the glob at this package:

```sh
node --experimental-strip-types --test extensions/pi/test/*.test.ts
```

There is no separate build; `tsconfig.json` (`noEmit`) exists only for
type-checking in editors.

## Deploy

`tao install-prompts` (with the Pi agent target) symlinks this source directory
into the Pi agent extensions tree:

```
extensions/pi  ->  ~/.pi/agent/extensions/tao
```

The deploy path and symlink behavior are defined in
[`internal/promptinstall/operations.go`](../../internal/promptinstall/operations.go).
`tao install-prompts --check` reports the symlink status (`current`, `stale`,
`missing`, or `unmanaged`); `--force` replaces a conflicting target. Set
`TAO_PI_EXTENSION_DIR` to override the detected source directory.
