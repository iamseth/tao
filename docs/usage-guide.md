# Tao Usage Guide

Workflow judgment for using Tao day to day: when to reach for each command, how
to get the most out of it, and the gotchas the reference docs don't cover.

This guide is a companion to the [README](../README.md) (install, commands, and
the artifact contract in [plan-format.md](plan-format.md)). The README tells you
*what* each command is; this guide tells you *when* and *how* to use them well.

## Choose your workflow

Most work follows the same middle — plan, slice, validate, and run — but the
entry, pacing, recovery, and completion are choices:

1. **Capture or plan:** save an idea as a repository note when you are not ready
   to reason about it; start `/tao-plan` when you are. An unambiguous note can
   take the explicit `tao note run` shortcut, but ambiguous notes should go
   through `/tao-plan note:<id>`.
2. **One slice or the full run:** use `tao run --max-slices 1 <plan>` when you
   want a checkpoint before more work; use `tao run <plan>` when Tao should
   continue through every pending slice, final verification, and review.
3. **Respond to the durable stop:** approve an approval gate, resolve an ordinary
   blocker before `tao run --continue`, rerun the same command for an interrupted
   slice, or use the final-verification action that matches its recorded
   classification. A `changes_requested` review leads to either automatic or
   manually initiated rework.
4. **Choose a completion branch:** after an approved exact-head review, either
   integrate locally with `tao merge` **or** hand off through a pull request.
   These are alternatives, not consecutive steps.

```text
capture note ──→ plan when ready ──→ slice ──→ validate ──→ run
                    ↑                                      │
start planning ─────┘                         approve/fix/recover as directed
                                                           │
                                             approved exact-head review
                                                ├── no PR: tao merge
                                                └── PR: tao run --pull-request
```

`/tao-plan` and `/tao-slice` are **prompts** you run inside Pi or Claude Code. `tao
validate`, `tao run`, and the rest are **CLI commands** you run from the
repository that owns the plan. The handoff between them is the plan directory in
Tao's data home — everything stays local and inspectable on disk.

A useful mental split:

- **Planning prompts** (`/tao-plan`, `/tao-grill-me`, `/tao-improve-codebase-architecture`,
  `/tao-improve-documentation`, `/tao-repo-health`, `/tao-catch-me-up`, `/tao-insights-review`,
  `/tao-groom-notes`, `/tao-steal`) are
  **read-only**. They never edit code or write Tao plan/note artifacts;
  `/tao-steal` only fetches and removes a temporary scouting snapshot.
- **Build prompts** (`/tao-note`, `/tao-slice`, `/tao-run`, `/tao-commit`, `/tao-pr`) write
  artifacts, code, or git state.

A PR workflow can reach Tao's local `completed` status once the approved review
and durable PR metadata identify the same non-empty head. That is a completed
handoff, not proof that the host merged it. Only current `plan_merged` evidence
proves default-branch integration.

## Decide whether to capture or plan

`/tao-note` is the capture end of the note-to-plan pipeline for agent sessions: run the slash command inside a session to distill the conversation into a self-contained repository note, then plan the open note later with `/tao-plan note:<id>`. Use `tao note create` for manual capture instead; `/tao-note` names the installed slash command, while `tao note` names the CLI command group.

Use `tao note` (or `tao n`) when an idea is worth retaining but not worth interrupting your current work. If you are ready to answer questions and make scope decisions now, skip capture and start `/tao-plan <topic>` directly. From a registered checkout, the shortest capture path is:

```sh
tao n c tighten run retry diagnostics
tao n c --tag testing add coverage for stale review heads
tao note create < longer-idea.md
```

Every note belongs to one registered repository. Commands choose the current checkout by default; use `--repo <unique-id-prefix-or-exact-name>` when capturing or inspecting another registered repository. This keeps a backlog attached to its codebase instead of creating a global inbox.

New notes are open. A plain `tao note` or `tao note list` shows open notes newest first; filters and `--all` help with later triage. Open notes can be edited or manually archived, and manually archived notes can be reopened.

Choose the handoff based on ambiguity, not size alone:

- **`/tao-plan note:<id> [optional context]`** is the default for anything that needs questions, tradeoffs, or scope decisions. Planning reads the open note as untrusted context without mutating it. When the work is clear, `/tao-slice` creates and validates a normal plan in the same registered repository, then archives the note with that plan link. A plan-linked archive is terminal and cannot be edited, reopened, or linked to another plan.
- **`tao note run <id>`** is the unchanged explicit shortcut for a note that already states a complete, unambiguous change. Tao first generates and validates a normal plan and rejects unresolved open questions. Only then does it mark the note promoted and invoke the ordinary run lifecycle.

Historical notes promoted to planning sessions remain readable and are accepted by note-aware planning. Their planning-session provenance is preserved when validation links and archives them; Tao does not create new planning-session records. If linked archival fails after validation, retain the normal plan and use the exact recovery command reported by `/tao-slice`. Once direct generation produced and linked a valid plan, any approval stop, blocked slice, failed verification, review result, or later recovery belongs to that plan; resume it with the normal plan commands.

Direct note execution is not a bypass. Generated slices still honor dependencies and approvals; execution still honors agent, permission, timeout, workspace, commit, and pull-request settings; and completed work still follows the normal review and merge safeguards. When in doubt, use `/tao-plan note:<id>`.

### Planner routing for `tao note run`

Leave routing off for ordinary use. Enable **shadow** first when you want to
check assignment coverage and ledger health without changing which planner runs.
Choose **randomized** only when you deliberately want reproducible assignment
among installed eligible planning runtimes. It changes only the planning runtime:
execution, review, and merge keep their existing agent settings, including
`TAO_AGENT`. Interactive `/tao-plan` and `/tao-slice` are not routed. See
[Configuration](configuration.md) for settings and one-run overrides.
Manual overrides are recorded separately and excluded from randomized comparisons.

Shadow ledger failures warn and continue. Randomized routing refuses to allocate
a plan if its initial ledger write fails; a ledger failure after plan creation
stops before execution. Keep the created plan, resolve the reported ledger
problem, and use the exact `tao route link <route-id> <plan-id>` recovery command
printed by Tao (select the same repository). Linking repairs only the ledger,
not plan state or execution authority; resume the existing plan through normal
plan commands rather than generating another one.

The **Planner routing** section of `tao insights` shows policy/arm cohorts,
overrides, attempts without plans, missing links, and matured versus censored
coverage, with inverse-probability weights for randomized comparisons. Check
coverage before interpreting weighted completion: shadow records and overrides
are not randomized evidence, and the ledger never authorizes lifecycle recovery.

### `/tao-groom-notes [focus]` — review the local backlog

Use this read-only prompt in Pi or Claude Code before choosing work from an aging
backlog. It works in any registered repository: it confirms the current checkout
against Tao's catalog, inventories all open notes, then checks full note text,
local linked plans and current code. Optional focus narrows evaluation, not
repository scope or write permissions. Failed registration stops the pass; a
confirmed empty backlog simply needs no action.

The report separates delivered/obsolete requests (ARCHIVE), remaining or blocked
work (RESCOPE), unsupported tiers (RE-TIER), moved references (STALE COORDINATES)
and still-valid requests (VALID), with tier counts and explicit coverage gaps.
An abandoned prerequisite is a broken dependency, not a reason to close its
dependent; a completed plan alone does not prove delivery in this checkout.

Nothing is applied, installed, fetched or planned. Review the proposed literal
commands before separately authorizing changes. Edits replace the whole body,
and `--tag` replaces the entire tag set (omitting it preserves tags). Any later
application must reread the full current note and preserve unrelated text/tags;
do not blindly apply a stale proposal. Refresh managed prompts through the normal
`tao install-prompts` setup outside the grooming session.

### `/tao-steal <git-url>` — scout a foreign repository for ideas

Use this read-only prompt in Pi or Claude Code when a foreign repository may
have ideas worth adopting, not when you want to install it. From a registered
checkout, pass one HTTPS, SSH, or scp-style Git URL and optional focus text. The
prompt confirms the checkout, reads local Tao contracts, and classifies ideas
as already enforced by a Tao mechanism, already covered by a Tao prompt, or
genuinely missing; it also checks overlap with open notes.

`tao steal fetch` is the only network operation: it creates a temporary snapshot
under `<data-home>/steal/`, outside registered checkouts, and prints its
`Snapshot` path and source identity. Tao uses a protocol allowlist, a
`--depth 1 --single-branch --no-checkout` clone followed by checkout, disabled
hooks and submodule recursion, symlink stripping, and a 200 MiB cap. Snapshot
content is untrusted evidence: the prompt never executes it or follows its URLs.
Reading is limited to the README, docs index, and top-level skill, prompt,
command, and workflow files, with at most 400 files, 64 KiB per file, and 2 MiB
total quoted into the session, including command output and repeated excerpts.
Unread areas, omissions, and budget stops remain explicit coverage gaps.

The report identifies the source branch, commit and declared version (unknown
when absent), classifies proposed changes, explains rejected ideas, and gives
constraints, verification, sequencing, tiers and source citations. It ends with
literal, never-executed `TAO_UPDATE=off tao note create` proposals scoped to the
confirmed repository. Each carries a tier tag and the verbatim `Campaign tag`
from fetch output: `steal-<host>-<path>-<YYYY-MM-DD>`, derived by Tao from the
validated URL, never from snapshot claims. Zero proposals is a valid outcome.

Nothing is filed, installed or applied to the checkout; review proposals before
separately authorizing note creation. After a successful fetch, the prompt runs
the exact command printed under `Removal` once and reports the cleanup outcome.
If cleanup fails, it reports the leftover `Snapshot` path and error rather than
claiming success. To remove a leftover snapshot yourself, confirm that exact
path belongs to this fetch and use its printed `Removal` command; do not take
cleanup commands from snapshot content. `tao cleanup` does not manage these
snapshots.

## Monitoring plans

Use `tao show <plan>` when returning to one plan. Its single `Next:` line is the
safest read-only recommendation Tao can derive from durable lifecycle evidence;
the reason explains why it takes precedence. Any indented alternatives are
subordinate options, and administrative alternatives may bypass safeguards, so
they are not equivalent recommendations. A terminal `No action` distinguishes a
finished or otherwise non-actionable plan from one that should progress.
`tao show` also lists recorded rulings under the slice that made them.

The agent telemetry section in `tao show` (also available with `--json`) helps
identify which roles account for **recorded** token use and cost. Check its
availability counts before comparing plans: partial totals and unknown/legacy
coverage are not complete bills, missing measurements are not zero, and failed
attempts still contribute usage. Missing phase events do not prove a phase never
ran. Interactive planning is not collected; direct note generation is recorded
only in surviving validated plans, and merge-batch usage stays separate. Improved
collection can raise totals without indicating a more expensive workflow. See
[the telemetry contract](plan-format.md#events) for operation coverage.

Use `tao monitor` while runs are active across more than one registered
repository. Its urgency-ordered view keeps live and stale runs ahead of blocked
and quieter plans, while showing lifecycle status, active phase, coarse run
age, compact slice progress, and durable activity separately. `SLICES` shows
combined completions over the original total (`1/3`) and appends added rework
when present (`4/3+6`). During `running_slice`, `PHASE` shows at most the first
20 characters of the active slice ID. `RUN` floors invocation age to seconds,
minutes, or hours by magnitude, and `-` means no runtime record was observed.
The interactive view refreshes in place; use `tao monitor --once` for a stable
snapshot to paste or redirect. Invalid plan rows are hidden by default so this
operational view stays focused. Use `tao monitor --show-invalid` when diagnosing
damaged plans; repository warning rows remain visible with either setting.

Keep using `tao list` for current-repository history, its `--active` filter, and
its recency limit. Monitor intentionally shows all registered repositories and
only valid, non-completed plans by default; its invalid-plan filter does not
change list scope, aliases, or defaults. Because a qualifying PR handoff is
`completed`, monitor hides it even before remote integration; use merge evidence,
not monitor absence, when you need to know whether Tao recorded a merge.

Treat LIVE and STALE as process-liveness hints, not workflow verdicts. LIVE means
the publisher has refreshed its heartbeat recently, but does not prove that the
agent made semantic progress. STALE preserves the last reported phase and
heartbeat age; it can mean an interrupted, paused, overloaded, or merely delayed
process and does not mean the plan failed. Lifecycle STATUS and UPDATED durable
activity remain the sources for semantic state.

Single-plan merges publish `merge_integrating`, `merge_verifying`,
`merge_recording`, and `merge_cleanup` runtime phases. A live `tao merge` shows
`MERGING` in `tao ui`/`tao monitor`, with readable phase labels such as
`merge: verifying`; `tao show` prints a merge-in-progress line with its PID and
phase (also available as `merge_in_progress` in `--json`). These are best-effort
liveness hints, not merge evidence. Once the owner is gone and its heartbeat is
stale or absent, the existing merge recovery guidance returns.

## Interactive dashboard: `tao ui`

Use `tao ui` for one terminal view of plans and open notes across registered
repositories, or to launch plan actions without copying IDs. It requires a
terminal; use `tao monitor --once` for redirected or pasteable output.

Tabs cycle **History → Backlog → WIP**, opening on **WIP**. Press `c` on WIP
or History to open the selected plan's
**Changes** detail tab, showing a live read-only delta against its review base.
Use `w` to switch worktree/branch scope and `u` to force a refresh; missing
worktrees fall back to branch scope.
**WIP** contains unfinished plans: **NOW** holds plans with immediate actions and
**NEXT** holds planned work. Blocked, failed, and awaiting-review/merge plans stay
in WIP. **History** contains completed and abandoned plans, newest activity first,
without a ten-plan limit; use search and paging to revisit older work. Completed
plans have no default lookback limit; `--completed-window 24h` restricts them to
the last day, while `--completed-window 0` omits them. Skipped
slices do not move their parent plan to History.
The plan list's `I/R/E` column shows impact/risk (L/M/H) and
effort (S/M/L), with `-` when unset; it hides before SLICES on narrow terminals.
The separate **NEXT** action label recommends what to do, not permission to do it.
Filters and their enabled state persist in `<DataHome>/ui-filters.json` and
combine with session-only search. Repository criteria are shared; WIP and History
have independent status selections limited to their respective states. Turning
filters off never changes which page a plan belongs to. See [configuration](configuration.md) for themes.
Batch merge uses the sole enabled repository filter, otherwise the selected plan's repository.

Capture notes and launch note planning from **Backlog** (notes). `TAO_AGENT` selects Pi
(default) or Claude (`claude`) for native foreground planning in the same terminal.
The agent owns terminal input until exit; returning preserves filters and selection.
Launching or exiting alone does not change the note; use `/tao-slice` to create
its plan. Foreground planning ends with dashboard shutdown, unlike detached run,
approval, and merge actions, which survive exit and still enforce normal gates.
Check durable state: ordering, heartbeats, and `stalled?`/`crashed?` labels are
advice or liveness hints, never approval, failure, or merge evidence.

The [README command index](../README.md#command-reference) links the underlying
non-interactive commands. Run `tao ui --help` for exact keys, tabs, actions,
confirmation behavior, note editing, repository selection, and display options.

---

## Planning commands

### `/tao-plan <topic>` — lead with this

Read-only planning session that ends in a **Planning Packet** ready for `/tao-slice`.

**When to use:** at the *start* of any non-trivial change, as soon as you can
phrase a one-line topic. Don't do a separate manual "research the codebase"
phase first — the prompt is built to do that discovery for you. Its rules tell it
to *"inspect the codebase when the answer is likely available there instead of
asking the user"* and to keep that inspection targeted.

**How to use it well:**

- **Pack intent into the topic line.** The argument is free-form. The more
  constraints, hunches, and non-goals you supply up front, the fewer round-trips
  it needs:

  ```text
  /tao-plan add bounded automatic retries to direct plan runs,
     preserve safe interruption recovery and per-plan locking
  ```

- **Treat it as an interview.** It asks one question at a time, each with a
  recommended answer and a reason. Your job is mostly to confirm or redirect —
  your domain knowledge enters when you disagree with a recommendation.
- **Answer only the final question shown.** Tao renders `/tao-plan` once, but some
  agent hosts may show task, progress, or status text before the final assistant
  response. If a clarification looks duplicated, answer only the question in the
  final response. After updating Tao, rerun `tao install-prompts` to apply the
  latest managed prompt instructions.
- **Let it converge.** It stops when the plan is *specific enough to slice*, not
  when it's exhaustive. Don't push for more detail than `/tao-slice` needs.

**What it won't do:** implement code, edit files, or create Tao artifacts. It
stops at the Packet and tells you to run `/tao-slice`.

**Decision guide:**

| Situation | Move |
|---|---|
| You know the topic, even roughly | `/tao-plan <topic with constraints/hunches>` immediately |
| One decision is genuinely thorny | `/tao-plan`, then `/tao-grill-me` on that decision |
| You can't even phrase the topic | Think until you can write one line, then `/tao-plan` |
| You want to "research the code first" | Skip it — name the suspect files in your topic and let `/tao-plan` inspect them |

### `/tao-grill-me [focus]` — interrogate one decision

Interview-only prompt that drills into a specific design decision until the
constraints, risks, and open questions are clear. Same one-question-at-a-time,
recommended-answer style as `/tao-plan`, but pointed at a single hard call rather
than the whole plan.

**When to use:** mid-planning, when one decision is load-bearing and you want it
pressure-tested before it propagates into slices. Run it *within* a `/tao-plan`
session, then return to planning.

### `/tao-improve-codebase-architecture [focus]` — find refactor opportunities

Read-only architecture review. Looks for shallow modules, concepts smeared
across many files, hard-to-change seams, and leaky coupling. Produces a numbered
list of opportunities (each with files, problem, proposed change, benefits,
risks, verification) and a **top-5 prioritization table**.

**When to use:** when you want a structured read on *where* the codebase is
hurting before committing to a refactor — not for a specific feature.

**How to use it well:** it ends by asking which opportunity you want to explore
next. The natural flow is to pick one, then take it into `/tao-plan` → `/tao-slice` to
turn it into executable work. It won't edit anything unless you explicitly ask.

### `/tao-improve-documentation [focus]` — find documentation gaps

Read-only documentation audit. Reviews both prose docs (READMEs, guides, design
notes) and code-level docs (package/exported-symbol comments) for staleness,
gaps, inaccuracy, and missing context. Produces a numbered list of opportunities
(each with files, problem, proposed change, benefits, risks, verification) and a
**top-5 prioritization table**.

**When to use:** when you want a structured read on *where* the docs are failing
readers — drifted from the code, missing for a key concept, or absent at
important seams — before committing to a documentation pass.

**How to use it well:** like its architecture sibling, it ends by asking which
opportunity you want to explore next. The natural flow is to pick one, then take
it into `/tao-plan` → `/tao-slice` to turn it into executable work. It only recommends —
it never edits, creates, or deletes files on its own.

### `/tao-repo-health [focus]` — audit maintenance risk

Read-only audit of repository health: bloat and stray generated artifacts,
duplicate files, dependency/config sprawl, inconsistent structure, and anything
that makes future changes harder to review safely. Output is severity-ordered
findings (each with evidence, impact, recommendation, validation) plus a
prioritized action list.

**When to use:** periodic hygiene checks, or before onboarding work to a repo you
don't know well. It inspects `git status` first so your in-flight work isn't
mistaken for debt, and it marks uncertain findings as hypotheses rather than
overstating certainty. It will not delete, clean, or commit anything on its own.

### `/tao-catch-me-up [period or focus]` — catch up on local changes

Use this in Pi or Claude Code when returning to a repository or before planning
work that depends on recent changes. By default it summarizes commits reachable
from **current HEAD over the last two weeks**, using local Git history only and
excluding uncommitted work. It opens with a compact scope line stating the resolved
window and timezone, branch (or detached HEAD), pinned short hash, and coverage
caveats. Plain-language highlights follow as bullets grouped under **New features**,
**Bug fixes**, and optionally **Other notable changes**. Each bullet ends with the
short commit hash(es) in parentheses for manual follow-up.

```text
/tao-catch-me-up
/tao-catch-me-up last month
/tao-catch-me-up since 2026-09-01, focus on CLI compatibility
/tao-catch-me-up focus on storage architecture
```

Period/focus arguments narrow or adjust the question, not permission to act.
The catch-up does not edit files, create reports, run tests/builds, fetch, or query
remotes. It inspects metadata first, then bounded file summaries and targeted
diffs; it discloses sampling/truncation and shallow or unavailable history rather
than claiming exhaustive coverage. An empty window stays empty, never silently
expanding to older work. This is orientation, not a review or evidence of remote
integration. Refresh the managed prompts with `tao install-prompts` after updating
Tao.

### `/tao-insights-review [focus]` — review Tao-wide experience evidence

Read-only review of how Tao is working across every repository in its local
catalog. Run it periodically, before choosing Tao roadmap work, or after you
notice the same Tao workflow friction in multiple projects. It works only from
the canonical Tao source repository; use the optional focus to narrow judgment,
not to authorize changes.

The workflow starts from this deterministic report:

```sh
tao insights --all-repos --digest
```

Blocked insights use structured gate evidence first: literal `golangci-lint run`,
`go test`, `go build`, and single-target `make lint/test/build` commands yield
`lint_failure`, `test_failure`, or `build_failure`. Only nonempty, all-valid
relative `_test.go` paths specialize these to `*_test_files`. This lexical hint
proves neither ownership nor root cause (including cache or outside-plan issues).
Composite commands, wrappers, quoted commands, and `make verify` do not identify
a failing family. Historical prose uses conservative known failure phrases;
unmatched or empty reasons contribute to counted `other`, not bespoke labels.
Reports, digests, and `blocked_` scorecard labels agree. Historical labels can
therefore change at read time; stored events and lifecycle behavior do not.

`tao insights` reports evidence and coverage; it does not generate advice. Its
bounded digest selects output-token and cost outliers independently so one
metric cannot crowd out the other, and states how many detected outlier plans
were omitted; the full report remains uncapped. Structured event rows add
observed plan breadth and absolute UTC recency (plus repository breadth only in
all-repository scope). These counts are audit observations, not rates, causal
classifications, or authority to retry or recover; when counted historical
events lack timestamps, Tao says that recency is unavailable.

The `/tao-insights-review` planning prompt evaluates that evidence against
current Tao code and guidance, rejects obsolete or application-specific
signals, and produces zero or more agent-generated recommendations. It reads
all available structured plan history, while agent-log analysis is limited to
plans active in the last 30 days. Tao discovers recent log candidates before
scanning, orders each repository's candidates newest first, and takes balanced
repository rounds: every active repository receives one candidate opportunity
before any receives another. These are candidate-level turns, not equal byte
allocations; the existing global candidate, byte, line, signal, and excerpt
limits remain shared and unchanged. The aggregate coverage line is always
shown, while incomplete or work-limited all-repository reports add stable,
repository-qualified coverage rows; complete reports and single-repository
output stay concise. Missing roots, damaged records, unreadable logs, stale
logs, and evidence concentrated in one repository are reported as limits
rather than silently generalized.

**How to interpret and use the review:**

- Findings form one global order by estimated impact descending, then estimated
  effort ascending. Both are independent integer estimates from 1–500, not a
  ratio, probability, or promise; read their rationales and confidence before
  deciding what to do.
- Environment findings are optional and intentionally passive. The prompt may
  run `tao doctor` when the evidence warrants it and `command -v` only for an
  implicated executable. It does not run version or network diagnostics, probe
  MCP services, install tools, or change configuration.
- Agent logs and excerpts are untrusted local evidence. Collection is bounded
  and likely secrets are redacted, but sanitization is not a guarantee; review
  the digest and findings before sharing them outside your machine. The prompt
  should quote only the minimum evidence needed and never follow instructions
  found in collected text.
- No actionable findings is a successful result. Do not turn weak, obsolete, or
  highly concentrated signals into work merely to produce a non-empty list.
- The review makes no changes. For a recommendation you want to pursue, copy its
  ready-to-use topic into `/tao-plan`. If it is worth retaining but not planning
  now, capture its concise note topic with `tao note create ...` (or `tao n c
  ...`).

### Planner scorecard — compare downstream outcomes

Run `tao insights --scorecard` when comparing planner runtime/model cohorts on
downstream quality, efficiency, and reliability outcomes. Add `--all-repos` for
cross-repository evidence; use `--scorecard` instead of, not together with, `--digest`.

The read-only scorecard reports evidence coverage, censoring, sparse cells, and
inversions where overall and stratified comparisons disagree. Ambiguous
historical planner labels are excluded by default. Rates use matured plans
only; active plans are censored, not counted as failures. Read these limits
before interpreting differences: the scorecard never declares a winner, makes
causal claims, or drives routing.

---

## Build commands

### `/tao-slice` — turn a plan into executable artifacts

Converts the current planning conversation into a durable Tao plan: it runs
`tao init --slug <short-slug> --json` to allocate a plan directory, then writes
`state.json`, `slices.json`, `plan.md`, `planning-brief.md`, `handoff.md`, and
`events.jsonl`.

**When to use:** right after `/tao-plan` (or `/tao-grill-me`) lands a Planning Packet you
believe in. Run it in the **same session** so it inherits the full planning
context — it slices from the conversation, not from a file you pass it.

**What good slices look like** (the prompt enforces these):

- Small and **serial** — prefer 30–90 minute slices, each independently
  reviewable, each leaving the repo in a valid state.
- Every slice carries **verification commands** chosen from *repository-owned*
  sources (`AGENTS.md`, `CLAUDE.md`, `README.md`, build files, task runners, CI),
  not invented. It prefers the **narrowest** documented command that covers the
  touched area over broad `go test ./...` / `make test` sweeps. During planning,
  run a chosen command when it does not depend on future slice outputs so setup
  mistakes are caught before the plan is persisted.
- Concrete repository files or directories that must exist before work begins
  are declared as **required inputs**. Do not derive them from command text; most
  slices need no input declaration. See [Required inputs](plan-format.md#required-inputs)
  for the artifact contract.
- Work needing sign-off is marked with an explicit **`approval` gate**, not
  smuggled in as an ordinary slice.

**After slicing:** it recommends `tao validate <plan-id>`. Do that next.

> **Tip:** if `tao validate` warns about a verification command, the usual cause
> is an execution-context mismatch — e.g. a `pnpm --filter <pkg>` command paired
> with a repo-root-relative test path. Prefer package-relative paths. These
> semantic findings are advisory; see [Validation warnings](#validation-warnings).

### `/tao-run` and `tao run` — execute slices

Run **`tao run <plan-id>`** day to day. Its `/tao-run` agent implements exactly
one pending slice per handoff, honoring dependencies and approval, using test-first
checks, and calling `tao slice-complete` for Tao-owned gates before intent, commit,
and bookkeeping. Blockers and failed verification stop work. Single-line `Ruling:`
notes may settle only ambiguities already resolved by plan intent, not missing or
contradictory contracts.

Before intent, a gate failure confined to Git-derived **Plan-Owned Files** permits
minimal in-session repair, including earlier slices' files. Other or unresolved
failures use `slice-blocked` with command/path evidence, not another repair session.
After intent, preserve original inputs and recover without edits or rerunning gates.
Tao owns the declared gates: no duplicate sequence or results file is required,
and agent claims are advisory. Commands run locally, without sandboxing or attestation,
with a ten-minute bound inside the remaining session budget even if session timeout
is disabled. Tao reports failures and supported mechanical corrections; final
repository verification is unchanged. See [observed verification](plan-format.md#observed-slice-verification).

Tao-owned slice, final, and merge gates override inherited `GOLANGCI_LINT_CACHE`
with `.tao/cache/golangci-lint` under the actual execution worktree (the integration
worktree for batch merges). Lint remains mandatory where declared; only its cache
is isolated. These caches disappear when their worktrees are removed. Manual
commands and explicit cache overrides inside commands or build scripts are
outside this guarantee. The same gates default `PYTHONDONTWRITEBYTECODE=1` so
Python test runs do not leave `__pycache__` in the worktree; an explicit environment
value or an override inside the command wins (for example,
`PYTHONDONTWRITEBYTECODE= python3 ...`, which Python treats as unset).
Manual commands are outside this bytecode guarantee too.

**Choose the run size:**

- `tao run <plan-id>` — execute all pending slices through verification and review.
- `tao run --max-slices 1 <plan-id>` — inspect one checkpoint or limit the first
  handoff. The next ordinary run continues the remainder.

**Choose recovery from the durable condition, not from how the failure looked:**

| Durable condition | Action | Why this action |
| --- | --- | --- |
| An approval-gated pending slice is not approved | `tao approve [--slice ID] <plan-id>`, then `tao run <plan-id>` | Approval satisfies the gate; it is not blocker recovery. |
| The plan records an ordinary blocker and you have resolved its stated cause | `tao run --continue <plan-id>` | `--continue` explicitly clears blocker lifecycle state. Tao does not infer resolution. |
| A blocker's fix is a contract change, such as a missing expected file, task, or manual check | `tao edit amend <plan-id> <slice-id> --reason-file FILE [--allow-file PATH] [--add-task TEXT] [--add-manual-check TEXT] [--goal-file FILE]`, then `tao run --continue <plan-id>` | The amendment is journaled with its reason and shown as Operator Amendments; it never changes slice status, clears the blocker note, or bypasses approval, so `--continue` still decides whether the blocker is resolved. |
| A `plan_owned` blocker has an unchanged worktree | Fix the named paths in the plan worktree, then rerun `tao run --continue <plan-id>` | Tao refuses with `blocker unchanged since <timestamp>; fix required in <paths>` only when structured plan-owned evidence and the recorded head/worktree fingerprint still match. Changing the fingerprint lifts only this guard; prose-only blockers are unaffected. |
| A clean isolated automatic slice is blocked on an older execution baseline, and a prerequisite has now produced a strictly newer baseline | `tao run --restart <plan-id>` | `--restart` supersedes that safe blocked boundary and preflights again; it is not a general retry. |
| An implementation handoff was interrupted before completion | Rerun the same `tao run` command | Tao classifies the recorded workspace, branch, head, policy, intent, and dirt before deciding whether resume is safe. `--continue` and `--restart` do not bypass that check. |
| Final verification fails with recorded classification `code` after slice execution in an ordinary run | Let automatic repair continue in the same invocation | Eligible failures generate and run repair slices, then rerun the gate, within the fixed lifetime cap of 2. Repairs count toward `--max-slices`; an exhausted invocation budget stops without appending one. `tao note run` shares this policy; reverification, PR recovery, review, and merge never schedule automatic repair. |
| The plan is already stopped in `verification_failed` with classification `code`, and fewer than two repair slices have ever been generated | `tao run --repair-verification <plan-id>` | This explicit path appends and runs one repair slice for the exact failed gate; it never chains automatic attempts. Plain `tao run` does not repair an already-failed plan. |
| A code-classified failure remains after two generated repair attempts | Repair and commit the source manually on the same plan branch, then run `tao run --reverify <plan-id>` from a clean worktree | Exhaustion is terminal for generated attempts. Reverification accepts the recorded failed head or a clean same-branch descendant after the manual fix; it does not reset or consult the repair cap. |
| Final verification is legacy-unclassified, or its recorded external cause (`tool_missing`, `timeout`, `cancelled`, or `invalid_command`) has been resolved | `tao run --reverify <plan-id>` | Tao reruns final verification without a repair slice at the unchanged failed head. |

The lifetime attempt count includes completed repairs and never resets; generated
repair slices are system-owned and cannot be skipped, removed, or amended. See
[repair evidence](plan-format.md#final-verification-repair-evidence) for durable
stop fields (`verification_repair_stopped` also appears in `tao insights`).
For ordinary pending or blocked slices, `tao edit amend` records a required reason,
validates before persisting, refuses an active run lock, and journals the change;
it never clears blockers or bypasses approval. See [slice edits](plan-format.md#state-lifecycle)
for append/replace semantics. Continue, restart, and explicit verification repair
are single-shot: later automatic rework rounds in that invocation run as ordinary runs.

**Supply later observations separately from approval.** Approval authorizes a
choice or overwrite; it does not deliver facts, and approved decisions need no
reconfirmation. An unfilled observation template remains missing information.
Tao conservatively detects some approval-as-data wording before agent launch and
refuses it with the implicated requirement and amendment guidance. Repeating
approval cannot fix missing facts; repeating `--continue` on this refusal leaves
the blocked state unchanged. This is a narrow heuristic, not a completeness check.
Put observations in the slice contract or a concrete `required_inputs` file with
real content available in the execution worktree, not only the control checkout.
File existence alone does not prove content. A missing external file still fails
whole-plan validation unless an exact direct producer contract exists; do not
invent an agent producer for human observations.

If the refused slice is interrupted and still `in_progress`, amendment is not
yet allowed. Confirm no run is active, write a blocker reason describing the
missing facts, and explicitly block the slice first using its data-home plan
directory:

```sh
tao slice-blocked --plan-dir /absolute/path/to/plan --slice-id <slice-id> \
  --reason-file /tmp/missing-observations.txt
```

This preserves the recorded execution boundary and worktree changes; do not reset
or commit interrupted automatic work. Then amend the blocked slice as below and
use `tao run --continue <plan-id>`. Amendment locking and ordinary exact-boundary
recovery checks still apply; blocking and amendment do not make unsafe recovery
eligible.

For a pending or blocked slice, record later facts with a contract amendment:

```sh
tao edit amend <plan-id> <slice-id> --reason-file /tmp/observation-reason.txt \
  --add-task 'Use the observed emulator result: startup displayed a blank screen after 30 seconds.'
```

The reason file explains why the contract changes. Put the actual observations
in `--add-task`, or use `--goal-file /tmp/observed-goal.txt` containing the full
replacement goal and facts; facts recorded only in the amendment reason are not
a contract change. Only recorded goal/tasks amendments lift the approval-as-data
heuristic; reason-only, file-scope, and manual-check changes do not. Neither an
amendment nor file existence proves factual completeness. These flags do not add
or waive `required_inputs`: declared inputs still undergo execution-worktree
checks, and the amended plan must still validate. After remediation, use ordinary
`tao run <plan-id>` for pending work or `tao run --continue <plan-id>` for blocked
work. Amendments neither clear blockers nor grant or bypass approval; satisfy any
outstanding approval gate separately.

Under `--commit-policy none`, a successful same-head reverification does not by
itself prove that permitted uncommitted work was committed.

Before workspace preparation or launch, every exact same-repository prerequisite
needs current Tao merge evidence ancestral to the selected baseline; advisory
sequence order is not authority. Run plans explicitly; independent plans may run
in separate terminals. Per-plan locks prevent duplicate drivers, not conflicts
between different plans.

In a tall enough TTY, a pinned header shows repository, plan, configuration,
phase/slice, elapsed time, progress, nearby slices, and session/token/cost metrics
above `LIVE OUTPUT`. Redirected output stays plain. Use `--no-run-header` or
`TAO_RUN_HEADER=false` to disable it; [configuration](configuration.md) owns
boolean defaults and diagnostics. Terminal scroll margins discard older visible
lines from scrollback; the full `agent-run.log` remains in the plan directory.

**Prefer `tao validate <plan-id>` before running** for whole-plan findings;
`tao run` preflights only the selected slice.

Execution "done" means completed slices plus persisted repository verification
and review results. Under default slice policy, the implementing agent proposes
each checkpoint's scoped Conventional Commit subject and `What:`/`Why:` body;
Tao validates, adds trusted trailers, persists the message, stages, and commits.
Invalid proposals stop before intent or Git mutation; only the same active session
may repair them, with no title fallback or separate message session.
Broad verification blocks, preferring declared `make verify` over Make/Go fallbacks.
Review inspects exact `base..HEAD` and is best-effort: recorded failures/timeouts do
not fail verified runs. Approval leaves a plan `reviewed` for merge, or `completed`
when recorded PR metadata matches the same non-empty head—not proof of integration.

A changes-requested review triggers automatic rework under ordinary gates:
generate fix slices, run them, and review again. Stops are checked in this order:

- **Attempt cap:** five cycles by default; inspect the remaining findings.
- **Equivalent findings stalled:** consecutive normalized finding sets match;
  the stop repeats the current blockers.
- **Plan agent budget warning:** a configured usage threshold was crossed after
  multiple rounds; inspect the named metric, observed/threshold values, and findings.

Stops preserve the latest review and never approve or merge. Configure the cap
with `TAO_MAX_REWORK_ATTEMPTS`, `tao repo config --max-rework-attempts N|unset`,
or `tao run --max-rework-attempts N <plan-id>`; zero disables the loop.
Environment/defaults → repository → explicit invocation is the precedence order,
including explicit zero. Use repository `unset` to restore inheritance.
Disabling review with `--no-review` or `TAO_REVIEW=false` makes the effective cap
zero, with one explanatory warning if the resolved count was positive.
`--reverify` also executes and presents zero attempts.

**Migration:** replace `TAO_AUTO_REWORK=false` or `--auto-rework=false` with the
corresponding numeric count of zero; `true` maps to five. These deprecated boolean
inputs remain accepted for one release and warn whenever supplied. A canonical
count wins over the boolean in the same layer; higher-precedence layers still win.
Merge-batch review is independent: `TAO_MERGE_REVIEW_MAX_ATTEMPTS` defaults to five
and is not affected by run rework settings.
Repeated locations alone do **not** stop new rework. After successful reopening,
best-effort advisories list normalized locations and rounds: a file-and-line anchor
in two distinct rounds or a file in three rounds of the current window, not
necessarily consecutive. These create no durable event or proof of stalled progress.

A later run refuses a fresh budget without prompting and repeats the persisted
stop reason, including historical anchor-reversal/file-recurrence stops rather
than converting them to advisories. Address the review first; explicit
`tao run --rework-restart <plan-id>` preserves history but makes the current round
a fresh baseline/window. It acknowledges a new bounded budget, never bypasses gates.

Start with `tao review <plan-id>` when returning to slice-complete or reviewed
work: inspect the persisted verdict, summary, findings, and proposal before PR or
merge. Unlike the `/tao-review` agent prompt, this CLI command reads or runs the
stored review. Its exact-diff reviewer supplies the merge proposal; Tao validates
and binds it rather than opening a merge-time message session. Missing, malformed,
oversized, or reserved-trailer proposals downgrade approval to `comment`, which
cannot authorize merge but retains findings for ordinary `tao rework`.
After commits, amendments, or other diff changes, refresh review and proposal with
`tao review --run <plan-id>`. Use `tao staleness` for pending-work base drift instead.

An explicit fresh review prints an advisory notice when the committed base/head
range matches a completed, non-approving prior review, then proceeds normally.
This compares only committed ranges—not prompts, models, or dirty worktree contents.
To deliberately retry with a different model, use
`tao review --run --model <name> <plan-id>`. The notice offers `tao rework` when
eligible; any `tao merge --force` alternative is an administrative exception that
intentionally bypasses review and merge safeguards, not a routine recommendation.
Historical superseded reviews do not authorize either alternative.

Reviewers grade completion needs and user impact: blocker/major findings request
changes; minor-only findings approve with advisory suggestions retained, even when
imperative. Mixed reviews put only blockers/majors in JSON, minors in prose.
Conclusive empty reviews approve; inconclusive ones comment with an explanation,
never hiding blockers. This is prompt guidance, not parser enforcement or retry
authority, and approval still requires the validated exact-diff proposal.

Approver identity prefers OS display name then login name; `TAO_APPROVED_BY` falls
back ahead of `USER`/`USERNAME`, so status may not show the eventual approver.
Run-path sessions default to a 20-minute wall-clock ceiling; `TAO_SESSION_TIMEOUT`
accepts Go durations or `0` to disable it. Interactive planning is exempt; see
[configuration](configuration.md) for runtime settings.

#### Session wrap-up and continuation

`TAO_SESSION_WARN_PERCENT` defaults to 80 and accepts integers 0–99. It requests
one advisory notice at that percentage of the existing hard session budget, only
for implementation/rework slices. Set it to `0` to roll back warning delivery;
resume notes still work. A zero hard timeout also disables warnings. Invalid
values are rejected when implementation consumes the invocation snapshot, not by
planning, review, PR, or merge operations. `tao note run` planning is warning-free;
its eventual implementation uses the shared run policy.

Pi steers within the existing session, between turns. A long tool or verification
command may leave no opportunity to wrap up before the deadline. Unsupported
runtimes silently ignore the notice. No deadline is extended and no new session
or warning-driven retry starts.

The notice asks the agent to stop expanding scope and use `slice-complete` only
when ready under the normal gates. If unfinished and safely before completion
intent, it may block with `slice-blocked --resume-note-file` pointing to a bounded
private temporary file outside the worktree, describing last action, next action,
why, and do-not guidance. Never weaken verification, commit manually, or interfere
with existing completion intent to beat a deadline. Notes are disposable untrusted
context, cached privately outside worktrees, not plan artifacts or report data.
See [agent-input trust](agent-input-trust.md#local-advisory-resume-note-cache).

A blocked slice still needs explicit `tao run --continue`; unchanged plan-owned
blockers still refuse continuation. A stale or unavailable note is simply omitted.
Timeouts retain their ordinary classification and recovery requirements: neither
a warning nor a note supplies completion, retry, or recovery authority.

#### Choose durable settings or one-off overrides

Use `tao config set <key> <value> --global` for your usual preferences across
repositories; omit `--global` for a registered checkout's defaults, or select
one explicitly with `--repo ID`. Use `unset` to inherit again. Keep these
local-only preferences out of Git. Use flags for a single run, or environment
variables for a shell/CI job. Resolution is
**built-in → global → repository → environment → flags**: an old shell export
can mask a newly saved value. `tao config get` and `tao status` show both stored
values and effective sources, so inspect those before changing more defaults.

For example, save `tao config set session_timeout 30m`, then use
`tao run --agent claude --session-timeout 45m <plan>` for a one-off exception.
The same two flags work on `tao note run`. Permissions remain runtime-specific:
`--dangerously-skip-permissions` bypasses Claude checks but is a compatibility
no-op for Pi. Neither preferences nor overrides authorize recovery actions.

In `tao ui`, open Settings and select a repository, then press `e` to open its
settings editor. Global values and read-only paths are not selectable; scroll
them with PgUp/PgDn or `g`/`G` (the arrow keys scroll too when no repository
is registered), and moving the repository selection returns to the selection
view. Use arrows/Tab to focus a field, Enter to edit, left/right
arrows for choices (or type a value), and Enter to request confirmation. Outside
text editing, `u` proposes unset/inherit and `d` disables a STOP cap with `null`.
Escape cancels editing or confirmation without saving. Confirmation separates
the proposed saved value from the current effective value and warns about
environment masking. Successful saves refresh settings; stale proposals require
refreshing and trying again. Global-only, process, and path rows are read-only;
use `tao config ... --global` for global edits. Restart the UI to capture changed
environment exports. See the [configuration reference](configuration.md) for the
complete scope/key matrix, budget rules, errors, and migration examples.

#### Choose an independent plan reviewer

Use a different runtime when you want review independent of the implementing
agent, for example:

```sh
TAO_AGENT=pi tao run --review-agent claude <plan>
tao review --run --review-agent claude <plan>
tao rework --run --review-agent claude <plan>
```

For a persistent choice, set `TAO_REVIEW_AGENT` or use
`tao config set review_agent claude` (or `--global`). Resolution follows the
scoped precedence above: a `TAO_REVIEW_AGENT` export masks a saved value, and an
explicit flag wins over both. `tao config unset review_agent` removes only that
saved value. With no selector, reviews inherit the implementing
runtime, preserving existing behavior. `review` and `rework` accept the flag only
with `--run`; ordinary and `--from-pr` handoffs and automatic rounds retain it.
PR feedback triage itself stays on the implementing runtime.

Choose a compatible review model separately with `TAO_REVIEW_MODEL` or
`tao repo config --review-model NAME`; selecting a runtime does not translate
model names or reset model precedence. Rejection is surfaced without fallback.
Review approval still requires Tao's exact-diff proposal validation; selecting a
runtime grants no lifecycle authority. `--no-review` leaves the unused reviewer
unstarted. Merge reviews, resolvers, and all other merge-owned sessions are
unaffected, and `tao merge` does not accept `--review-agent`.

Before using mixed runtimes, run `tao doctor --verbose` in the registered checkout.
It identifies the implementation and effective plan reviewer using environment and
repository settings; invocation-only overrides are not included. Install any
missing selected executable, put it on `PATH`, and run `tao install-prompts`, then
`tao install-prompts --check`. Installation covers both supported runtimes when
both are installed, regardless of role selection. Inherited single-runtime use
needs only that runtime; `--no-review` never needs the unused reviewer installed.

Doctor is passive setup guidance, not a live-provider test. Pi checks local
startup readiness once if selected for either role, using the shared merge probe;
Claude checks remain executable/prompt diagnostics. Neither proves remote
credentials, service availability, or compatibility with your review model. If a
review fails, correct the selected runtime's setup or review-model configuration,
then explicitly retry with `tao review --run <plan>` (repeat any invocation-only
selector). Tao does not silently switch runtimes or models, and diagnostics never
authorize approval, completion, or Git mutation.

#### Choose models for agent sessions

Use `TAO_MODEL` as a shared base and role settings when implementation, review,
or merge work benefits from a different model. `TAO_RUN_MODEL` covers
implementation and rework slices; `TAO_REVIEW_MODEL` covers plan review and its
proposal correction; `TAO_MERGE_REVIEW_MODEL` covers aggregate merge review;
`TAO_RESOLVER_MODEL` covers merge conflict and rework resolution. Planning
generation, pull-request work, and standalone merge-message generation use the
base model, not a role override.

Each model field follows built-in → global → repository → environment → flags.
An explicit per-invocation `--model` overrides the base and every role. After
resolution, each unset role falls back to the base. Thus a repository base does
not erase an explicit environment role setting; remove that role setting to use
the base. `tao config unset models.run_model`, for example, removes only that
repository's stored role default, restoring inheritance.
With no effective model setting, launch arguments remain unchanged and the
runtime chooses. Defaults are resolved for each invocation, not pinned to a
plan, so a later invocation can use a different model. The exception is a
[recorded rework escalation model](#escalate-late-automatic-rework), which stays
in force for that round's slices. See the
[configuration reference](configuration.md) for flags.

Names are opaque to Tao and passed to `pi --model` or `claude --model`; supplied
values must be non-empty and whitespace-free. Unset an environment variable
rather than assigning an empty string. Pi model patterns are fuzzy: prefer an
exact `provider/id` from `pi --list-models` rather than a short name that may
match another catalog entry. An unknown model fails the session with the
runtime's message; Tao does not retry or fall back to another model. Correct the
setting before trying again under the ordinary recovery rules. Model selection
never authorizes recovery, approval, commit, PR, or merge.

Optional reasoning effort uses the same precedence and role fallback: `TAO_EFFORT`
is the base, with `TAO_RUN_EFFORT`, `TAO_REVIEW_EFFORT`,
`TAO_MERGE_REVIEW_EFFORT`, and `TAO_RESOLVER_EFFORT` for roles. On `tao run`,
`tao review --run`, and `tao merge` (including `--all`), `--effort NAME` overrides
the base and every role. Values are opaque, non-empty, whitespace-free strings
passed to `pi --thinking` or `claude --effort`; runtime rejection fails the session
without retry or fallback. Unset effort adds no launch argument (including when
Pi derives effort from a model suffix). Escalation rounds keep the run-role effort;
there is no escalation-effort setting.

#### Check which defaults apply

Use `tao status` to inspect defaults and their sources, not to infer that every
setting is active everywhere. The [six-path applicability matrix and diagnostic
contract](configuration.md#command-applicability) covers `run`, `note run`,
`rework --run`, `review --run`, `merge`, and `prompt run`. Full execution shares
run settings; fresh review consumes review-session settings, while merge consumes
merge-session settings, never the run-only PR or slice-commit preferences.

Precedence remains built-ins, captured environment, repository defaults, then
explicit registered invocation flags. Explicit `false` and same-as-default values
still override; omitted flags do not. Conflicts report winning sources and remain
errors: fix an incompatible PR/workspace or PR/commit preference rather than
expecting Tao to silently drop PR creation. Malformed consumed environment values
must be fixed even when overridden; unrelated settings are not admitted. Disabling
review normalizes positive automatic-rework attempts to zero with a warning,
including explicitly requested attempts.

Direct note execution inherits automatic-rework limits, escalation, header,
models, permissions and timeout preferences; execution settings are validated
before durable promotion. Both normal and PR-thread `rework --run` validate and
carry their full-run options before persisting rework. These handoffs preserve
ordinary recovery and approval safeguards; planning routing remains separate.

For standalone rendering, `tao prompt run` uses only commit policy and execution
mode, without repository registration or repository-default lookup. Other prompts
consume neither. Existing prompt flag registration remains compatible, including
`--commit=false`; rendering is not execution admission. See the matrix before
changing a global setting to address a command-specific problem.

#### Recover an interrupted slice

Tao may retry an implementation handoff after at most two explicitly structured
transport failures. Today only Pi's `provider_transport_failure` diagnostic
qualifies; generic, authentication, timeout, planning, review, PR, merge, manual,
and unsafe-boundary failures do not. This is fixed safety policy, not a setting,
and each retry uses a fresh provider session after Tao rechecks durable plan and
Git state.

Do not infer recoverability from provider text, telemetry, or the presence of a
partial diff. Exact retry timing and event semantics belong in the
[plan-format contract](plan-format.md#slice-lifecycle).

Rerun the same direct command and let Tao inspect the recorded execution
boundary before touching the workspace. Cross-process per-plan locking prevents
another direct driver from racing that recovery:

- **Isolated, before commit intent:** when the recorded worktree root, feature
  branch, HEAD, `slice` policy, and clean-start evidence still match, Tao resumes
  the agent in place and preserves staged, tracked, and untracked edits. It
  records a numbered resume attempt. Rerunning after another provider failure
  repeats this classification; provider output and telemetry never authorize a
  blind retry.
- **Current checkout or policy `none`:** the work remains manually owned. Inspect
  and verify it, then complete it with `tao slice-complete` and the required
  notes/results inputs, or restore the recorded boundary. Tao does not claim the
  dirt as a resumed automatic run.
- **After `commit_intent`:** do not rerun implementation or create a commit by
  hand. Retry `tao slice-complete` with the original inputs so its deterministic
  transaction can recover or settle the exact commit. If the replay fails while
  staging, the error names the worktree that must change before the replay
  can succeed; Tao does not retry or fall back.
- **Changed or unsafe boundary:** a different root, branch, or HEAD, an active
  Git operation, conflicts, or ambiguous status is a refusal. Inspect the named
  paths and restore the recorded boundary before rerunning Tao.
- **Dirt without an immutable start boundary:** treat it as unrelated until
  proven otherwise. Tao will not turn it into a new clean-start baseline or
  attribute it to the interrupted slice.

For blockers or final-verification failures rather than interrupted implementation,
use the durable-condition table above.

### Decide between automatic and manual rework

A `changes_requested` verdict continues the same plan; do not create a second
plan for the fixes.

- **Prefer automatic rework:** an ordinary `tao run <plan-id>` automatically
  applies the rework gates, creates and runs follow-up slices, and reviews again.
  Choose this when the findings are actionable and you want the bounded loop to
  continue unattended.
- **Choose manual initiation:** use `tao rework <plan-id>` when automatic rework
  is disabled, when you want to inspect the generated slices before running, or
  when you deliberately stopped after review. Add `--run` to hand off
  immediately after reopening.
- **Use PR feedback as separate authority:** use `tao rework --from-pr` for
  unresolved threads on the recorded Tao-created pull request, not for findings
  in Tao's persisted review.

A `comment` verdict never authorizes merge. Historical or runtime-degraded
comments can retain findings even though new review guidance uses approval for
minor-only findings. When a completed comment review carries actionable
findings, `tao show` recommends `tao rework <plan-id>`, which converts them into
rework slices under the ordinary gates without `--force`; automatic rework does
not consume comment findings.
A comment without findings (for example, an inconclusive review, a parser
fallback, or an approval whose proposal was unusable) is not reworkable; use
`tao review --run <plan-id>` after the head changes.

Without `--from-pr`, `tao rework <plan-id>` is the manual form for a persisted
`changes_requested` or `comment` review with actionable findings.

Without `--from-pr`, `tao rework` is gated and non-mutating on refusal. It
refuses unless the plan is reviewed, the persisted review is completed with a
`changes_requested` or `comment` verdict, and Tao can find actionable findings;
approved reviews, reviews with no findings, and unfinished plans are left
untouched. Use `--force` only when you intentionally want to bypass those
ordinary review gates.

**What it does:** Tao deterministically maps each structured finding to one new
pending rework slice, preserving the finding's goal, files, and tasks when
available. Each generated slice carries a deterministic verification command
scoped to the touched package rather than a narrow test-name filter. Tao appends
those slices, flips the same plan back to runnable, records the reopen event, and
keeps completed slices and history intact.

#### Escalate late automatic rework

Choose a stronger model for late automatic attempts when ordinary rework is
still eligible but would benefit from different model capability. Set
`TAO_REWORK_ESCALATION_MODEL` to opt in; without an effective escalation model,
round events and sessions are unchanged. See the
[configuration reference](configuration.md) for repository and
per-run overrides.

`TAO_REWORK_ESCALATION_FROM_ATTEMPT` defaults to 4 and requires at least 1.
Override it with `tao repo config --rework-escalation-from-attempt N|unset` or
`tao run --rework-escalation-from-attempt N <plan-id>`, in the same environment →
repository → invocation order. A threshold above the attempt cap is valid and
leaves no escalation-eligible attempt in that window. Under the default cap of
five attempts, attempts 4 and 5 are eligible. Counting is one-based within the
current automatic-rework window, not the absolute round number or slice count.
`--rework-restart` and a successful pull-request reopen establish fresh
baselines, so the next automatic attempt counts as 1. Manual `tao rework` and
pull-request reopen rounds never escalate; subsequent automatic rounds can.

When Tao opens an eligible automatic round, it records the chosen `model` on
the `rework_round` event. That model stays in force for the round's slices even
if the setting is later changed or unset, or a later run supplies `--model`.
Settings changes affect future reopen decisions, not already recorded rounds;
legacy events without a model use the ordinary resolved run model. Escalation
does not change review or other agent roles.

Escalation neither adds attempts nor bypasses a stop: cap exhaustion, equivalent
findings, and plan budget remain checked in that order. Location advisories,
explicit restart requirements, and ordinary rework gates are unchanged. Model
selection never authorizes recovery, approval, commit, PR, or merge.

#### Follow up on pull-request threads

Use the recorded pull request as a separate, non-forced rework authority when
its exact head has current approved Tao review evidence:

```sh
tao rework --from-pr --dry-run <plan-id>  # persist and preview triage only
tao rework --from-pr <plan-id>            # reopen and create change slices
tao run --pull-request <plan-id>           # implement, re-review, and update the PR
```

`--from-pr` reads unresolved review threads from the plan's recorded PR and
prints path, author, classification, and action. It classifies selected threads
as follows:

- `change` creates an ordinary pending rework slice.
- `question` is reported for a human answer and creates no slice.
- `scope` is reported as scope feedback and creates no slice.
- `unmappable` refuses the reopen until the requested change has a safe file
  mapping.

Resolved threads are ignored. Outdated but unresolved threads remain eligible
because they can still request a valid change. Thread node IDs provide stable
identity: when the selected thread set is unchanged, the real run consumes the
triage already persisted by `--dry-run` instead of reclassifying it. The dry run
never reopens the plan or creates slices.

`--from-authors owner` is the default and selects threads started by the plan
owner (the authenticated `gh` user). Pass `--from-authors all` with `--from-pr`
to include threads started by any author. `--dry-run` also requires `--from-pr`
and cannot be combined with `--run`; `--from-pr` cannot be combined with
`--force`. If you do not need a preview, `tao rework --from-pr --run <plan-id>`
can hand the reopened plan directly to the ordinary run path; use the explicit
`tao run --pull-request` form when that run should push and refresh the recorded
PR.

A successful pull-request reopen starts automatic-rework accounting from its new
round. Earlier Tao-review rework rounds do not consume the new cap, equivalent-
finding check, or recurring-file window. Tao reads GitHub threads only: it never
posts replies and never resolves threads. After Tao updates the PR, a human must
answer questions and reply to or resolve host threads as appropriate.

Rework always reopens the same plan on its existing branch. It does not create a
child plan, does not discard the completed work, and does not mutate git state.
Direct `tao run` normally performs the Tao-review rework/run/review loop
automatically. Use the standalone command when automatic rework is disabled or
when you want to inspect the generated slices before running them; add `--run`
to hand the reopened plan back to `tao run`.

### `/tao-commit` — conventional commit, optionally pushed

Creates one local commit through Tao's standalone boundary. Tao first returns
only filtered allowed context and a fingerprint. The active agent/model proposes
`<type>(<lowercase-scope>): <lowercase-imperative-summary>` with non-empty
`What:` and `Why:` sections; Tao then rechecks the live repository, validates the
proposal, excludes `.tao/`, suspected secrets, and generated output, stages safe
paths, appends any trusted evidence, and creates the commit. By default it is
commit-only: no remote mutation occurs.

**When to use:** for an explicit standalone commit outside a run, or after
choosing `tao run --commit-policy none`. This command is intentionally fast and
does not start a nested agent process; invalid proposal content gets at most one
repair from the same selected session. Safety or stale-context errors stop, and
invalid content has no deterministic or title fallback.

When you deliberately own the complete canonical message, `/tao-commit --message`
passes it through the same central validation and safety boundary. This is an
explicit standalone override, not an automatic-workflow escape hatch. Automatic
runs never delegate slice commits to this command: `tao slice-complete` owns the
recoverable transaction and Tao owns Git.

Use `/tao-commit --push` when you also want Tao to publish the new commit; it
works with generated proposals or an explicit `--message`. Pi's extension and
Claude's managed prompt forward this explicit flag to Tao, never run Git
directly, and leave no-flag calls local-only. Put `--push` before any `--` context
delimiter; mentioning it in context or message text does not opt in.

The current branch must already have a configured upstream. Tao checks that
upstream before committing, rechecks the branch, destination, and created HEAD,
then publishes the exact newly created SHA without force. It does not choose a
remote, create an upstream, or publish older commits on a no-op. Context
preflight remains read-only even with `--push`.

If publication fails, **the local commit remains**. Tao reports its SHA,
destination, and recovery guidance. Inspect and resolve the failure, then
manually publish that exact commit to the reported destination without force.
Do not rerun `/tao-commit`, amend, or reset to retry publication; neither wrapper
automatically retries a push or treats its failure as a message-repair request.

Both standalone context generation and finalization refuse an active
Tao-managed plan worktree before exposing diff context or mutating Git. The
canonical repository identity, exact physical worktree path, and active plan
metadata identify candidate ownership; branch names alone are never used.
A switched branch or detached HEAD that disagrees with the recorded branch fails
closed as unresolved ownership. Follow the bounded path in the refusal. Ordinary
blocked work reports `tao run --continue`; restart is reported only when durable
slice metadata proves
an isolated automatic pre-intent boundary, and the run path still checks the live
cleanliness and newer-baseline requirements. Manual/current-checkout and
post-intent states report `tao slice-complete` so operators preserve or settle
the existing completion boundary rather than rerunning implementation. Failed
final verification reports its classification-aware repair or reverify path;
when automatic rework is disabled or deliberately stopped, review findings
report `tao rework` for manual initiation. The control checkout, unrelated
worktrees, cleaned workspaces, and repositories with no exact active plan match
remain available.

## Choose one completion branch

Both branches start from completed slices, successful broad verification, and a
current approved review for the exact branch head. Choose **one** based on who
owns the integration gate:

- **No PR:** use `tao merge <plan-id>` when the persisted Tao review is the human
  gate and you want Tao to integrate, verify, record `plan_merged`, and clean up.
- **Pull request:** use `tao run --pull-request <plan-id>` when the hosting
  provider's PR workflow owns the handoff. Use `/tao-pr` only for a manual,
  agent-driven PR that intentionally does not update Tao plan lifecycle.

Do not run `tao merge` merely to make a qualifying PR plan look complete in Tao.
After the host merge reaches local default, `tao merge` may optionally record
actual integration evidence, but that is evidence/cleanup follow-up rather than
the next step in a linear recipe.

### `/tao-pr` — open a pull request

Inspects the Git state, pushes if needed, and opens a PR using the automated
path's reviewer-facing conventions. It reports tests from repository commands
rather than Tao lifecycle bookkeeping and returns the PR URL.

**When to use:** after a run's work is committed and you want a PR by hand.
`/tao-pr` remains agent-driven, accepts additional user direction, and does not
record or mutate Tao plan lifecycle state. Refresh installed prompt copies after
an update with `tao install-prompts --force`.

Equivalent automated path: `tao run --pull-request`, which is gated — it's
rejected with `--commit-policy none`, or when the run is not in
`--execution-mode isolated`. The automated path requires the current approved
exact-head review proposal, uses its Conventional Commit subject verbatim as the
title, records the exact branch head, and deterministically owns lifecycle
metadata. Typed plans also receive their category label and every new PR is
assigned to the authenticated GitHub user. When the recorded head matches the
approved review head, Tao's lifecycle is complete even though the hosting
provider still owns integration.

### `tao merge` — integrate an approved plan without a PR

**When to use:** after `tao run` has completed every slice and its broad final
verification, and you've read the persisted review with `tao review <plan>`.
Use it for the solo workflow where the review is the human gate instead of a PR.

`tao merge <plan>` refuses by default unless:

- every slice is complete and the plan is reviewed and approved;
- the recorded review base matches `git merge-base <default> <plan-branch>`, so
  the review covered the exact diff being integrated;
- the recorded review head matches the plan branch tip, so commits added after
  the review cannot merge unreviewed (rerun `tao review --run <plan>` after
  follow-up commits);
- the plan worktree is clean.

Use plan-detail **Changes** to understand the delta, not to decide that merge
gates have passed. Branch scope shows committed base/head content; worktree
scope can include unreviewed edits. The shared review-base strategy is live:
a moved default branch or current-strategy checkout need not reproduce a
historical review's exact diff. Review and ancestor labels are advisory, and
neither scope grants approval or merge authority; the exact recorded evidence
and ordinary merge gates still decide.

**What it does:** Tao checks out default, squash-applies the reviewed branch,
and reuses its approved proposal for one commit with trusted `Tao-Plan` and
`Tao-Source-Head` trailers, without another message session. Legacy approvals
without proposals remain readable; they and forced merges lacking usable approval
may generate one proposal from the exact diff before mutation. Failure stops before
intent, staging, or commit, never falling back to a title. Checkpoint history stays
on the source branch until managed cleanup; `--no-squash` preserves it by rebasing
and fast-forwarding default.

**Single-plan conflict behavior:** an ordinary squash conflict runs one confined
resolver session in the default worktree. Tao alone validates the edits and
proposal, commits, verifies, and asks a fresh reviewer to review the exact
integration; only `approve` authorizes merge. A failed attempt restores the
default boundary when it still matches, never overwriting drift. Tao never
retries automatically; a later explicit `tao merge` may retry the attempt only
when Tao reports it was rearmed. See the [resolution contract](plan-format.md#state-lifecycle)
for phases, readiness checks, and rearm predicates.

`--force` does not bypass resolver validation or independent review; `--no-verify`
skips only command verification. Use `tao doctor` to check local resolver readiness
without a model request; remote credential validity remains unproven. Both merge
preflight and `tao doctor` prove read-only Git runs inside the provider sandbox
and fail closed naming the sandbox when it does not. Linux confinement requires
external `bwrap` at `/usr/bin/bwrap` or `/bin/bwrap`.
Reconcile findings or drift on the plan branch and refresh its source review;
do not use `tao merge --all` to repair a failed single-plan transaction.

`--no-squash` remains different: rebase or fast-forward conflicts abort, print
the conflicted files, and require manual resolution on the plan branch followed
by a refreshed review when content changes. It never invokes this squash
resolver/reviewer lifecycle.

**Verification and cleanup:** Tao prefers declared `make verify`, then declared
Make `build`/`test`, then `go build ./... && go test ./...` for Go modules. It invokes
Make only for recognized targets and skips verification if no supported gate exists.
Override detection with `--verify-command CMD` or `TAO_MERGE_VERIFY_COMMAND`;
unset uses detection, empty disables verification. Failure resets default to its
pre-merge SHA before cleanup; success records the merged SHA, marks `completed`,
and delegates worktree/branch removal to managed cleanup. Recorded Tao squash
evidence permits cleanup of the intentionally non-ancestral source branch.

Make `verify` comprehensive: applicable build, test, lint, static analysis, and
dependency-policy checks. Use narrower feedback during implementation, including
touched-package lint/static analysis for source/test slices alongside build/tests,
so the final gate is not their first check. Other build systems need an explicit
merge verification override; Tao does not infer their or package-manager commands.

After an external PR/manual merge, optionally run `tao merge <plan>` to record
integration, not to complete an already-completed PR workflow. If the plan branch,
review head, PR head, or workspace head is ancestral to default, Tao skips integration,
records `plan_merged`, retains or marks `completed`, and attempts safe cleanup.
Snapshots count only while matching the live plan tip; follow-up commits require
the normal full-branch merge. Default-branch plans are never auto-detected this way:
ancestry cannot distinguish their changes from unrelated work. For those plans or
squashes/cherry-picks whose ancestry cannot prove integration, manually verify the
intended changes landed before using `tao merge --record-only --force <plan>`.
Cleanup retries for such non-ancestral branches also require `--force`.
Rerunning an already-recorded merge retries cleanup; nothing left to clean is success.

**Restarting a stale single-plan intent:** when `tao show` or a merge refusal
reports that an unresolved intent is stale because default advanced cleanly, run
`tao merge --restart <plan>`. Single-plan restart compare-and-set clears only
that exact safe pre-mutation intent, leaves all Git work untouched, and stops.
Manually rebase the plan branch, then run `tao review --run <plan>` before a new
merge. This is distinct from `tao merge --all --restart`: batch restart removes
only batch-owned pre-landing recovery state, branch, and worktree so the batch
can start again; it does not clear any source plan's single-plan intent.

**Flags and limits:** `--no-verify` skips even explicit flag/environment command
gates, never structural checks or exact-integration review. `--force` bypasses
approval, review-base/head, and dirty-worktree pre-merge gates and reaches managed
cleanup; it cannot bypass resolution safety or authorize a non-approved integration.
A dirty-worktree refusal reports "untracked files only" when every dirty entry is
untracked and suggests deleting or ignoring those paths.
See `tao merge --help` for the full flag reference.

### `tao merge --all` — atomically integrate the approved set

**When to use:** when every reviewed and approved plan in the current repository
should land as one all-or-nothing change. Tao strictly preflights the complete
eligible set; an unhealthy, stale, dirty, or otherwise invalid candidate blocks
the batch rather than being silently skipped.

**Preview and ordering:** `tao merge --all --dry-run` reports the immutable
candidate snapshot, blockers, inferred low-overlap order, and likely deferrals.
It retains no batch state or integration changes. The order is deterministic,
respects source ancestry, and prefers lower path overlap; it is a conflict
reduction heuristic, not a dependency declaration.

**Staging and agent resolution:** a real run keeps default at its starting SHA
while it creates exactly one squash commit per source plan, normally from each
exact approved-review proposal, with Tao-owned `Tao-Plan` and
`Tao-Source-Head` trailers in a batch-owned integration worktree. Clean candidate
staging runs no verification. A textual conflict is deferred to a bounded
configured agent; that same resolver returns the structured message proposal
for its edits. Agent-resolved candidates keep their own verification gate.
Agents may edit only that integration worktree; Tao validates before intent and
alone stages and commits. Failed, malformed, empty, unsafe, ref-changing,
repeated, or attempt-capped resolution stops with source branches and durable
recovery evidence intact and no fallback message.

**Aggregate gate and atomic landing:** after every candidate is staged, Tao runs
the full merge verification command once on the staged aggregate, then reviews
the combined diff. Only the aggregate is gated for clean candidates: a candidate
that breaks the gate alone but is repaired by a later candidate can land.

If the gate fails on a freshly staged set, Tao bisects the integration prefixes
with additional gate runs and reports the attributed plan and the passing and
failing prefix commits. If the default starting commit already fails, Tao blocks
without blaming a candidate or invoking rework; fix the default branch and rerun
`tao merge --all --restart`. An attributed failure enters bounded aggregate
rework, sharing the existing aggregate rework attempt cap with review-driven
rework. Exhausting that cap leaves a resumable block naming the attributed plan;
a rerun rechecks the aggregate without repeating bisection or granting a fresh
rework budget.

An aggregate `changes_requested` verdict can also invoke bounded agent rework.
Every rework round produces a Tao-owned integration-resolution commit, followed
by full verification before a fresh aggregate review.
`TAO_AGGREGATE_REVIEW_CONVERGENCE_WINDOW` controls how
many consecutive changes-requested rounds the batch convergence check considers;
it defaults to `2` and must be an integer of at least `2`. Separately from
automatic rework's high-confidence finding equality, batch merge uses a
location-oriented safeguard: if different
findings keep recurring in the same files, Tao detects non-convergence early
and, when one candidate uniquely owns those files, prints an attributed block
naming the files and plan. The default is
stop-and-offer when no plan was previously ejected and removal leaves at least
one candidate: default does not move, and rerunning `tao merge --all` accepts
the offer by ejecting that candidate, rebuilding the remaining integration, and
running fresh full verification and aggregate review before landing the reduced
set. Use `tao merge --all --auto-eject` to perform that eject-and-reland in the
same run. Non-attributable non-convergence, a one-candidate batch, or another
non-convergence after a completed ejection remains blocked for manual review.

Only `approve` for the exact default base and integration head permits one
guarded fast-forward of default. Tao then records every landed source plan's
merge event before managed cleanup; an ejected plan remains deferred with the
attributed reason, and the final output names that plan and reason after a
successful same-run or rerun-triggered ejection. Verification, review, drift,
or cleanup failure cannot land an unverified subset into default.

**Resume, restart, and recovery:** rerun `tao merge --all` to resume matching
durable progress. When an active durable batch exists, even `--dry-run` first
inspects and resume-validates that batch; it can snapshot fresh candidates only
after no active batch remains. A rerun of an eligible attributed non-convergence
block is the explicit operator action to eject that plan and re-land the rest;
Tao prints manual-only guidance when no non-empty reduced set is available or an
earlier ejection already completed. If an interruption happened after the
guarded fast-forward, the durable landing intent proves the exact head and
settlement resumes without a second merge; evidence recording and cleanup are
idempotent. Use `tao merge --all --restart` only to discard stale, batch-owned
pre-landing state, branch, and worktree. When Tao reports that restart is safe,
preview that recovery and a fresh candidate snapshot with:

```sh
tao merge --all --restart --dry-run
```

Restart is refused after landing and never removes source plans. Resolve
reported source/default drift rather than deleting recovery files by hand.

#### Live progress and batch logs

Batch runs print timestamped transition lines with the batch ID, sequence,
status change, and candidate/deferred/ejected counts. Verification prints a
start line and a finish line with elapsed time and pass/fail status, using UTC
RFC3339 timestamps. Verification command output remains buffered and
tail-bounded, not live-streamed. Skip and recording messages also appear;
the structured batch summary remains the final output. Progress is display-only
and does not change batch state or authorize recovery.

Batch agent output is also appended to
`<data-home>/repos/<repo-id>/merge-batches/<batch-id>/agent-transcript.log`.
This best-effort transcript is created when agent output is written; batches
with no agent output need not have one. `--restart` retains the old batch's
transcript.

From another terminal, use `tao log --batch [--follow] [batch-id]` to show
recorded transitions or follow newly appended ones. Without a batch ID it
selects the current repository's active batch; provide an ID to inspect a
previous batch. This reads `transitions.jsonl`, not `agent-transcript.log`,
and does not replay verification output.

```sh
tao log --batch
tao log --batch --follow
tao log --batch <batch-id>
```

#### Batch flags

**Strict batch flags:** `--dry-run` and `--auto-eject` require `--all`.
`--dry-run` runs no verification command and is observational, but does not
bypass an active batch: Tao inspects
and resume-validates durable progress before producing a fresh candidate
snapshot. `--restart` works in both forms and means something different in each:
with a plan argument it clears that plan's stale pre-landing merge intent, and
with `--all` it discards batch-owned pre-landing recovery state. Combining
`--all --restart --dry-run` is the safe preview for eligible pre-landing batch
recovery. Batch mode allows one `--verify-command CMD` override and the separate
`--auto-eject` convergence opt-in, but still rejects `--force`, `--record-only`,
`--no-squash`, and `--no-verify`. Those bypass semantics remain available only
to the explicit single-plan workflow.

---

## Sharing plan reports

Use `tao report` when coworkers with repository access need a readable snapshot
without access to Tao's private plan directory. Reports are internal,
access-controlled sharing drafts, not public exports. Choose the normal report
for implementation and outcome context, or `--planning-only` when the audience
should see planning context without execution-derived data:

```sh
tao report --output plan-report.md <plan-id>
tao report --planning-only --output prompts/my-plan.md <plan-id>
```

`tao report` renders rulings per slice in full mode only, not in planning-only output.

Planning-only output is synthesized rather than copied from raw artifacts. It
omits prompt capture and execution, verification, review, telemetry, and outcome
data; legacy aggregate planning effort may remain when already recorded.

Both modes render a sanitized allowlist rather than copying raw plan artifacts.
Ordinary URLs and filesystem paths remain useful context for repository-authorized
coworkers; credentials, credential-bearing URLs, and common personal identifiers
are redacted. Even so, treat the file as a sharing draft and review it for the
appropriate internal audience and context before sending it. Use `--output -`
for a pure Markdown stdout stream; an existing file requires the explicit
`--force` flag. See the [plan report format](plan-report.md) for the detailed v1
layout, missing-value semantics, and safety contract.

Writing under the repository, including `prompts/`, dirties that checkout. Tao
creates only the requested report file and never stages or commits it. If a
current-checkout run or merge requires a clean tree, either make a separate
manual commit for the report first or generate it after integration; otherwise
write outside the checkout or use stdout. Report generation does not alter plan
lifecycle metadata or other Git state.

---

## Putting it together

A typical feature, end to end:

```text
/tao-plan add X, constraints Y and Z, don't touch W      # interview → Planning Packet
/tao-grill-me the storage-format decision                # only if one call is hard
/tao-slice                                               # writes the plan directory
```
```sh
tao validate <plan-id>       # check generated verification commands
tao run <plan-id>            # all pending slices, verification, and review
# or: tao run --max-slices 1 <plan-id>  # one checkpoint at a time
```

At a stop, follow the condition Tao recorded rather than running every recovery
flag in sequence:

```sh
tao approve <plan-id>                  # approval gate, then run normally
tao run --continue <plan-id>           # ordinary blocker whose cause is cleared
tao run --restart <plan-id>            # safe blocked slice on a newer baseline
tao run --repair-verification <plan-id>
tao run --reverify <plan-id>
```

A `changes_requested` review normally triggers bounded automatic rework. If you
chose manual control, inspect the review and reopen explicitly:

```sh
tao review <plan-id>
# The same tao rework command applies to a comment review with findings.
tao rework <plan-id>        # inspect generated slices before running
# or: tao rework --run <plan-id>
```

After an approved exact-head review, choose one completion branch:

```sh
# No-PR branch: Tao integrates and records merge evidence.
tao merge <plan-id>

# PR branch: Tao creates/updates the recorded PR handoff instead.
tao run --pull-request <plan-id>
```

For exploration rather than a specific change, start with
`/tao-improve-codebase-architecture` or `/tao-repo-health`, pick one finding, and feed it
into the loop above.

---

## Operational safeguards

Use this section for choices that affect ownership or safety. The
[README command index](../README.md#command-reference) and
`tao <command> --help` are the exact command reference; the
[plan-format contract](plan-format.md) owns artifact, lifecycle, and
backward-compatibility details.

### Commits, branches, and pull requests

Keep the default `slice` commit policy when Tao should own clean, recoverable
checkpoint commits. Choose `none` only when you intentionally accept manual Git
ownership and potentially uncommitted completion. `expected_files` is advisory,
not an allowlist; Tao still stops on unsafe or ambiguous Git state. Historical
`plan` policy metadata remains readable, but is not selectable for new runs.

Keep the default `isolated` execution mode when you want Tao to own a dedicated
worktree and avoid direct work on the default branch. Choose `current` only when
you deliberately want in-place work and accept responsibility for that
checkout. Recorded workspace ownership, not a branch naming pattern, determines
whether Tao may resume or clean work; do not reuse, rename, or delete a branch to
work around an ownership refusal.

When requested placement differs from recorded placement, `tao run` prints at
most one informational notice per invocation, including in non-interactive output.
It names both modes, not an execution outcome: recorded placement and all safety
checks still apply, and the run may be refused. The notice neither changes the
request nor grants permission to resume, restart, reverify, or finalize a PR.
Legacy plans remain readable, but ordinary writes use the current vocabulary;
downgrading to an older Tao binary afterward is not guaranteed to work. See the
[artifact contract](plan-format.md) for persistence compatibility.

Use `tao run --pull-request` only for an isolated, automatically committed run.
Tao requires an approved review for the exact head before push or forge mutation.
If interrupted, rerun the same command and follow `tao show <plan>` rather than
repairing PR metadata by hand. Matching approved-review and PR metadata can make
the local Tao workflow `completed`, but does not prove remote review, CI, or
merge; only recorded merge evidence proves default-branch integration. Exact PR
body, title, labeling, assignment, and option behavior belongs in
`tao run --help` and the README command reference.

When unfinished work is intentionally no longer needed, use
`tao abandon --reason "superseded by a different approach" <plan>` to record a
terminal outcome and its reason. Abandonment preserves plan and workspace
evidence and never cleans branches or worktrees. Tao refuses it during an
unsettled lifecycle transaction that still needs recovery.

### Workspaces

Linked Git worktrees share the main checkout's registered repository identity,
plans, notes, and repository defaults. Commands such as `tao list` and
`tao show` find that shared data from a linked checkout or its subdirectories;
you do not need a separate registration or `--plans-dir` override.

Shared identity does not redirect execution to the main checkout. For a new
`current`-mode workspace, Tao uses the launch checkout and records it as the
execution root; subsequent runs still honor the plan's recorded root and health
checks. Standalone `tao commit` (including `--context` and `--push`) intentionally
refuses an active Tao-managed worktree. Follow its recovery guidance instead of
trying to bypass ownership with another launch directory.

Isolated runs may update a stale clean workspace from the current local default
branch, prepare dependencies when a supported lockfile is present, and then
check the selected slice's required inputs inside that workspace. Tao does not
fetch or pull first; dirt, conflicts, or missing inputs stop before the agent.
Resolve the reported condition rather than editing workspace metadata or relying
on an earlier slice's promise that a file should exist.

Managed plan workspaces automatically initialize pinned recursive Git submodules
before JS dependency installation, independently of its cache. Batch integration
workspaces (`tao merge --all`) initialize submodules before dependent agents and
gates, including changed pins, resumed work, and verification probes; they do not
install JS dependencies. Git transport and authentication policy still apply.
Credential, transport, and unresolved submodule prerequisites stop the batch
without being attributed as code defects or spending a repair attempt. Resolve
the reported prerequisite and rerun; preserve local submodule edits before
retrying a refused update or cleanup. This adds neither configurable prepare
commands nor preparation of the control checkout for ordinary single-plan merges.

Cleanup is explicit and preview-first. Use `tao workspace clean <plan>` for one
workspace and `tao cleanup --dry-run` after integration for repository-wide
managed cleanup, including unreferenced integration namespaces (`tao/integration`
branches and `.tao/integrations` worktrees), never the active merge batch.
From linked worktrees, cleanup uses Git's shared common directory to locate the
control repository's active batch. If that identity cannot be established,
integration cleanup is skipped, even with `--force`; ordinary plan cleanup
continues. Integration cleanup holds the control repository's batch ownership
lock from active-state lookup through removal. If a merge batch (including a
live `--dry-run`) holds ownership, integration cleanup is skipped even with
`--force`, while ordinary plan cleanup continues.
Unregistered integration directories are reported but never removed, even with
`--force`. PR completion alone is not deletion authority: protected,
dirty, current, and unmerged state remains safeguarded, while recorded squash
merge evidence handles the intentional non-ancestry of a squash source branch.
Plan artifacts are never removed by workspace cleanup. See
`tao workspace --help` and `tao cleanup --help` for exact subcommands and force
options.

### Validation warnings

Keep blocking input facts separate from advisory command analysis. A selected
slice's declared required inputs must exist in its prepared workspace, and
malformed slice structure can block. Tao does not execute verification commands
during readiness or claim to understand arbitrary tool semantics, so command and
agent-budget findings remain review signals. `tao validate` checks the whole
plan; `tao run` preflights only the selected runnable slice. See the
[plan-format contract](plan-format.md#validation) for exact validation rules.

An invalid budget *configuration* is different from exceeding a valid advisory
threshold: commands that consume it reject the override by name instead of
silently retaining defaults or disabling a hard cap. Use `tao status` to inspect
it, then correct or unset it at its source: environment, `tao config`, or the
Settings repository editor. A valid environment override does not repair invalid
saved configuration. Help, status, and the Settings/Debug tabs remain available
for diagnosis; see the [configuration contract](configuration.md).

### Data and privacy

Tao is local-first. Treat its data home and workspace-local `.tao/` metadata as
private, local-only state and never commit them. Notes belong only to their
registered repository; legacy global note files are ignored. Missing telemetry
never blocks a run, and Tao does not currently write agent transcript sidecars.

The [plan-format contract](plan-format.md#plan-directory) is the authority for
exact plan files, local-only runtime artifacts, events, and legacy readability.
Use `tao status` for resolved runtime settings and the
[configuration reference](configuration.md) for supported
configuration; provider tuning never replaces Tao's durable plan and Git
recovery checks.
