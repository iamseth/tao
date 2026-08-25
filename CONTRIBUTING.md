# Contributing

Thanks for your interest in improving tao. This project keeps a single source of
truth for how the repo is built and shaped, so this guide stays short and points
you at it.

## Repo shape and conventions

Read [AGENTS.md](AGENTS.md) first. It describes the package layout, the
documentation boundaries between the README, AGENTS.md, and the `docs/` files,
and the behavior that must be preserved when editing. Both humans and agents
follow it, so changes should respect those boundaries rather than duplicating
reference material across documents.

## Build, test, and lint

Use the [Makefile](Makefile) — run `make help` to list the targets. The common
ones are:

- `make build` — compile the binary.
- `make test` — run the test suite.
- `make lint` — run the linters.

Run `make build`, `make lint`, and `make test` before opening a pull request.

## TUI preview workflow

The developer-only `cmd/tui-preview` binary runs the production TUI event loop
against deterministic in-memory fixtures. It does not read Tao's data home or
repositories, and its action keys (`r`, `a`, `m`, and `M`) are deliberately
inert. The preview is not released; `.goreleaser.yml` continues to build only
`cmd/tao`.

Start the mixed fixture interactively:

```sh
make tui-preview
# or choose another fixture
make tui-preview TUI_PREVIEW_ARGS='--scenario stress'
```

Use `--list-scenarios` and `--list-views` to discover stable fixture and view
names. In interactive mode, exercise both Plans and Notes, open plan, note, and
slice details, navigate with the production keys, and resize the terminal along
both axes. Quit with `q` or Ctrl-C and confirm the terminal is restored.

For a reproducible frame that does not require a terminal, select a view and
explicit dimensions with `--plain`:

```sh
go run ./cmd/tui-preview --plain --scenario mixed --view plans --size 100x30 > /tmp/plans.txt
go run ./cmd/tui-preview --plain --scenario mixed --view plans --size 100x30 > /tmp/plans-again.txt
diff -u /tmp/plans.txt /tmp/plans-again.txt
```

Add `--color` when inspecting ANSI styling in plain output. To extend coverage,
add or update a typed scenario in `internal/tuipreview/fixtures.go`; keep fixture
values deterministic and in memory, then cover the scenario through the shared
collectors and production renderers rather than adding external fixture files.

## Dependencies

tao deliberately has **zero third-party dependencies**. Please do not add any new
ones: introducing a third-party module requires explicit approval from a project
owner before it can be merged. CI enforces this with `make verify-no-deps`, which
fails if `go.mod` gains any dependency beyond the main module, so run it locally
before opening a pull request.

## Local-only artifacts

Never commit Tao data-home contents or workspace-local `.tao/` metadata; these
are local-only artifacts. See AGENTS.md for the full list and the rules that
apply when committing manually.
