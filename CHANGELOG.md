# Changelog

All notable changes to Tao are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project intends to follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Weeks begin on Monday. Beta releases may change before Tao reaches its first
stable release.

## [Unreleased]

## [0.1.0-beta.1] - 2026-08-13

Tao's first public beta introduces the complete local-first planning, execution,
review, rework, and integration workflow. Beta binaries are available for macOS
and Linux on AMD64 and ARM64.

### Week of 2026-08-10

#### Added

- Users can turn actionable GitHub review threads into deduplicated rework slices with `tao rework --from-pr`, preview the result first, and avoid repeating feedback Tao has already consumed.
- Teams can create Tao-managed pull requests that follow repository conventions for branch names, titles, descriptions, labels, and assignees.
- Users can keep the active plan, slice, phase, elapsed time, and agent usage visible in a pinned terminal header while a run is in progress.
- Teams that routinely use pull requests can set that preference once per repository while still overriding it for an individual run.
- Pi users can compose replies in a private Vim workspace with the latest assistant response available as context, without mixing that context into the outgoing draft.
- Users can manage plans across repositories from `tao ui`, including reading details and logs, approving work, queueing plans, and launching detached runs without leaving the terminal.
- Zsh users get command, alias, flag, and context-aware argument completion that stays synchronized with Tao's command registry.
- Operators can inspect merge-batch agent usage and timeout records when diagnosing expensive or interrupted integrations.

#### Changed

- Direct runs and queued runs now follow the same automatic-rework rules, so budgets, restart behavior, and reruns remain predictable in either workflow.
- Run and merge agents now share the same timeout, permission, progress, and safety behavior, making provider behavior more consistent across workflows.
- Repository and cross-repository insights now present sections in a stable order, making reports easier to scan and compare over time.
- Plan lists, monitor views, and queue tables now align Unicode names and identifiers correctly.

#### Fixed

- Resizing a terminal from too small to usable no longer lets new log output overwrite the restored pinned run header.

#### Reliability

- Slice commits and single or batch merges have broader crash-and-drift coverage, reducing the chance that an interrupted operation resumes against the wrong Git state.

### Week of 2026-08-03

#### Added

- Users can export a sanitized Markdown snapshot of a plan for coworkers without sharing raw local artifacts or untrusted captured content.
- Users can choose a planning-only report that excludes execution and review data, while full reports provide safe effort totals and abbreviated commit evidence.
- Insights reports now explain when Tao stopped an automatic-rework loop, helping users distinguish a safety stop from uncontrolled repetition.
- `tao monitor` now shows when a standalone review is preparing, verifying, or waiting on the review agent, so long reviews no longer appear idle.

#### Changed

- Pull-request users can finish Tao's local workflow once an approved review and recorded PR point to the same head; Tao still clearly distinguishes this handoff from proof that the PR was merged.
- Monitor output is narrower and easier to scan, with combined slice progress, shortened active-slice identifiers, and readable runtimes.
- Agent prompts now ask for broader verification, clearer inputs, and more stable review findings, helping plans converge with fewer avoidable rework cycles.
- CLI commands now delegate shared lifecycle and validation rules to common domain services, reducing inconsistent behavior between commands that perform similar work.
- Plan updates now clear obsolete lifecycle fields explicitly while preserving unfamiliar fields, making older and forward-compatible plan data safer to edit.
- Contributors now have CI documentation that matches the checks actually run by the project.

#### Fixed

- Concurrent reviews, runs, and rework commands now recheck authoritative plan state before changing it, preventing stale operations from overwriting newer results.
- Interrupted workspace rebases can resume only when the exact expected commit series is present, with clearer guidance when manual recovery is required.
- Rebase checks now work for users who have not configured a global Git name and email.
- Rebase safety checks now handle insertions surrounded by repeated formatting lines without moving the edit to the wrong location.

### Week of 2026-07-27

#### Added

- Users now see preparation, verification, and agent-review progress during standalone reviews instead of waiting without feedback.
- Users can inspect bounded agent usage, run failures, review outcomes, and repository trends through `tao insights` and compact planning digests.
- Users can review sanitized trends across every registered repository, including recent agent-log signals, without requiring all source checkouts to be available.
- Users with multiple supported agents installed can manage all Tao prompts in one operation and receive a warning when managed prompts are stale.
- Agent users can capture useful context from an active session directly into a repository note with `/tao-note`.
- Users can watch active and incomplete plans across registered repositories with `tao monitor`, either as a live display or a one-shot report.
- Standalone installations can update with `tao update` using verified release checksums and atomic replacement, while configurable startup checks can warn or update automatically.
- Users are protected from automatic-rework loops that keep producing different findings in the same files; Tao stops with an explanation and requires an explicit restart.
- macOS users and contributors now receive the same automated build and test coverage as Linux users.
- Maintainers have a repeatable release procedure that verifies the exact commit, publishes immutable SemVer tags, and checks GitHub and Homebrew artifacts.

#### Changed

- Installed agent commands now use the `/tao-` prefix, avoiding collisions with similarly named commands from other tools.
- `tao doctor` now highlights actionable setup problems by default, while `--verbose` retains the complete environment report.
- `tao monitor` now hides damaged plans during normal monitoring while `--show-invalid` keeps them available for diagnosis.
- Automatic rework now remembers recurring-finding stops across direct runs, queues, recovery, and restarts, so changing entry points cannot silently bypass the safety limit.

#### Fixed

- Cancelled or completed verification commands no longer leave child processes running or cause Tao to hang.
- Valid plans no longer receive a warning merely because the currently active slice is still present in the pending queue.

#### Reliability

- CLI tests now use an isolated Tao data home, so contributors can run the suite without creating phantom repositories or locks in their personal Tao data.

### Week of 2026-07-20

#### Added

- Users can plan LLM-assisted work as small slices, approve sensitive steps, run them one at a time, and inspect durable progress before integration.
- Users can validate plans, review lifecycle events, detect base-branch drift, and retain recovery evidence in repository-scoped local storage.
- Users can execute slices in isolated Git workspaces, resume interrupted work safely, review completed diffs, request bounded rework, and merge one plan or a compatible batch.
- Teams can place plans in a durable per-repository queue, run independent work in parallel, recover interrupted drains, and receive best-effort completion notifications.
- Users can choose Pi, Claude Code, OpenCode, or Codex while keeping the same Tao workflow, permission controls, timeouts, structured results, and usage reporting.
- Users can keep repository ideas in a local note backlog, refine them over time, and promote ready notes through Tao's normal guarded planning and execution workflow.
- Agent users can install ready-made prompts for planning, slicing, reviewing, merging, checking repository health, capturing notes, and improving a codebase.
- Contributors can verify changes with CI on Linux and macOS, while maintainers can publish checksum-protected GitHub and Homebrew release artifacts without adding runtime Go dependencies.
- Users can let Tao create scoped Conventional Commits from agent proposals while Tao protects trusted trailers, records intent before Git changes, and recovers interrupted commits safely.

#### Changed

- Verification now works consistently across supported agents and repository layouts instead of depending on Pi-specific execution behavior.
- Equivalent review findings now share a stable identity, preventing superficial wording changes from resetting the automatic-rework budget.
- Temporary Pi transport failures can recover through at most two fresh sessions, while unsafe, timed-out, authentication, and post-mutation failures still stop for user attention.
- Plan authors can mark required implementation inputs explicitly, allowing validation to stop incomplete slices before an agent starts work.
- Implementation agents now propose commit messages, but Tao alone validates and creates automatic slice commits, giving users better messages without surrendering Git safety.
- Contributors get a reproducible `golangci-lint` installation in CI instead of relying on a prebuilt installer action.

#### Fixed

- Users can commit `.env.example` templates without weakening protection for real environment files that may contain secrets.
- `make install` now replaces an existing Tao binary correctly, making local upgrades reliable.
