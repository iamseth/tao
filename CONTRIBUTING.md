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
