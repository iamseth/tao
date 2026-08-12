# Tao Pi Extension

Repo-local [Pi](https://github.com/iamseth/pi) extension that hosts Tao's fast
standalone `/tao-commit` command and context-aware reply composer. It does not
register unprefixed aliases. The commit command is a thin proposal wrapper
around the Go-owned `tao commit` boundary:

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
no deterministic/title fallback. An explicit `/tao-commit --message` is the only
standalone override and still passes central validation and safety. The command
never pushes.

Automatic slice completion, review-backed merge, and active merge-resolution
flows do not call this extension: their already active implementation/review or
resolver agent supplies the proposal directly to the owning Tao transaction.
When Pi is selected, `tao install-prompts` routes the `commit` prompt to this
extension instead of installing a Markdown prompt.

## Reply composer

In Pi's TUI, Ctrl+G overrides the stock external-editor action with a
context-aware reply composer. It opens the current expanded draft and uses the
latest settled, text-bearing assistant message from the active session branch
as read-only reference material. Only the edited draft is returned to Pi; the
reference is kept in a separate temporary file and cannot become part of the
submitted prompt.

For `nvim` and `vim`, the composer opens two vertical buffers: editable
`prompt.md` on the left and read-only, non-modifiable `reference.md` on the
right, with the cursor left in the draft. Other editors receive only the draft
file, matching Pi's stock single-file external-editor behavior. The same
single-file behavior is used when no assistant reference is available. The
composer follows Pi's external-editor setting and environment fallback order.

`/tao-compose-reply` provides the same composition workflow when the Ctrl+G
override is unavailable, including when another extension owns Pi's custom
editor. Set `TAO_PI_REPLY_COMPOSER=0` before starting Pi to disable only the
Ctrl+G override and retain Pi's stock external-editor behavior; the fallback
command remains registered. Editor launch failures preserve the current draft.

### Pi compatibility

The reply composer is verified against Pi **0.84.1**. Confirm the installed
package version with `pi --version`, not the Homebrew cellar directory name,
which can lag the package version.

The Ctrl+G integration relies on Pi internals that should be rechecked after an
upgrade: `app.editor.external` is reserved from extension shortcut
registration; `setCustomEditorComponent` copies the default editor's action
handlers after constructing a custom editor; `CustomEditor.handleInput()` can
intercept the action before delegation; the custom editor's `keybindings` field
is private; and the stock external-editor path expands the draft with
`getExpandedText()` before `getText()`. It also mirrors Pi's TUI
`stop()`/`start()`/`requestRender(true)` lifecycle. The fallback command relies
on `ui.custom()` restoring its entry snapshot, so it applies the edited draft
only after that promise resolves. Editor settings rely on Pi's exported
`getAgentDir` and `CONFIG_DIR_NAME` rather than hardcoded agent or project paths.

## Layout

- `src/index.ts` — extension entrypoint; registers commands and installs the
  reply-editor override when each TUI session starts.
- `src/commit.ts` — wrapper workflow (Tao context/finalization calls, selected-
  model proposal and one repair, private temporary-file cleanup). It must not
  duplicate Tao's validation, staging, exclusions, trailers, or Git authority.
- `src/reply-context.ts` — selects the newest settled assistant text from the
  active session branch.
- `src/reply-composer.ts` — resolves the editor, constructs its argv, manages
  private temporary files, and reads back only the draft.
- `src/compose-reply.ts` — registers and runs `/tao-compose-reply`.
- `src/reply-editor.ts` — installs the custom editor and intercepts Ctrl+G.
- `src/pi-runtime.ts` — dynamically loaded Pi runtime imports and external-editor
  settings lookup; Pi package value imports must remain confined here.
- `src/pi-api.ts` — hand-written type definitions for the Pi extension API.
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
