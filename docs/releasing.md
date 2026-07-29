# Releasing Tao

This guide is for Tao maintainers publishing a stable release. The checked-in
[release workflow](../.github/workflows/release.yml) and
[GoReleaser configuration](../.goreleaser.yml) are authoritative if this guide
ever differs from the automation.

## What the release workflow does

Pushing a complete stable `vMAJOR.MINOR.PATCH` tag starts the **Release** GitHub
Actions workflow. The current tag filter does not accept incomplete versions or
prerelease suffixes. The job checks out full Git history, installs the Go version
specified by the workflow, runs `make test` and `make build`, and invokes
GoReleaser v2 with `release --clean`.

GoReleaser then:

- runs `go mod tidy` before building;
- builds static `tao` binaries for `darwin` and `linux` on `amd64` and `arm64`;
- injects the complete Git tag into `tao version`;
- packages each binary as a `tar.gz` archive and publishes `checksums.txt`;
- creates the GitHub Release in `iamseth/tao`; and
- updates the `tao` formula in `iamseth/homebrew-tap`.

The workflow's direct prepublication checks are narrower than the repository's
canonical local gate: it runs `make test` and `make build`, not `make verify`.
Run the complete local procedure below before creating the tag.

## Prerequisites

Before releasing, confirm that:

- you can push tags to `iamseth/tao` and inspect its GitHub Actions runs;
- your local Go toolchain matches the version declared in `go.mod`;
- GoReleaser v2 is installed and `goreleaser --version` reports major version 2;
- Homebrew is available in a clean test environment for installation testing;
  and
- the Tao repository has a `HOMEBREW_TAP_GITHUB_TOKEN` Actions secret with
  write access to `iamseth/homebrew-tap`.

The release job uses GitHub's repository token to publish the GitHub Release and
`HOMEBREW_TAP_GITHUB_TOKEN` to push the formula update. A missing or
insufficient tap token allows that publication step to fail.

## Prepare `main`

Start with no local edits, then update `main` without creating a merge commit:

```sh
git status --short
git switch main
git fetch origin --prune
git pull --ff-only origin main
git status --short
```

Both status commands should produce no output. Confirm that local `main` is the
same commit as `origin/main`:

```sh
test "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)"
git log -1 --oneline --decorate
```

In GitHub Actions, confirm the **CI** workflow succeeded for that exact commit on
`main`. Do not release merely because an older `main` run passed.

Run the repository-owned gates from the repository root:

```sh
make verify
goreleaser --version
make release-check
```

`make verify` builds, tests, lints, checks modernization findings, and verifies
that Tao still has no third-party Go module dependencies. `make release-check`
validates `.goreleaser.yml` and requires GoReleaser v2.

## Choose and create the tag

Fetch existing tags before selecting a version so that a published version is
not accidentally reused:

```sh
git fetch origin --tags --prune
test "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)"
```

If this comparison fails, do not create the tag. Restart the preparation steps
for the updated `main`, rerun `make verify` and `make release-check`, and
reconfirm successful CI for the new exact commit before continuing.

The recommended first stable Tao release is `v0.1.0`. For that release, set:

```sh
version=v0.1.0
```

For later releases, choose the next appropriate stable SemVer value. The current
workflow accepts complete stable `vMAJOR.MINOR.PATCH` tags only.

Verify the tag does not already exist, create one annotated tag at the checked
commit, and inspect it before pushing:

```sh
test -z "$(git tag --list "$version")"
git tag -a "$version" -m "Tao $version"
git show --no-patch --decorate "$version"
test "$(git rev-list -n 1 "$version")" = "$(git rev-parse HEAD)"
```

Check the annotation, version, tagger, and target commit shown by `git show`.
Creating the local tag does not publish it.

Push only that one explicit tag ref. Do not use `git push --tags`:

```sh
git push origin "refs/tags/$version"
```

## Verify publication

After the push:

1. Open the **Release** workflow run triggered by `$version`. Confirm it targets
   the same commit inspected above and that its test, build, and GoReleaser steps
   all succeed.
2. Open the GitHub Release for `$version` and verify that it targets that commit.
3. Confirm the release has all four archives: `darwin/amd64`, `darwin/arm64`,
   `linux/amd64`, and `linux/arm64`, plus `checksums.txt`.
4. Download the assets and use `checksums.txt` to verify their checksums.
5. Confirm a formula update commit landed in `iamseth/homebrew-tap` for the new
   version and references the published archives and checksums.
6. In an environment with no existing Tao installation, perform a clean install
   and check the embedded version:

   ```sh
   brew update
   brew install iamseth/tap/tao
   tao version
   ```

   `tao version` must report `$version`.

## Handle failures without changing the tag

A pushed release tag is immutable. Never move, delete, or reuse it, even when a
workflow or publication step fails.

For a transient infrastructure or service failure, inspect the failed step and
retry the existing GitHub Actions run. Preserve the pushed tag so the retry uses
the same source commit and version.

For an error in release content, packaging configuration, or source code, fix
the problem on `main`, let CI pass, repeat the local verification, and publish a
new SemVer version. Do not force-push or recreate the old tag. This keeps the tag,
GitHub Release, checksums, and Homebrew history unambiguous.
