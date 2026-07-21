# Tao Pi Extension

Repo-local [Pi](https://github.com/iamseth/pi) extension that hosts Tao's
`/commit` command (`/tao-commit` remains as a legacy alias). The command safely
stages current changes, asks Pi's selected model to infer a conventional commit
message from the staged diff and recent history, and creates a local commit.
When the Pi agent is selected, `tao install-prompts` routes the `commit` prompt
to this extension instead of installing a Markdown prompt.

## Layout

- `src/index.ts` — extension entrypoint; default export registers the `commit`
  command via Pi's `registerCommand` API.
- `src/commit.ts` — commit workflow (git context, message proposal/validation,
  staging, commit creation).
- `src/pi-api.ts` — type definitions for the Pi extension API.
- `test/*.test.ts` — Node test-runner suites.

`package.json` declares the entrypoint under `pi.extensions`; Pi's loader reads
TypeScript modules directly, so there is no compile/bundle step.

## Requirements

- Node `>=22.19.0` (see `engines.node`). The extension and its tests run
  TypeScript directly through Node's `--experimental-strip-types` flag.

## Test

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
