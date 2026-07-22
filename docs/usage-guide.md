# Tao Usage Guide

Workflow judgment for using Tao day to day: when to reach for each command, how
to get the most out of it, and the gotchas the reference docs don't cover.

This guide is a companion to the [README](../README.md) (install, commands, and
the artifact contract in [plan-format.md](plan-format.md)). The README tells you
*what* each command is; this guide tells you *when* and *how* to use them well.

## The core loop

```
/plan  →  /slice  →  tao validate  →  tao run  →  tao review  →  tao merge (or PR)
                                      ↳ tao run --continue when blocked
                                                             ↳ automatic rework/run/review when changes requested
```

`/plan` and `/slice` are **prompts** you run inside Pi or Claude Code. `tao
validate`, `tao run`, and the rest are **CLI commands** you run from the
repository that owns the plan. The handoff between them is the plan directory in
Tao's data home — everything stays local and inspectable on disk.

A useful mental split:

- **Planning prompts** (`/plan`, `/grill-me`, `/improve-codebase-architecture`,
  `/improve-documentation`, `/repo-health`) are **read-only**. They never edit
  code or write Tao artifacts.
- **Build prompts** (`/slice`, `/run`, `/commit`, `/pr`) write artifacts, code,
  or git state.

## Capture first with repository notes

Use `tao note` (or `tao n`) when an idea is worth retaining but not worth interrupting your current work. From a registered checkout, the shortest capture path is:

```sh
tao n c tighten queue retry diagnostics
tao n c --tag testing add coverage for stale review heads
tao note create < longer-idea.md
```

Every note belongs to one registered repository. Commands choose the current checkout by default; use `--repo <unique-id-prefix-or-exact-name>` when capturing or inspecting another registered repository. This keeps a backlog attached to its codebase instead of creating a global inbox.

New notes are open. A plain `tao note` or `tao note list` shows open notes newest first; filters and `--all` help with later triage. Open notes can be edited or archived, and archived notes can be reopened. Promotion is a one-way handoff: promoted notes retain their destination and cannot be edited, archived, or reopened.

Choose promotion based on ambiguity, not size alone:

- **`tao note plan <id>`** is the default for anything that needs questions, tradeoffs, or scope decisions. It creates a durable source-linked planning record, but does not start an agent. Continue with the note as context in a fresh CLI agent planning session and slice only when the work is clear.
- **`tao note run <id>`** is an explicit shortcut for a note that already states a complete, unambiguous change. Tao first generates and validates a normal plan and rejects unresolved open questions. Only then does it mark the note promoted and invoke the ordinary run lifecycle.

If supervised-session creation or direct plan generation fails, the note remains open so you can edit and retry. If Tao created a destination but could not link it, follow the recovery identifier in the error instead of promoting again. Once direct generation produced and linked a valid plan, any approval stop, blocked slice, failed verification, review result, or later recovery belongs to that plan; resume it with the normal plan commands.

Direct note execution is not a bypass. Generated slices still honor dependencies and approvals; execution still honors agent, permission, timeout, workspace, commit, and pull-request settings; and completed work still follows the normal review and merge safeguards. When in doubt, use `note plan`.

---

## Planning commands

### `/plan <topic>` — lead with this

Read-only planning session that ends in a **Planning Packet** ready for `/slice`.

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
  /plan add bounded automatic retries to the durable run queue,
     must survive process restarts, don't change direct CLI runs
  ```

- **Treat it as an interview.** It asks one question at a time, each with a
  recommended answer and a reason. Your job is mostly to confirm or redirect —
  your domain knowledge enters when you disagree with a recommendation.
- **Answer only the final question shown.** Tao renders `/plan` once, but some
  agent hosts may show task, progress, or status text before the final assistant
  response. If a clarification looks duplicated, answer only the question in the
  final response. After updating Tao, rerun `tao install-prompts` to apply the
  latest managed prompt instructions.
- **Let it converge.** It stops when the plan is *specific enough to slice*, not
  when it's exhaustive. Don't push for more detail than `/slice` needs.

**What it won't do:** implement code, edit files, or create Tao artifacts. It
stops at the Packet and tells you to run `/slice`.

**Decision guide:**

| Situation | Move |
|---|---|
| You know the topic, even roughly | `/plan <topic with constraints/hunches>` immediately |
| One decision is genuinely thorny | `/plan`, then `/grill-me` on that decision |
| You can't even phrase the topic | Think until you can write one line, then `/plan` |
| You want to "research the code first" | Skip it — name the suspect files in your topic and let `/plan` inspect them |

### `/grill-me [focus]` — interrogate one decision

Interview-only prompt that drills into a specific design decision until the
constraints, risks, and open questions are clear. Same one-question-at-a-time,
recommended-answer style as `/plan`, but pointed at a single hard call rather
than the whole plan.

**When to use:** mid-planning, when one decision is load-bearing and you want it
pressure-tested before it propagates into slices. Run it *within* a `/plan`
session, then return to planning.

### `/improve-codebase-architecture [focus]` — find refactor opportunities

Read-only architecture review. Looks for shallow modules, concepts smeared
across many files, hard-to-change seams, and leaky coupling. Produces a numbered
list of opportunities (each with files, problem, proposed change, benefits,
risks, verification) and a **top-5 prioritization table**.

**When to use:** when you want a structured read on *where* the codebase is
hurting before committing to a refactor — not for a specific feature.

**How to use it well:** it ends by asking which opportunity you want to explore
next. The natural flow is to pick one, then take it into `/plan` → `/slice` to
turn it into executable work. It won't edit anything unless you explicitly ask.

### `/improve-documentation [focus]` — find documentation gaps

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
it into `/plan` → `/slice` to turn it into executable work. It only recommends —
it never edits, creates, or deletes files on its own.

### `/repo-health [focus]` — audit maintenance risk

Read-only audit of repository health: bloat and stray generated artifacts,
duplicate files, dependency/config sprawl, inconsistent structure, and anything
that makes future changes harder to review safely. Output is severity-ordered
findings (each with evidence, impact, recommendation, validation) plus a
prioritized action list.

**When to use:** periodic hygiene checks, or before onboarding work to a repo you
don't know well. It inspects `git status` first so your in-flight work isn't
mistaken for debt, and it marks uncertain findings as hypotheses rather than
overstating certainty. It will not delete, clean, or commit anything on its own.

---

## Build commands

### `/slice` — turn a plan into executable artifacts

Converts the current planning conversation into a durable Tao plan: it runs
`tao init --slug <short-slug> --json` to allocate a plan directory, then writes
`state.json`, `slices.json`, `plan.md`, `planning-brief.md`, `handoff.md`, and
`events.jsonl`.

**When to use:** right after `/plan` (or `/grill-me`) lands a Planning Packet you
believe in. Run it in the **same session** so it inherits the full planning
context — it slices from the conversation, not from a file you pass it.

**What good slices look like** (the prompt enforces these):

- Small and **serial** — prefer 30–90 minute slices, each independently
  reviewable, each leaving the repo in a valid state.
- Every slice carries **verification commands** chosen from *repository-owned*
  sources (`AGENTS.md`, `CLAUDE.md`, `README.md`, build files, task runners, CI),
  not invented. It prefers the **narrowest** documented command that covers the
  touched area over broad `go test ./...` / `make test` sweeps.
- Work needing sign-off is marked with an explicit **`approval` gate**, not
  smuggled in as an ordinary slice.

**After slicing:** it recommends `tao validate <plan-id>`. Do that next.

> **Tip:** if `tao validate` flags a verification command, the usual cause is an
> execution-context mismatch — e.g. a `pnpm --filter <pkg>` command paired with a
> repo-root-relative test path. Prefer package-relative paths. See
> [Validation warnings](#validation-warnings) below.

### `/run` and `tao run` — execute slices

Day to day you run **`tao run <plan-id>`** from the CLI. Under the hood the
`/run` prompt puts the agent in **WORK mode** to implement exactly **one** pending
slice at a time: select the next slice, honor `depends_on` and approval gates,
implement only that slice, run its verification commands, and complete it via
`tao slice-complete` (which Tao uses to update state, queue movement, and
events). It stops on blockers and failed verification rather than pushing
through.

**When to use which form:**

- `tao run <plan-id>` — normal execution of all pending slices.
- `tao run --max-slices 1 <plan-id>` — run a single slice to inspect results.
- `tao run --continue <plan-id>` — resume after you've cleared a blocker. It
  clears the blocked lifecycle state but does **not** bypass approval gates,
  dependencies, or verification preflight.

**Before running, prefer `tao validate <plan-id>`** for whole-plan findings —
`tao run` only preflights the one slice it's about to execute.

When a full plan run completes, treat "done" as slices complete, a persisted
repository-wide verification result, and a post-completion review result. With
the default slice policy, every modifying slice already has a Tao-owned
checkpoint commit, so the review can inspect the exact `base..HEAD` diff. Broad
verification is blocking and uses the repository's declared `make verify` when
available before narrower Make/Go fallbacks. The review is best-effort: a failed
or timed-out review session is recorded for you to see, but it does not turn
verified work into a failed run.

When a successful review requests changes, `tao run <plan-id>` automatically
uses the ordinary rework gates, runs the generated fix slices, and reviews again.
`tao run --all` uses the same default-on loop. Both stop with an error after five
rework cycles or when consecutive reviews repeat an equivalent finding set,
leaving the latest `changes_requested` review intact. Change the cap with
`--max-rework-attempts N`; disable the loop with `--auto-rework=false` or
`TAO_AUTO_REWORK=false`. Disabling review with `--no-review` or
`TAO_REVIEW=false` also disables automatic rework.

After either stop, a later `tao run` refuses to silently grant the plan a fresh
automatic-rework budget. It reports the persisted stop reason and, for an
equivalent-findings stall, repeats the loud finding-bearing warning. Inspect and
address the review first. If you deliberately want another bounded budget, pass
`--rework-restart`; this is an acknowledgment, not a bypass of ordinary rework
gates. The refusal never prompts, so unattended `tao run --all` selects stopped
plans to report the refusal safely even though they have no pending slices.
Unattended `tao run`/`tao run --all` callers that intentionally continue must
also pass `--rework-restart`; `run --all` then opens the first round of a fresh
durable queue budget before dispatch. For a manually managed durable queue,
continue the stopped plan explicitly before starting another drain.

When you return to a slice-complete or reviewed plan, start with
`tao review <plan-id>`. It reads the persisted review from the data-home plan
directory, so you can triage the verdict, summary, and findings before opening a
PR or merging. If you make follow-up commits, amend the branch, or otherwise
change the diff after the recorded review, run `tao review --run <plan-id>` to
refresh it against current `HEAD`. Use `tao staleness <plan-id>` for the separate
base-commit drift check on pending work.

Run-path agent sessions have a wall-clock hang ceiling so unattended batches do
not stall forever on one stuck agent process. The default is 20 minutes; set
`TAO_SESSION_TIMEOUT` to another Go duration such as `45m`, or to `0` to disable
the ceiling. Interactive planning sessions (`/plan` and `/slice`) are not
subject to this timeout.

#### Recover an interrupted slice

Rerun the same direct command, or restart the durable queue, and let Tao inspect
the recorded execution boundary before touching the workspace. Direct and
queued runs use the same execution service and cross-process plan lock, so the
safe choice does not depend on how the run was launched:

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

`tao run --continue` has a different purpose: it clears lifecycle blocker state
after you resolve a recorded blocker. It does not override any interrupted-slice
boundary check.

### `tao rework` — turn review findings into follow-up slices

**When to use:** after `tao review <plan-id>` shows a reviewed plan with a
`changes_requested` verdict and actionable findings. `tao rework <plan-id>` is
the standard way to continue the review → rework → run → re-review → merge loop
without creating a second plan.

By default, `tao rework` is gated and non-mutating on refusal. It refuses unless
the plan is reviewed, the persisted review requested changes, and Tao can find
actionable findings; approved reviews, reviews with no findings, and unfinished
plans are left untouched. Use `--force` only when you intentionally want to
bypass those gates.

**What it does:** Tao deterministically maps each structured finding to one new
pending rework slice, preserving the finding's goal, files, and tasks when
available. Each generated slice carries a deterministic verification command
scoped to the touched package rather than a narrow test-name filter. Tao appends
those slices, flips the same plan back to runnable, records the reopen event, and
keeps completed slices and history intact.

Rework reopens the same plan on its existing branch. It does not create a child
plan, does not discard the completed work, and does not mutate git state. Direct
`tao run` normally performs this rework/run/review loop automatically. Use the
standalone command when automatic rework is disabled or when you want to inspect
the generated slices before running them; add `--run` to hand the reopened plan
back to `tao run`.

Unlike direct runs and `tao run --all`, unattended durable drains remain opt-in:
`tao queue start --auto-rework` applies the same ordinary, non-forced gates after
each fresh automatic review. It defaults to five rework cycles beyond the
initial run; use `--max-rework-attempts N` to set another cap, or zero to disable
the loop. `TAO_AUTO_REWORK=true` opts in through the environment and
`TAO_MAX_REWORK_ATTEMPTS` changes the cap; explicit flags take precedence.
Automatic review must remain enabled.

The queue persists the policy, round count, and high-confidence fingerprint of
the complete normalized finding set, so restart reconciliation resumes the same
bounded loop. It stops and classifies the queue entry as failed at the cap or
when two consecutive reviews match that full finding identity. Distinct findings
in the same file continue within the bounded budget. In either case the plan
remains `changes_requested` and the latest review is preserved for manual
diagnosis. Approval and merge always remain manual.

### Unattended queue — batch plan runs

Use the unattended queue when you have several already-sliced, validated plans
and want Tao to keep working without choosing the next plan by hand. It is
CLI-first and local: the drainer runs in your terminal, persists per-repo queue
state under Tao data home, and exits when the queue is drained or stopped. If the
drainer is interrupted, start it again to reconcile runnable plans from disk and
resume the remaining work.

Use `tao queue add <plan>...` to record pending entries without starting work,
and `tao queue status` for an active-first view of the durable queue. Its focused
default keeps every queued and running entry visible, along with failures from
the last 24 hours and successes from the last hour. When you need older results,
use `tao queue status --all`; switching views does not prune queue history.
`tao queue start` is the foreground drainer: it reconciles currently runnable
plans into the queue, runs pending plans, records failed plans durably, and
continues with the rest. Add `--auto-rework` for the bounded review/rework loop
described above, optionally with `--max-rework-attempts N`. Use
`tao queue stop <plan>` to remove a pending entry before it starts.

For a one-shot unattended batch, run `tao run --all`; it auto-reworks by default
just like a single-plan run. Add `--active` when you only want active runnable
plans. Keep CLI queue concurrency at the default of 1
unless you have confirmed the plans cannot touch the same files: values above 1
for `tao queue start --max-parallel N` are not cross-plan-conflict-safe.

When you come back, start with the focused `tao queue status` view and
`tao status` for the repository-wide plan rollup. Reach for `--all` when an
older result needs investigation. Together these commands answer the unattended
check: "N of M done & reviewed, K failed" before you inspect individual plans.
Then read each slice-complete or reviewed plan's persisted review before deciding
whether to run `tao rework`, open a PR, run `tao merge`, or queue follow-up work.

To get that headline pushed to another tool, set `TAO_NOTIFY_COMMAND` before
starting the foreground drainer, for example:

```sh
export TAO_NOTIFY_COMMAND='notify-send "Tao batch" "$TAO_BATCH_REVIEWED of $TAO_BATCH_TOTAL done & reviewed, $TAO_BATCH_FAILED failed"'
tao queue start --auto-rework --max-rework-attempts 5
```

Tao always prints the final batch summary. The notifier is best-effort, runs as a
shell command with `TAO_BATCH_*` summary variables in its environment, and an
unset, failing, or timed-out notifier does not fail the batch.

### `/commit` — local conventional commit

Creates a single local Git commit for the current changes, matching the repo's
recent commit style. Reviews `git status`, `git diff`, and `git log` first; uses
`<type>(<scope>): <summary>` conventional format; refuses to commit `.tao/`
local-only artifacts, secrets, or build output; and never pushes.

**When to use:** when you're committing outside a run, or when you chose
`tao run --commit-policy none`. Automatic runs do not delegate slice commits to
this prompt: `tao slice-complete` owns that deterministic transaction.

### `/pr` — open a pull request

Inspects the branch, status, recent commits, and diff against the base branch
(defaults to `main`), pushes if needed, and opens a PR with a structured
description (summary, motivation, scope, testing, risks, rollback). Returns the
PR URL.

**When to use:** after a run's work is committed and you want a PR by hand.
Equivalent automated path: `tao run --pull-request`, which is gated — it's
rejected with `--commit-policy none`, or when the run is not in
`--execution-mode isolated`.

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
the reviewed plan branch, and creates one deterministic commit with `Tao-Plan`
and `Tao-Source-Head` trailers. The plan branch keeps its per-slice checkpoint
history for review and recovery until managed cleanup succeeds. Use
`--no-squash` to preserve those commits by rebasing the plan branch and
fast-forwarding default.

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

If a PR or manual `git merge` already integrated the plan, run `tao merge <plan>`
anyway. Tao checks the plan branch, review head, PR head SHA, and workspace head
SHA against the default branch. When any is already an ancestor of default, Tao
skips rebase/fast-forward, records `plan_merged`, marks the plan `completed`,
and attempts safe cleanup. A plan whose branch is the default branch itself
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
while it creates exactly one squash commit per source plan, with `Tao-Plan` and
`Tao-Source-Head` trailers, in a batch-owned integration worktree. A textual
conflict or candidate verification failure is deferred and sent to bounded
sessions of the configured agent. Agents may edit only that integration
worktree; Tao stages and commits. Failed, empty, unsafe, ref-changing, repeated,
or attempt-capped resolution stops with source branches and durable recovery
evidence intact.

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

## Putting it together

A typical feature, end to end:

```text
/plan add X, constraints Y and Z, don't touch W      # interview → Planning Packet
/grill-me the storage-format decision                # only if one call is hard
/slice                                               # writes the plan directory
```
```sh
tao validate <plan-id>     # check generated verification commands
tao run <plan-id>          # execute pending slices
tao run --continue <plan-id>   # after clearing any blocker
tao review <plan-id>       # read the persisted post-completion review
tao rework --run <plan-id> # if review requests changes, reopen and run fixes
tao review --run <plan-id> # refresh the review after follow-up changes
tao merge <plan-id>        # no-PR path after you accept an approved review
tao run --pull-request <plan-id>   # or /pr by hand
```

For exploration rather than a specific change, start with
`/improve-codebase-architecture` or `/repo-health`, pick one finding, and feed it
into the loop above.

---

## Behavior reference

The sections below are the detailed behavior the README summarizes: how runs
commit, where they execute, what validation can analyze, and how Tao treats your
data.

### Commits, branches, and pull requests

`tao run` defaults to `--commit-policy slice`, which requires a clean execution
worktree before each agent starts. On completion, `tao slice-complete` first
persists a deterministic intent, screens and stages safe changes, creates a
commit carrying `Tao-Plan` and `Tao-Slice` trailers, and only then marks the slice
complete. If Tao is interrupted after Git advances, retrying recovers the commit
only when its parent and full message match the recorded intent. A slice that
made no changes records `no_changes` without creating an empty commit.

Slice `expected_files` remain advisory: Tao warns when a safe committed path was
not declared, but does not treat the list as an allowlist. Suspected secrets,
generated paths, ambiguous Git status, protected branches, changed branch/HEAD
boundaries, and leftover changes stop automatic completion. These checkpoint
commits make partial and `--max-slices` runs recoverable and provide the exact
history reviewed before integration.

Use `--commit-policy none` when you explicitly want manual ownership. Completion
then records `manual_uncommitted` and does not mutate Git; commit with `/commit`
or your own workflow. In current mode this means work may remain uncommitted and
Tao does not enforce automatic-policy cleanliness. In isolated mode, prefer the
default slice policy unless you have a concrete manual history requirement.

`plan` is historical metadata only, not an executable policy. New CLI, prompt,
environment, and queue inputs reject it with migration guidance. For a dirty
legacy plan-policy worktree, inspect the diff, commit or stash it manually, then
rerun with `slice`; use `none` only if you intentionally accept manual
uncommitted completion. Tao never guesses how to split or migrate that dirty
work.

`tao run` defaults to `--execution-mode isolated`, the single knob that drives
both workspace placement and branch behavior. `isolated` creates or reuses a
`tao/<plan-id>` feature branch and never commits directly to `main` or `master`.
Use `--execution-mode current` to run in place on the branch where the run
started. Set `TAO_EXECUTION_MODE=isolated|current` to change the CLI default.

Use `tao run --pull-request` to create a GitHub pull request after a full run
completes and Tao has committed and reviewed the plan work. PR creation is
disabled by default and is valid only in `--execution-mode isolated` — requesting
a PR in `current` mode is a hard error, as is combining it with policy `none`.
Tao pushes and opens the PR through authenticated `gh`, but the hosting provider
owns integration. Choose the host's **Squash and merge** action, then run
`tao merge --record-only --force <plan>` to record completion and clean local
state. Tao does not click or emulate the host merge action.

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
dependencies in the workspace root when a supported lockfile is present. It does
not fetch or pull remotes for this pre-run rebase. Dirty worktrees and rebase
conflicts fail early, before the agent runs, so you can clean, stash, or resolve
manually and retry. `--execution-mode current` runs in place and never performs
this automatic rebase.

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

`tao cleanup --dry-run` previews completed-plan cleanup for both Tao-managed
workspaces and local branches. Normal `tao cleanup` removes eligible clean
completed-plan workspaces before deleting their local branches, but does not
remove plan artifacts in Tao's data home. It never deletes protected branches or
dirty workspaces, and without `--force` it relies on `git branch --delete` so
unmerged branches stay protected by Git. The exception is a Tao-recorded squash:
verified squash evidence permits deleting its intentionally non-ancestral source
branch, but never bypasses the final dirty-workspace check.

Even after a plan branch has been merged, make clean-worktree verification your
safe-deletion habit before removing the branch or workspace by hand. A clean
status confirms each automatic slice transaction settled without leftovers.

### Validation warnings

Verification analysis is conservative. It understands common command shapes such
as `cd DIR && ...`, `pnpm --dir DIR ...`, `pnpm --filter PACKAGE ...`, direct
`go test`, and direct Vitest or Jest file arguments.

For package-cwd runners, prefer package-relative paths:

```sh
pnpm --filter web exec vitest src/app.test.ts
```

instead of mixing a package filter with a repo-root path like:

```sh
pnpm --filter web exec vitest apps/web/src/app.test.ts
```

Whole-plan validation reports findings for every slice; `tao run` preflights only
the selected runnable slice. Tao also surfaces informational agent budget
warnings in CLI views. These are review signals only; they do not change plan
status or block runs.

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
Tao does not override Pi transport configuration, add provider-specific retries,
or infer success from a longer connection: `TAO_SESSION_TIMEOUT` remains Tao's
provider-neutral wall-clock limit, and every rerun still passes the durable Git
boundary checks above.
