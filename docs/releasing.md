# Releasing Tao

This guide is for Tao maintainers publishing beta and stable releases. The
checked-in [release workflow](../.github/workflows/release.yml) and
[GoReleaser configuration](../.goreleaser.yml) are authoritative if this guide
ever differs from the automation.

## Release channels

Tao accepts two immutable SemVer tag forms:

- `vMAJOR.MINOR.PATCH-beta.NUMBER` publishes a GitHub prerelease. It builds all
  archives and checksums but deliberately skips the stable Homebrew tap.
- `vMAJOR.MINOR.PATCH` publishes a stable GitHub release and updates the
  `iamseth/homebrew-tap` cask.

Each tag must have checked-in release notes at
`.github/release-notes/<tag>.md`. Other `v*` tags trigger the workflow but fail
validation before GoReleaser can publish anything.

A manual **Release** workflow run is a non-publishing snapshot. It runs the
canonical verification gate, builds every release archive and checksum, and
uploads them as a workflow artifact for inspection.

## What the release workflow does

For a valid pushed tag, the workflow checks out full Git history, installs the
configured Go toolchain and linter, runs `make verify`, and invokes GoReleaser
v2. GoReleaser then:

- runs `go mod tidy` before building;
- builds static `tao` binaries for Darwin and Linux on AMD64 and ARM64;
- injects the complete Git tag into `tao version`;
- packages each binary as a `tar.gz` archive and publishes `checksums.txt`;
- creates a GitHub Release using the checked-in release notes;
- marks beta tags as prereleases and prevents them from becoming latest; and
- updates the Homebrew cask only for stable tags.

`tao update` uses GitHub's latest stable-release endpoint and independently
rejects prereleases, so beta publication does not opt users into beta updates.

## Prerequisites

Before releasing, confirm that:

- you can push tags to `iamseth/tao` and inspect its GitHub Actions runs;
- your local Go toolchain is compatible with the version declared in `go.mod`;
- GoReleaser v2 is installed and `goreleaser --version` reports major version 2;
- Homebrew is available in a clean environment for stable installation tests;
  and
- for stable releases only, `HOMEBREW_TAP_GITHUB_TOKEN` is configured with
  write access to `iamseth/homebrew-tap`.

Beta releases do not require the Homebrew token because the workflow passes
`--skip=homebrew`. The GitHub repository token publishes release assets.

## Prepare `main`

Start with no local edits, then update `main` without creating a merge commit:

```sh
git status --short
git switch main
git fetch origin --prune --tags
git pull --ff-only origin main
git status --short
test "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)"
```

Both status commands should produce no output. In GitHub Actions, confirm the
**CI** workflow succeeded for that exact commit on `main`.

Run the repository-owned gates:

```sh
make verify
goreleaser --version
make release-check
```

Then run the **Release** workflow manually against `main`. Download the
`tao-release-snapshot` artifact and confirm it contains four archives and
`checksums.txt`. Snapshot builds do not publish a release or modify Homebrew.

## Prepare release content

Choose the next immutable version. The first beta is `v0.1.0-beta.1`; later beta
fixes increment the beta number, and the first stable release remains `v0.1.0`.

Before tagging:

1. Add `.github/release-notes/<tag>.md` with user-focused highlights,
   installation steps, limitations, and a feedback link.
2. Move shipped entries from `Unreleased` into a dated version section in
   `CHANGELOG.md`, leaving `Unreleased` ready for subsequent work.
3. Confirm release-note archive names match `.goreleaser.yml`.
4. Commit, push, and wait for CI to pass on the exact release commit.

## Create and push the tag

Set and verify the intended version:

```sh
version=v0.1.0-beta.1 # or the intended stable version
git fetch origin --tags --prune
test "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)"
test -z "$(git tag --list "$version")"
test -s ".github/release-notes/$version.md"
```

Create an annotated tag at the verified commit and inspect it before pushing:

```sh
git tag -a "$version" -m "Tao $version"
git show --no-patch --decorate "$version"
test "$(git rev-list -n 1 "$version")" = "$(git rev-parse HEAD)"
```

Push only that explicit tag ref. Do not use `git push --tags`:

```sh
git push origin "refs/tags/$version"
```

## Verify publication

After pushing the tag:

1. Confirm the **Release** workflow targets the tagged commit and succeeds.
2. Confirm the GitHub Release targets the same commit and uses the checked-in
   release notes.
3. For beta tags, confirm the release is marked **Prerelease**, is not marked
   **Latest**, and did not modify `iamseth/homebrew-tap`.
4. Confirm the release contains Darwin/AMD64, Darwin/ARM64, Linux/AMD64, and
   Linux/ARM64 archives plus `checksums.txt`.
5. Download the assets and verify them:

   ```sh
   shasum -a 256 -c checksums.txt --ignore-missing
   ```

6. Extract each locally runnable archive and confirm `tao version` reports the
   exact tag.
7. For a stable release, confirm the Homebrew cask update landed, then test a
   clean installation:

   ```sh
   brew update
   brew install iamseth/tap/tao
   tao version
   ```

## Handle failures without changing the tag

A pushed release tag is immutable. Never move, delete, or reuse it, even when a
workflow or publication step fails.

For a transient service failure, retry the existing workflow run so it uses the
same source commit and version. For a source, configuration, or release-content
error, fix `main`, let CI pass, repeat all verification, and publish the next
version—for example, `v0.1.0-beta.2`. Never force-push or recreate a published
tag.
