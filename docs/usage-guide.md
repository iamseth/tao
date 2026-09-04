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
  `/tao-improve-documentation`, `/tao-repo-health`, `/tao-insights-review`) are
  **read-only**. They never edit code or write Tao artifacts.
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

Use `tao ui` when you want one terminal view for plans and open notes across
registered repositories, or want to launch a common plan action without first
copying an ID. It requires a terminal; use `tao monitor --once` for redirected
or pasteable output.

The dashboard opens on **Plans**. Use `Tab` or the horizontal arrows to move
among **Plans**, **Notes**, **Settings**, and **Debug**; use `j`/`k` or the
vertical arrows to select rows. `Page Up` and `Page Down` move by a viewport on
long pages. `Enter` opens details, `Esc` returns, and `f` toggles repository
focus on Plans and Notes. On either list, `gg` jumps to the first visible item
and `G` jumps to the last. In plan detail, `Tab` and `Shift+Tab` switch detail
tabs while left and right open the previous or next visible plan. Notes are grouped by numeric tier,
with lower tiers first and untiered notes last; their rows show all non-tier tags
plus both creation age and update recency. On the Notes list or detail view,
`Ctrl+G` opens the selected note in `$EDITOR` (or `nvim` when unset); edit the
tag lines and body, then write and quit to persist the changes. Press `c` to
copy the selected note ID to the system clipboard for a planning session. Keys
`0` through `3` replace the selected note's tier tag. Lowercase `d` asks before deleting the
note; uppercase `D` deletes it immediately. Deletion archives the note and
removes it from the open Notes list. **Done** is always
displayed with up to 15 completed or abandoned plans. **Now** contains
in-progress, blocked, reviewed, and other plans with an immediate action
such as monitor, approve, or merge. **Next** contains planned work. On Plans, the
principal actions are run (`r`), approve (`a`), merge one (`m`), and merge the
repository's approved set (`M`); confirmations and the underlying commands still
enforce every normal gate. Settings can change a repository's pull-request
default, while Debug remains read-only.

Treat **NEXT**, ordering, heartbeats, and `stalled?`/`crashed?` labels as advice
or liveness hints, never as approval, failure, or merge evidence. TUI-launched
run, approval, and merge processes are detached and survive dashboard exit, so
check durable plan state rather than assuming that exiting or seeing a launch
label stopped or completed the action.

Run `tao ui --help` for the exact keys, tabs, actions, confirmation behavior,
and display options. The [README command index](../README.md#command-reference)
links the non-interactive commands behind those actions.

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

**Choose the run size:**

- `tao run <plan-id>` — normal execution of all pending slices. Choose this when
  Tao can continue unattended through final verification and review.
- `tao run --max-slices 1 <plan-id>` — stop after one slice. Choose this when you
  want to inspect a checkpoint, limit the first handoff, or make a decision
  before more slices run. The next ordinary `tao run` continues the remainder.

**Choose recovery from the durable condition, not from how the failure looked:**

| Durable condition | Action | Why this action |
| --- | --- | --- |
| An approval-gated pending slice is not approved | `tao approve [--slice ID] <plan-id>`, then `tao run <plan-id>` | Approval satisfies the gate; it is not blocker recovery. |
| The plan records an ordinary blocker and you have resolved its stated cause | `tao run --continue <plan-id>` | `--continue` explicitly clears blocker lifecycle state. Tao does not infer resolution. |
| A clean isolated automatic slice is blocked on an older execution baseline, and a prerequisite has now produced a strictly newer baseline | `tao run --restart <plan-id>` | `--restart` supersedes that safe blocked boundary and preflights again; it is not a general retry. |
| An implementation handoff was interrupted before completion | Rerun the same `tao run` command | Tao classifies the recorded workspace, branch, head, policy, intent, and dirt before deciding whether resume is safe. `--continue` and `--restart` do not bypass that check. |
| Final verification failed with recorded classification `code` | `tao run --repair-verification <plan-id>` | Tao appends and runs one bounded repair slice for the failed final gate. |
| Final verification is legacy-unclassified, or its recorded external cause (`tool_missing`, `timeout`, `cancelled`, or `invalid_command`) has been resolved | `tao run --reverify <plan-id>` | Tao reruns final verification without a repair slice and requires the exact recorded Git head. |

Under `--commit-policy none`, a successful same-head reverification does not by
itself prove that permitted uncommitted work was committed.

Runtime prerequisites are checked before workspace preparation or agent launch.
A dependent plan becomes runnable only after each exact same-repository
prerequisite has current Tao merge evidence that is ancestral to the selected
baseline; advisory sequence order is not authority.

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
`--no-run-header`, or set `TAO_RUN_HEADER=0` to opt out by default. The header is
enabled when `TAO_RUN_HEADER` is unset or has any value other than exactly `0`.

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

When Tao records who approved an approval gate, it prefers the OS user's display
name and then login name. `TAO_APPROVED_BY` is only a fallback (ahead of `USER`
and `USERNAME`), so a status row showing it as an environment override may not
reflect the approver ultimately recorded.

Run-path agent sessions have a wall-clock hang ceiling so unattended batches do
not stall forever on one stuck agent process. The default is 20 minutes; set
`TAO_SESSION_TIMEOUT` to another Go duration such as `45m`, or to `0` to disable
the ceiling. Interactive planning sessions (`/tao-plan` and `/tao-slice`) are not
subject to this timeout.

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
does not override any interrupted-slice boundary check. When a clean automatic
slice was blocked by a prerequisite and the baseline has since advanced, use
`tao run --restart` instead; Tao records the superseded boundary and re-runs
prerequisite and selected-slice preflight before handoff. A failed broad final
gate is not an interrupted implementation slice: follow its recorded
classification, using `tao run --repair-verification` for code repair or, after
resolving a non-code cause, `tao run --reverify` at the recorded head.

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

Without `--from-pr`, `tao rework <plan-id>` is the manual form for a persisted
`changes_requested` review with actionable findings.

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

**Single-plan conflict behavior:** an ordinary squash conflict starts exactly
one configured provider-neutral resolver session in the default worktree while
the default and source refs remain at their recorded boundaries. These sessions
use the platform filesystem sandbox; Linux requires an externally installed
`bwrap` at `/usr/bin/bwrap` or `/bin/bwrap`. `tao doctor` passively checks the
executable, confinement, ephemeral configuration projection, RPC initialization,
selected model, and local credential readiness without sending a model request;
remote credential validity remains unproven. Tao runs that same disposable RPC
readiness path before recording one-shot `requested` evidence. A readiness
failure sends no attributed prompt, restores the prepared squash boundary, and
leaves a later explicit invocation free to try again. Tao treats the
plan title, source review, changed paths, conflict status, and provider output as
untrusted. The resolver may edit only; Tao rejects unsafe paths, unresolved
entries or markers, malformed output, protected-ref or HEAD movement, empty
edits, and invalid proposals before it stages or commits. Tao then fingerprints
the exact edits, persists intent, creates the resolution commit itself, runs the
configured verification gate, and asks a separate fresh session to review the
exact parent/head integration. Only independent `approve` authorizes merge
evidence and cleanup. Tao never retries automatically. After `requested`, only
structured `not_transmitted` or explicit prompt-rejection evidence can rearm a
later explicit `tao merge`, and only after the exact default/source refs, HEAD,
branch, and clean worktree are restored and the matching request is cleared by
compare-and-set. Accepted or unknown delivery, partial writes, missing responses,
timeouts, post-transmission cancellation, remote authentication rejection,
provider/model execution errors, rollback failure, or concurrent drift consume
the one-shot authority and retain manual recovery behavior.

`--force` does not bypass resolver validation or independent review.
`--no-verify` skips only command verification; structural validation and exact
independent review remain required. A failed attempt restores the recorded
default boundary when it still matches and otherwise refuses rollback rather
than overwriting drift. Reconcile findings or drift on the plan branch and its
source review; do not use `tao merge --all` as a repair path for a failed
single-plan transaction.

`--no-squash` remains different: rebase or fast-forward conflicts abort, print
the conflicted files, and require manual resolution on the plan branch followed
by a refreshed review when content changes. It never invokes this squash
resolver/reviewer lifecycle.

**Verification and cleanup:** after integration, Tao prefers a repository's
declared `make verify` target. Without one, it uses declared Make `build` and/or
`test` targets, or native `go build ./... && go test ./...` for a Go module.
Make is not required: Tao invokes it only when the repository declares a
recognized target, and skips automatic verification when no supported gate is
detected. `--verify-command CMD` overrides detection for one merge;
`TAO_MERGE_VERIFY_COMMAND` provides the environment override. Leaving the
variable unset uses build-system detection, while setting it to an empty string
disables merge verification. If verification fails, Tao resets default to the
pre-merge SHA before cleanup. If it passes, Tao
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
fast-forward and keeps conflict resolution manual. `--no-verify` skips the
post-merge command gate, including explicit flag or environment overrides, but
not structural conflict checks or the independent exact-integration review.
`--force` bypasses approval, review-base, review-head, and dirty-worktree
pre-merge gates and is passed to managed cleanup; it cannot bypass automatic
resolution safety or turn any independent non-approval into authorization.

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
fresh aggregate review. `TAO_AGGREGATE_REVIEW_CONVERGENCE_WINDOW` controls how
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
access-controlled sharing drafts, not public exports. Choose the normal report
for implementation and outcome context, or `--planning-only` when the audience
should see planning context without execution-derived data:

```sh
tao report --output plan-report.md <plan-id>
tao report --planning-only --output prompts/my-plan.md <plan-id>
```

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
tao run --repair-verification <plan-id> # code-classified final-gate failure
tao run --reverify <plan-id>           # resolved external/legacy final-gate failure
```

A `changes_requested` review normally triggers bounded automatic rework. If you
chose manual control, inspect the review and reopen explicitly:

```sh
tao review <plan-id>
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

Use `tao run --pull-request` only for an isolated, automatically committed run.
Tao requires an approved review for the exact head before push or forge mutation.
If interrupted, rerun the same command and follow `tao show <plan>` rather than
repairing PR metadata by hand. Matching approved-review and PR metadata can make
the local Tao workflow `completed`, but does not prove remote review, CI, or
merge; only recorded merge evidence proves default-branch integration. Exact PR
body, title, labeling, assignment, and option behavior belongs in
`tao run --help` and the README command reference.

### Workspaces

Isolated runs may update a stale clean workspace from the current local default
branch, prepare dependencies when a supported lockfile is present, and then
check the selected slice's required inputs inside that workspace. Tao does not
fetch or pull first; dirt, conflicts, or missing inputs stop before the agent.
Resolve the reported condition rather than editing workspace metadata or relying
on an earlier slice's promise that a file should exist.

Cleanup is explicit and preview-first. Use `tao workspace clean <plan>` for one
workspace and `tao cleanup --dry-run` after integration for repository-wide
managed cleanup. PR completion alone is not deletion authority: protected,
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

### Data and privacy

Tao is local-first. Treat its data home and workspace-local `.tao/` metadata as
private, local-only state and never commit them. Notes belong only to their
registered repository; legacy global note files are ignored. Missing telemetry
never blocks a run, and Tao does not currently write agent transcript sidecars.

The [plan-format contract](plan-format.md#plan-directory) is the authority for
exact plan files, local-only runtime artifacts, events, and legacy readability.
Use `tao status` for resolved runtime settings and the
[README configuration section](../README.md#configuration) for supported
configuration; provider tuning never replaces Tao's durable plan and Git
recovery checks.
