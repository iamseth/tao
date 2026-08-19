# Tao Usage Guide

Workflow judgment for using Tao day to day: when to reach for each command, how
to get the most out of it, and the gotchas the reference docs don't cover.

This guide is a companion to the [README](../README.md) (install, commands, and
the artifact contract in [plan-format.md](plan-format.md)). The README tells you
*what* each command is; this guide tells you *when* and *how* to use them well.

## The core loop

```
/tao-plan  →  /tao-slice  →  tao validate  →  tao run  →  tao review  →  tao merge (or PR)
                                      ↳ clear blocker, then tao run --continue
                                                             ↳ automatic rework/run/review when changes requested
```

`/tao-plan` and `/tao-slice` are **prompts** you run inside Pi or Claude Code. `tao
validate`, `tao run`, and the rest are **CLI commands** you run from the
repository that owns the plan. The handoff between them is the plan directory in
Tao's data home — everything stays local and inspectable on disk.

A useful mental split:

- **Planning prompts** (`/tao-plan`, `/tao-grill-me`, `/tao-improve-codebase-architecture`,
  `/tao-improve-documentation`, `/tao-repo-health`, `/tao-insights-review`) are
  **read-only**. They never edit code or write Tao artifacts.
- **Build prompts** (`/tao-note`, `/tao-slice`, `/tao-run`, `/tao-commit`, `/tao-pr`) write
  artifacts, code, or git state.

After an approved review, choose one of two completion paths. The solo no-PR
path uses `tao merge` to integrate and record `plan_merged`. An opt-in PR run
reaches `completed` when Tao has a current approved review and durable PR
metadata for the same non-empty head. That is a completed local handoff, not
proof that the hosting provider merged anything; only current `plan_merged`
evidence proves default-branch integration.

## Capture first with repository notes

`/tao-note` is the capture end of the note-to-plan pipeline for agent sessions: run the slash command inside a session to distill the conversation into a self-contained repository note, then plan the open note later with `/tao-plan note:<id>`. Use `tao note create` for manual capture instead; `/tao-note` names the installed slash command, while `tao note` names the CLI command group.

Use `tao note` (or `tao n`) when an idea is worth retaining but not worth interrupting your current work. From a registered checkout, the shortest capture path is:

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

## Monitoring plans

Use `tao show <plan>` when returning to one plan. Its single `Next:` line is the
safest read-only recommendation Tao can derive from durable lifecycle evidence;
the reason explains why it takes precedence. Any indented alternatives are
subordinate options, and administrative alternatives may bypass safeguards, so
they are not equivalent recommendations. A terminal `No action` distinguishes a
finished or otherwise non-actionable plan from one that should progress.

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

## Interactive dashboard: `tao ui`

Use `tao ui` for a keyboard-driven version of the cross-repository monitor when
you want to observe plans and launch routine follow-up without leaving the
terminal. It requires a terminal; use `tao monitor --once` for redirected or
pasteable output. The dashboard groups nonempty sections as follows:

- **NEEDS ATTENTION** contains actionable conditions such as blocked or
  changes-requested plans, approval gates, pending slice completion, stopped
  rework, and runs whose lock evidence suggests the process exited.
- **RUNNING** contains plans with live run records and stale records shown as
  `stalled? (<age> old)` when there is no dead-process lock evidence.
- **PLANNED / IN REVIEW** contains the remaining active plans, including plans
  waiting for execution or review rather than a live process.
- **COMPLETED** contains the remaining plans completed within the configured
  lookback window. It is hidden initially and can be shown or hidden with `c`.

The question marks on `stalled?` and `crashed?` are deliberate. `stalled?` means
the last run heartbeat became stale and Tao found no dead-process lock evidence.
`crashed?` means Tao found run-lock evidence whose recorded process is no longer
alive, whether the runtime record is stale or absent. Either observation may be
incomplete or delayed; heartbeats and locks are operational liveness hints, not
lifecycle evidence, review results, or verdicts about semantic progress.

Keys on the table page are:

- `j`/`k` or the arrow keys move the selection.
- `r` starts the selected plan in a detached `tao run` process. After its
  blocker is cleared, a blocked plan is resumed with `--continue`; a plan with a
  fresh live heartbeat is left alone.
- `a` shows the selected approval-gated slice and reason, then requires `y` or
  `n` before starting detached approval. `Esc` or `q` declines the prompt.
- `m` asks for confirmation on a selected `reviewed` plan, then starts detached
  `tao merge <plan-id>`. Other rows are left alone; the merge command remains
  responsible for approval, branch, worktree, and lifecycle eligibility.
- `M` names and confirms the selected repository (or the active focused
  repository), then starts detached `tao merge --all` with that repository root
  as its working directory. It never batches plans across repositories.
- `f` focuses the dashboard on the selected plan's repository; press `f` again
  while focused to restore the all-repository view. Repository warnings and the
  completed-row setting remain scoped consistently as snapshots refresh.
- `c` toggles the completed rows already included by `--completed-window`.
- `Enter` opens the selected plan's queue-ordered slice list and live
  `agent-run.log` tail. On that page, `j`/`k` or the arrow keys select a slice
  and `Enter` opens its full read-only details; empty fields are omitted.
  `Esc` returns one level at a time, and log following continues until you
  leave plan detail for the table.
- `q` or `Ctrl-C` quits from any page. On the table, two `Esc` presses within one
  second also quit; a lone or stale `Esc` does not.

Run, approval, and merge subprocesses are launched in new sessions with their
streams detached, so they survive TUI exit. A `merging…` row label is only
best-effort launch feedback, not durable merge evidence or a success claim.
`Esc` or `q` declines an active merge confirmation before navigation or quit
handling.

`--interval DURATION` sets the refresh period (default `2s`) and must be greater
than zero. `--completed-window DURATION` sets the completed-plan lookback
(default `168h`); `0` omits completed plans entirely, so `c` cannot reveal them.
For example:

```sh
tao ui
tao ui --interval 5s
tao ui --completed-window 24h
tao ui --completed-window 0
```

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
plans active in the last 30 days. Missing roots, damaged records, unreadable
logs, stale logs, and evidence concentrated in one repository are reported as
limits rather than silently generalized.

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
  ready-to-use topic into `/plan`. If it is worth retaining but not planning
  now, capture its concise note topic with `tao note create ...` (or `tao n c
  ...`).

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

Day to day you run **`tao run <plan-id>`** from the CLI. Under the hood the
`/tao-run` prompt puts the agent in **WORK mode** to implement exactly **one** pending
slice at a time: select the next slice, honor `depends_on` and approval gates,
implement only that slice, run its verification commands, and complete it via
`tao slice-complete` (which Tao uses to update state, pending-slice ordering,
and events). It stops on blockers and failed verification rather than pushing
through.

**When to use which form:**

- `tao run <plan-id>` — normal execution of all pending slices.
- `tao run --max-slices 1 <plan-id>` — run a single slice to inspect results.
- `tao run --continue <plan-id>` — resume only after you've cleared a blocker.
  It explicitly clears the blocked lifecycle state but does **not** infer
  resolution or bypass approval gates, dependencies, or verification preflight.

Run each plan explicitly. If two plans are independent, you can launch one
`tao run <plan-id>` in each of two terminals. A cross-process per-plan lock
prevents duplicate drivers for the same plan; it does not make overlapping
changes across different plans conflict-safe.

In an interactive terminal, `tao run` pins a compact live header above the
agent log. It combines repository, plan, and run configuration with the active
slice or phase and elapsed time, a capped progress bar, a titled window of
nearby slices centered on the current one, and compact session/token/cost
metrics. A divider and `LIVE OUTPUT` label separate the header from provider
output. It is TTY-only and requires enough terminal rows; redirected and other
non-interactive output remains plain. Disable it for one invocation with
`--no-run-header`, or set `TAO_RUN_HEADER=0` to opt out by default.

The pinned region still uses terminal scroll margins rather than an alternate
screen. Lines that scroll out of that region are therefore dropped from
terminal scrollback. The complete agent log is still retained as
`agent-run.log` in the plan directory.

**Before running, prefer `tao validate <plan-id>`** for whole-plan findings —
`tao run` only preflights the one slice it's about to execute.

When all slices settle, treat execution "done" as slices complete, a persisted
repository-wide verification result, and a post-completion review result. With
the default slice policy, the implementing agent proposes each checkpoint
message before completion. Tao validates the scoped Conventional Commit subject
and `What:`/`Why:` body, appends trusted plan/slice trailers, persists the exact
final message, and alone stages and commits. A malformed proposal stops before
intent or Git mutation and may be repaired only in that same active session;
there is no title fallback or separate normal message session. The resulting
checkpoint commits let review inspect the exact `base..HEAD` diff. Broad
verification is blocking and uses the repository's declared `make verify` when
available before narrower Make/Go fallbacks. The review is best-effort: a failed
or timed-out review session is recorded for you to see, but it does not turn
verified work into a failed run. Without a qualifying PR, an approved result is
`reviewed` and ready for `tao merge`; when the same non-empty head also has
recorded PR metadata, the plan is `completed` as a PR workflow without claiming
that the host integrated it.

When a successful review requests changes, `tao run <plan-id>` automatically
uses the ordinary rework gates, runs the generated fix slices, and reviews again.
It stops with an error after five rework cycles, when consecutive reviews repeat
an equivalent finding set, or
before another reopen when the same normalized primary finding file appears in
three consecutive `changes_requested` reviews. The fixed three-review boundary
applies even when each review reports a different issue in that file. Tao leaves
the latest review intact. Change the cap with `--max-rework-attempts N`; disable
the loop with `--auto-rework=false` or `TAO_AUTO_REWORK=false`. Disabling review
with `--no-review` or `TAO_REVIEW=false` also disables automatic rework.

After any stop, a later `tao run` refuses to silently grant the plan a fresh
automatic-rework budget. It reports the persisted stop reason and repeats the
loud finding-bearing warning for equivalent-findings and recurring-file stalls.
Inspect and address the review first. If you deliberately want another bounded
budget, pass `--rework-restart`; this preserves historical slices but establishes
the current round as a fresh baseline, so earlier reviews do not count toward
the new three-review window. Restart is an acknowledgment, not a bypass of
ordinary rework gates. The refusal never prompts. To intentionally continue,
rerun that plan directly with `--rework-restart`; Tao then opens the first round
of a fresh bounded budget.

The installed `/tao-review` slash command is an agent prompt, while `tao review`
is the ordinary CLI command that runs or displays Tao's persisted plan review.
When you return to a slice-complete or reviewed plan, start with
`tao review <plan-id>`. It reads the persisted review from the data-home plan
directory, so you can triage the verdict, summary, findings, and approved commit
proposal before opening a PR or merging. The reviewer already inspecting the
exact base/head diff supplies that proposal; Tao validates and binds it to the
review instead of opening a merge-time message session. An approval with a
missing, malformed, oversized, or reserved-trailer proposal is safely downgraded
to a non-approving `comment`, so it cannot authorize merge. If you make follow-up
commits, amend the branch, or otherwise change the diff after the recorded
review, run `tao review --run <plan-id>` to refresh both review and proposal
against current `HEAD`. Use `tao staleness <plan-id>` for the separate base-commit
drift check on pending work.

Run-path agent sessions have a wall-clock hang ceiling so unattended batches do
not stall forever on one stuck agent process. The default is 20 minutes; set
`TAO_SESSION_TIMEOUT` to another Go duration such as `45m`, or to `0` to disable
the ceiling. Interactive planning sessions (`/tao-plan` and `/tao-slice`) are not
subject to this timeout.

#### Recover an interrupted slice

During one implementation-slice invocation, Tao automatically handles at most
two explicitly structured transport failures. For Pi, only the
`provider_transport_failure` diagnostic qualifies. Tao waits 1 second before
the first retry and 2 seconds before the second, with both waits cancellable,
then reloads artifacts, reruns selected-slice preflight, and asks the existing
safe execution-boundary classifier whether another handoff is allowed. Each
allowed retry uses a fresh `pi --mode rpc` process and the ordinary numbered
resume prompt; it does not continue a provider session. The budget is local to
the invocation, so an explicit later run starts with two retries even though
resume-attempt event numbering continues as durable audit history.

This is fixed policy, not configuration. Tao adds no retry flag or environment
setting. It does not retry planning, review, pull-request, or merge agents;
session timeouts; authentication failures; generic provider errors; or errors
that merely contain WebSocket text. Manual/current-checkout, policy-`none`,
post-commit-intent, and otherwise unsafe states are also never automatic retry
boundaries. Provider output, metrics, and events do not authorize recovery.
Because each fresh session has its own `TAO_SESSION_TIMEOUT`, a handoff that
reaches the three-session maximum can take roughly three times that timeout plus
3 seconds of retry waits (about 60 minutes 3 seconds with the default), excluding
other orchestration work.

If the original agent durably completes the slice before its transport error
reaches Tao, Tao reloads and accepts that result only after ordinary progress
and completion-boundary validation; it does not launch or charge another retry
handoff.

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
  transaction can recover or settle the exact commit.
- **Changed or unsafe boundary:** a different root, branch, or HEAD, an active
  Git operation, conflicts, or ambiguous status is a refusal. Inspect the named
  paths and restore the recorded boundary before rerunning Tao.
- **Dirt without an immutable start boundary:** treat it as unrelated until
  proven otherwise. Tao will not turn it into a new clean-start baseline or
  attribute it to the interrupted slice.

`tao run --continue` has a different purpose: it explicitly clears lifecycle
blocker state after you resolve a recorded blocker. Tao does not infer that
resolution from Git state, blocker prose, or external conditions, and continue
does not override any interrupted-slice boundary check.

### `tao rework` — turn review findings into follow-up slices

**When to use:** after `tao review <plan-id>` shows a reviewed plan with a
`changes_requested` verdict and actionable findings, or after a human leaves
review threads on a completed Tao-created pull request. `tao rework <plan-id>`
is the standard way to continue the review → rework → run → re-review → merge
loop without creating a second plan.

Without `--from-pr`, `tao rework` is gated and non-mutating on refusal. It
refuses unless the plan is reviewed, the persisted review requested changes,
and Tao can find actionable findings; approved reviews, reviews with no
findings, and unfinished plans are left untouched. Use `--force` only when you
intentionally want to bypass those ordinary review gates.

**What it does:** Tao deterministically maps each structured finding to one new
pending rework slice, preserving the finding's goal, files, and tasks when
available. Each generated slice carries a deterministic verification command
scoped to the touched package rather than a narrow test-name filter. Tao appends
those slices, flips the same plan back to runnable, records the reopen event, and
keeps completed slices and history intact.

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

### `/tao-commit` — local conventional commit

Creates one local commit through Tao's standalone boundary. Tao first returns
only filtered allowed context and a fingerprint. The active agent/model proposes
`<type>(<lowercase-scope>): <lowercase-imperative-summary>` with non-empty
`What:` and `Why:` sections; Tao then rechecks the live repository, validates the
proposal, excludes `.tao/`, suspected secrets, and generated output, stages safe
paths, appends any trusted evidence, and creates the commit. It never pushes.

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

### `/tao-pr` — open a pull request

Inspects the branch, status, recent commits, and diff against the base branch
(defaults to `main`), pushes if needed, and opens a PR with a structured
description (summary, motivation, scope, testing, risks, rollback). Returns the
PR URL.

**When to use:** after a run's work is committed and you want a PR by hand.
Equivalent automated path: `tao run --pull-request`, which is gated — it's
rejected with `--commit-policy none`, or when the run is not in
`--execution-mode isolated`. The automated path requires the current approved
exact-head review proposal, uses its Conventional Commit subject verbatim as the
title, and records the exact branch head. Typed plans also receive their category
label and every new PR is assigned to the authenticated GitHub user. When the
recorded head matches the approved review head, Tao's lifecycle is complete even
though the hosting provider still owns integration.

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

**What it does:** by default Tao checks out the default branch, squash-applies
the reviewed plan branch, and reuses the approved review's proposal for the one
commit, adding trusted `Tao-Plan` and `Tao-Source-Head` trailers itself. The
reviewer already saw the exact base/head diff, so the normal path opens no second
message session. Historical approved reviews without a proposal remain readable;
a squash merge generates one proposal on demand from the exact diff before any
mutation. `--force` also permits this exceptional path when current approved
proposal evidence is unavailable or invalid. Generation or validation failure
stops without intent, staging, commit, or title fallback. The plan branch keeps
its per-slice checkpoint history for review and recovery until managed cleanup
succeeds. Use `--no-squash` to preserve those commits by rebasing the plan branch
and fast-forwarding default.

**Single-plan failure behavior:** squash, squash-commit, rebase, or fast-forward
failures abort the attempt, restore the default tip and worktree when possible,
and print conflicted files. Single-plan mode does not ask an agent to resolve
conflicts; resolve them manually on the plan branch, refresh the review if the
diff changes, and rerun `tao merge`.

**Verification and cleanup:** after integration, Tao prefers a repository's
declared `make verify` target. Without one, it uses declared Make `build` and/or
`test` targets, or native `go build ./... && go test ./...` for a Go module.
Make is not required: Tao invokes it only when the repository declares a
recognized target, and skips automatic verification when no supported gate is
detected. `--verify-command CMD` overrides detection for one merge;
`TAO_MERGE_VERIFY_COMMAND` provides the environment override. If verification
fails, Tao resets default to the pre-merge SHA before cleanup. If it passes, Tao
records the merged default SHA, marks the plan `completed`, and delegates
worktree/branch removal to managed cleanup. For Tao-created squashes, that
recorded evidence lets cleanup safely remove the now non-ancestral source branch.

Repository owners who use this convention should make `verify` the comprehensive
gate for an integrated change, composing the project's relevant build, test,
lint, static-analysis, and dependency-policy checks. Keep narrower commands for
ordinary implementation feedback. Repositories using another build system can
keep their native workflow and set an explicit merge verification override;
Tao does not infer package-manager or other build-system commands.

If a PR or manual `git merge` already integrated the plan and you explicitly
want Tao to persist actual integration evidence, you may run `tao merge <plan>`.
A qualifying PR plan is already lifecycle-complete, so this is optional evidence
recording rather than a completion workaround. Tao checks the plan branch,
review head, PR head SHA, and workspace head SHA against the default branch.
When any is already an ancestor of default, Tao skips rebase/fast-forward,
records `plan_merged`, retains or marks the plan `completed`, and attempts safe
cleanup. A plan whose branch is the default branch itself
(execution-mode current) is never auto-detected this way — ancestry against
default cannot distinguish the plan's work from unrelated commits — so record
such plans explicitly with `--record-only --force` after verifying the changes
landed. A recorded head snapshot only counts while it still
matches the live plan branch tip: if you added follow-up commits after the
external merge, the snapshot is stale and Tao merges the full branch through
the normal path instead. Rerunning `tao merge` on an already-recorded plan
retries any cleanup that previously failed; a branch with nothing left to clean
counts as success. For squash merges, cherry-picks, or other integrations where
ancestry cannot prove the merge, use `tao merge --record-only --force <plan>`
only after you have manually verified the default branch contains the intended
changes — and because ancestry cannot prove those merges, a later cleanup retry
for such a branch also needs `--force` to remove it.

**Flags and limits:** `--record-only` records an already external merge without
integrating. `--no-squash` preserves checkpoint commits with rebase plus
fast-forward. `--no-verify` skips the post-merge verify gate, including explicit
flag or environment overrides. `--force` bypasses approval, review-base,
review-head, and dirty-worktree pre-merge gates and is passed to managed cleanup;
it does not make conflicts safe or replace review.

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
`Tao-Source-Head` trailers in a batch-owned integration worktree. A textual
conflict or candidate verification failure is deferred to a bounded configured
agent; that same resolver returns the structured message proposal for its edits.
Agents may edit only that integration worktree; Tao validates before intent and
alone stages and commits. Failed, malformed, empty, unsafe, ref-changing,
repeated, or attempt-capped resolution stops with source branches and durable
recovery evidence intact and no fallback message.

**Aggregate gate and atomic landing:** after every candidate is staged, Tao runs
the full merge verification command and reviews the combined diff. An aggregate
`changes_requested` verdict can invoke bounded agent rework, producing a
Tao-owned integration-resolution commit, followed by full verification and a
fresh aggregate review. Separately from automatic rework's high-confidence
finding equality, batch merge uses a location-oriented safeguard: if different
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
durable progress. A rerun of an eligible attributed non-convergence block is the
explicit operator action to eject that plan and re-land the rest; Tao prints
manual-only guidance when no non-empty reduced set is available or an earlier
ejection already completed. If an interruption
happened after the guarded fast-forward, the durable landing intent proves the
exact head and settlement resumes without
a second merge; evidence recording and cleanup are idempotent. Use
`tao merge --all --restart` only to discard stale, batch-owned pre-landing state,
branch, and worktree. Restart is refused after landing and never removes source
plans. Resolve reported source/default drift rather than deleting recovery
files by hand.

**Strict batch flags:** `--dry-run`, `--restart`, and `--auto-eject` require
`--all`. Batch mode allows one `--verify-command CMD` override and the separate
`--auto-eject` convergence opt-in, but still rejects `--force`, `--record-only`,
`--no-squash`, and `--no-verify`. Those bypass semantics remain available only
to the explicit single-plan workflow.

---

## Sharing plan reports

Use `tao report` when coworkers with repository access need a readable snapshot
without access to Tao's private plan directory. Reports are internal,
access-controlled artifacts, not public exports. A full report summarizes
planning, slice progress, execution effort, verification, and any review or
merge outcome that is available in the plan's current phase. Its top-level
sections are Planning Context, Implementation, Implementation Summary, Review
and Outcome, and Redactions and Omissions. Implementation Summary and Review
and Outcome are separate top-level sections rather than nested sections:

```sh
tao report --output plan-report.md <plan-id>
```

For a lighter planning record, explicitly choose a repository prompt path:

```sh
tao report --planning-only --output prompts/my-plan.md <plan-id>
```

Planning-only output is synthesized and non-verbatim. It contains planning
context and original planned slices, but no slice execution information: no
statuses, rework, durations, verification, commits, execution telemetry, reviews,
or outcomes. Valid aggregate planning effort can appear only for legacy plans
that already contain planning-session statistics. Tao does not restore or create
planning-session capture, so do not expect newly created plans to have planning
token metrics.

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
tao validate <plan-id>     # check generated verification commands
tao run <plan-id>          # execute pending slices
tao run --continue <plan-id>   # only after clearing the recorded blocker
tao review <plan-id>       # read the persisted post-completion review
tao rework --run <plan-id> # if review requests changes, reopen and run fixes
tao review --run <plan-id> # refresh the review after follow-up changes
tao merge <plan-id>        # no-PR path after you accept an approved review
tao run --pull-request <plan-id>   # or /tao-pr by hand
```

For exploration rather than a specific change, start with
`/tao-improve-codebase-architecture` or `/tao-repo-health`, pick one finding, and feed it
into the loop above.

---

## Behavior reference

The sections below are the detailed behavior the README summarizes: how runs
commit, where they execute, what validation can analyze, and how Tao treats your
data.

### Commits, branches, and pull requests

`tao run` defaults to `--commit-policy slice`, which requires a clean execution
worktree before each agent starts. On completion, the active implementing agent
returns a bounded structured proposal; `tao slice-complete` centrally validates
it, formats the canonical subject and `What:`/`Why:` body, adds trusted
`Tao-Plan` and `Tao-Slice` trailers, and persists that exact final message in
`commit_intent` before screening or staging safe changes. Tao alone creates the
commit and only then marks the slice complete. If interrupted after Git advances,
retrying recovers only when the parent and full message match the recorded
intent; it never asks another model or substitutes a generated title. Historical
pre-contract intents retain their exact old hash/message recovery semantics
rather than being rewritten. A slice that made no changes records `no_changes`
without creating an empty commit.

Slice `expected_files` remain advisory: Tao warns when a safe committed path was
not declared, but does not treat the list as an allowlist. Suspected secrets,
generated paths, ambiguous Git status, protected branches, changed branch/HEAD
boundaries, and leftover changes stop automatic completion. These checkpoint
commits make partial and `--max-slices` runs recoverable and provide the exact
history reviewed before integration.

Use `--commit-policy none` when you explicitly want manual ownership. Completion
then records `manual_uncommitted` and does not mutate Git; commit with `/tao-commit`
or your own workflow. In current mode this means work may remain uncommitted and
Tao does not enforce automatic-policy cleanliness. In isolated mode, prefer the
default slice policy unless you have a concrete manual history requirement.

`plan` is historical metadata only, not an executable policy. New CLI, prompt, and
environment inputs reject it with migration guidance. For a dirty
legacy plan-policy worktree, inspect the diff, commit or stash it manually, then
rerun with `slice`; use `none` only if you intentionally accept manual
uncommitted completion. Tao never guesses how to split or migrate that dirty
work.

`tao run` defaults to `--execution-mode isolated`, the single knob that drives
both workspace placement and branch behavior. For a new typed plan, `isolated`
derives `<category>/<plan-slug>` after removing the plan ID timestamp; `feat`
uses the repository-facing `feature` category and the other supported types keep
their names. An untyped legacy plan retains `tao/<plan-id>`, and a branch already
recorded in workspace metadata remains authoritative and is never renamed. Tao
refuses to create the worktree when a newly derived branch already exists without
durable ownership for that plan; it does not reuse the branch or choose another
name. Isolated execution never commits directly to `main` or `master`. Use
`--execution-mode current` to run in place on the branch where the run started.
Set `TAO_EXECUTION_MODE=isolated|current` to change the CLI default.

Use `tao run --pull-request` to create a GitHub pull request after a full run
completes and Tao has committed and reviewed the plan work. PR creation is
disabled by default and is valid only in `--execution-mode isolated` — requesting
a PR in `current` mode is a hard error, as is combining it with policy `none`.
Before pushing, Tao requires a current approved review for the exact branch head
and uses that review proposal's Conventional Commit subject verbatim as the PR
title. For typed plans, the subject type must match the persisted plan type. Tao
then pushes and opens the PR through authenticated `gh`, recording the source
branch and exact head whether the PR is created or discovered. Existing PR
discovery remains idempotent. New typed PRs receive the mapped category label
(`feat` maps to `feature`); Tao preserves an existing label's color and
description, or creates the missing label with stable defaults. Every new PR is
assigned to the authenticated user. If creation reports a failure, Tao repairs
metadata only when that create attempt emitted an exact PR identity and Tao
persisted it first. A matching PR discovered after the failure—or on a later
retry with only legacy branch/head intent—is returned without Tao adding labels
or assignment because a human may have created it concurrently.

The body uses Problem, Fix, Tests, Deploy, and Scope sections. Tests report
recorded repository command results while omitting lifecycle-only Tao commands,
Deploy defaults to no special steps, and Scope keeps the exact diff stat in a
collapsed Changed files block even when a path contains `tao`. Optional agent
polishing must preserve that structure and is rejected if its reviewer-authored
Problem, Fix, or Deploy prose introduces plan IDs, slice or lifecycle metadata,
merge guidance, or other Tao-specific text; the fallback follows the same
rules. If the non-empty PR head matches
the current review with status `completed` and verdict `approve`, the plan
becomes `completed` in Tao. This does not query or imply remote merge,
human-review, CI, open/closed, or draft state. Choose the host's **Squash and
merge** action; no forced record-only merge command is needed merely to complete
Tao's lifecycle. After the merged change reaches your local default branch,
optionally preview `tao cleanup --dry-run` and then run `tao cleanup`. Tao does
not click or emulate the host merge action.

### Workspaces

`--execution-mode isolated` (the default) runs in a dedicated git worktree, with
per-plan workspaces rooted at `.tao/workspaces/<plan-id>`, and leaves the launch
checkout untouched. Use `tao run --execution-mode current` to run a single plan
in place in the control checkout, or set `TAO_EXECUTION_MODE=current` to make CLI
runs default to that in-place behavior. Use
`tao workspace prepare`, `status`, `list`, and preview-first `clean` to inspect or
manage worktree-backed workspaces manually.

Before a worktree-backed run starts, Tao may rebase a stale clean worktree onto
the current local default branch before invoking the agent, then prepares
dependencies in the workspace root when a supported lockfile is present. After
preparation, Tao checks the selected slice's explicitly declared required inputs
in that worktree—not in the control checkout—before changing lifecycle state or
invoking the agent. A missing input or one with the wrong filesystem kind blocks
the run, including when an earlier slice promised to produce it. Slices without
declared inputs keep the legacy behavior.

Tao does not fetch or pull remotes for this pre-run rebase. Dirty worktrees and
rebase conflicts fail early, before the agent runs, so you can clean, stash, or
resolve manually and retry. `--execution-mode current` runs in place and never
performs this automatic rebase.

Dependency preparation detects `package-lock.json`/`npm-shrinkwrap.json`,
`pnpm-lock.yaml`, `yarn.lock`, and `bun.lock`/`bun.lockb`; installed dependency
directories stay local to that workspace, while normal package-manager cache
variables continue to work.

Workspace cleanup is manual by default. `tao workspace clean <plan>` previews
cleanup without removing anything and prints the workspace path, branch, cleanup
status, and exact git worktree action. Pass `--force` to remove a clean inactive
workspace. Active plans, missing or ambiguous workspaces, protected branches
(`main`/`master`), dirty workspaces, and unmerged workspace branches are refused
by default; use `--force-active` or `--force-dirty` only when intentionally
overriding those checks. Plan artifacts in Tao's data home are never removed by
workspace cleanup.

After an externally merged PR is present on your local default branch,
`tao cleanup --dry-run` optionally previews cleanup for Tao-managed workspaces
and local branches. Cleanup discovers legacy `tao/*` branches and exact branch
names durably recorded by plans or Tao workspaces in the current registered
repository. It never scans generic `feature/*`, `fix/*`, `docs/*`, or other
category prefixes, so a repository-native name alone does not prove Tao
ownership. Normal `tao cleanup` removes only candidates that live Git state
classifies as merged and clean before deleting their local branches, but does
not remove plan artifacts in Tao's data home. PR lifecycle completion by itself
is not cleanup authorization. Tao never deletes protected branches or dirty
workspaces, and without `--force` it relies on `git branch --delete` so unmerged
branches stay protected by Git. The exception is a Tao-recorded squash: verified
squash evidence permits deleting its intentionally non-ancestral source branch,
but never bypasses the final dirty-workspace check.

Even after a plan branch has been merged, make clean-worktree verification your
safe-deletion habit before removing the branch or workspace by hand. A clean
status confirms each automatic slice transaction settled without leftovers.

### Validation warnings

Keep two readiness signals separate:

- **Explicit input facts can block.** Tao checks only required files and
  directories declared by the plan. Whole-plan validation can recognize an
  exact direct producer promise; the selected run still requires the artifact
  to exist in the prepared execution worktree. Malformed slice structure, such
  as a missing or entirely blank verification-command list, also blocks.
- **Command semantics are advisory.** Tao does not execute commands during
  readiness and cannot prove arbitrary shell, build-tool, package-manager, or
  test-runner commands valid or invalid. Its conservative analyzer recognizes a
  few common shapes to give planning feedback, but every semantic finding is a
  warning rather than an execution gate.

For example, package-cwd runners should use package-relative paths:

```sh
pnpm --filter web exec vitest src/app.test.ts
```

instead of mixing a package filter with a repo-root path like:

```sh
pnpm --filter web exec vitest apps/web/src/app.test.ts
```

Whole-plan validation reports findings for every slice; `tao run` preflights only
the selected runnable slice. Agent budget and command-analysis warnings are
review signals only: they do not change plan status or block runs. See the
[plan-format contract](plan-format.md#validation) for schema-level details.

### Data and privacy

Tao is local-first.

```text
$TAO_DATA_HOME or ~/.local/share/tao/
└── repos/
    └── <repo-id>/
        ├── repo.json
        ├── notes/             # private backlog for this repository
        └── plans/
            └── <plan-id>/
                ├── state.json
                ├── slices.json
                ├── events.jsonl
                ├── plan.md
                ├── planning-brief.md
                ├── handoff.md
                └── review.md      # after a completed review
```

Treat Tao data-home contents and workspace-local `.tao/` metadata as local-only
and do not commit them. Note writes are private and atomic, and listing warns
about unreadable records while continuing with valid ones. Tao uses only the
repository-scoped `repos/<repo-id>/notes/` store; unrelated legacy global note
data is ignored. Notes are a CLI-only workflow.

Agent attribution and telemetry are best-effort; missing metrics do not fail
runs, and available attribution remains in plan events.

Pi starts a fresh `pi --mode rpc` session for each Tao run or commit operation.
Claude starts a fresh non-interactive Claude Code session, reads prompts from
stdin, and streams JSON output for logs and optional telemetry. Tao does not
currently write agent transcript or session sidecars.

If a Pi-backed provider or proxy drops a quiet long-lived response, increasing
Pi's SSE idle timeout or HTTP idle timeout can be an optional operational
mitigation; use the setting names and supported values documented by the
installed Pi/provider version. These settings do not establish recovery safety.
Tao does not override Pi transport configuration or infer success from a longer
connection. Its only automatic transport recovery is the fixed, structured-only
implementation-slice policy above; no provider-specific retry configuration is
added. `TAO_SESSION_TIMEOUT` remains Tao's provider-neutral wall-clock limit,
and every retry or later rerun still passes the durable Git boundary checks.
