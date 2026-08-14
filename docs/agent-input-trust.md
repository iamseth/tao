# Agent-authored input trust audit

This audit inventories places where Tao consumes data written by an agent or by an agent-driven tool. The focus is reliability hardening at interface boundaries: plan artifacts, agent stdout transports, captured agent output, and plan metadata that agents can edit during `/tao-slice` or `/tao-run`.

## Core `state.json` and `slices.json` plan artifacts

- **Agent-authored input:** `/tao-slice` agents write `state.json`, `slices.json`, `planning-brief.md`, and related plan files; `/tao-run` agents can also edit `slices.json` on exceptional paths.
- **Consumed by:** `internal/plan/artifact_io.go`, `internal/plan/repository.go`, `internal/plan/validate.go`, `internal/plan/lifecycle.go`, `internal/run/execute.go`, `internal/run/finalize.go`, and CLI commands that resolve or render plans.
- **Current trust assumption:** Required JSON must parse. Tao-owned multi-artifact lifecycle writes use a private mutation journal, but most inconsistencies in legacy or directly edited plans remain warnings so they stay readable; selected-slice verification preflight blocks only a narrow set of runnable-slice hazards.
- **Concrete failure mode:** A malformed required JSON file makes a plan invalid. A syntactically valid but wrong `pending_slices`, `current_slice`, `repo.root`, `workspace`, `expected_files`, or `verification.commands` field can select the wrong slice, point execution at the wrong checkout, obscure advisory commit-scope warnings, or send bad instructions back to the agent in the run packet. The journal provides crash consistency for conforming Tao writers, not authenticity for agent-writable targets.
- **Severity:** High.
- **Recommendation:** Keep load tolerant, but add targeted validation warnings/errors for unsafe paths and workspace roots at run-gating boundaries; warn on broad or parent-traversing `expected_files` that weaken advisory slice-commit scope comparisons.

## Tao-owned plan mutation journal

- **Agent-authored input:** None on the conforming path. `.mutation.json` is an internal Tao intent file, although the current filesystem boundary does not prevent a non-conforming agent or local process from altering it or its targets.
- **Consumed by:** `internal/plan/mutation_journal.go` at full-plan load, state-only read, and journaled mutation entry points; run, slice-complete, review, and merge consumers receive only settled required artifacts.
- **Current trust assumption:** A valid `tao.plan.mutation.v1` journal is authoritative. Tao checks plan IDs, exact payload hashes, event mutation IDs, and event conflicts before replay, then rolls forward idempotently and removes the journal durably last. Invalid intent blocks reads and writes without changing any target.
- **Concrete failure mode:** A process with artifact write access can forge a self-consistent journal, replace targets after settlement, or exhaust storage with large payloads. The protocol does not authenticate the writer, impose a general artifact quota, transact optional Markdown/telemetry sidecars, or repair historical torn writes that have no journal.
- **Severity:** High if the shared filesystem is hostile; low for the crash-prefix failures the journal is designed to recover.
- **Recommendation:** Keep journal creation and settlement inside `internal/plan`, preserve explicit no-journal legacy recovery, and treat stronger ownership, authentication, and artifact quotas as separate hardening work rather than weakening deterministic replay.

## `slice-complete` notes and verification-results files

- **Agent-authored input:** Temporary notes and verification-results JSON files passed to `tao slice-complete --notes-file ... --verification-results-file ...`.
- **Consumed by:** `internal/cli/slice_complete.go` reads both files, unmarshals `[]plan.VerificationRun`, normalizes relative `cwd` values against the command's current working directory, and passes them to `PlanRecord.CompleteSlice`.
- **Current trust assumption:** The agent is responsible for accurate notes and verification results. Tao only requires parseable JSON and then persists trimmed notes and arbitrary command/cwd/result/details strings into `slices.json`.
- **Concrete failure mode:** Oversized notes or result details can bloat `slices.json`; a bogus `result` can falsely record verification as passed; a relative or misleading `cwd` can become durable drift evidence; an excessive result array can make summaries and run packets noisy.
- **Severity:** High for correctness of completed-slice evidence; medium for bloat/availability.
- **Recommendation:** Add size caps and shape validation at the `slice-complete` boundary, while preserving legacy readability for already persisted artifacts.

## Tao-owned blocked-path input and residual direct artifact writes

- **Agent-authored input:** `prompts/run.md` instructs agents to pass a temporary blocker-reason file and optional invalid/corrected verification-command flags to `tao slice-blocked`; non-conforming agents can still write plan artifacts directly.
- **Consumed by:** `internal/cli/slice_blocked.go` bounds and validates the inputs, calls `PlanRecord.BlockSlice` to update `state.json` and `slices.json`, and records Tao-owned `slice_blocked` plus optional `verification_command_invalid` events in `events.jsonl`.
- **Current trust assumption:** The command owns the canonical blocked shape and idempotent event emission, but it trusts bounded agent-provided reasons and verification evidence. Plan files remain writable to the agent, and readers preserve tolerant legacy handling.
- **Concrete failure mode:** An agent can supply false but bounded blocker or verification evidence. A non-conforming agent can bypass the command and directly forge events or create inconsistent state/slice metadata; bounded event-line reading prevents oversized lines from making the whole plan unloadable but cannot establish event authenticity.
- **Severity:** Medium for false bounded evidence and audit confusion; high only if non-conforming direct writes corrupt required artifacts.
- **Recommendation:** Keep conforming blocked-path mutations behind `slice-blocked`, treat event content as untrusted audit data, and preserve tolerant legacy reads. Preventing forged direct writes requires a stronger ownership or filesystem boundary.

## Run packet and prompt rendering from agent-written plan fields

- **Agent-authored input:** Slice titles, goals, context, tasks, expected files, verification commands, planning brief, prior notes, and recent events.
- **Consumed by:** `internal/plan/run_packet.go` and `internal/run/agent_prompts.go` render these values into the next agent prompt.
- **Current trust assumption:** Plan text is operational context for the next agent. Tao does not escape or label individual fields as untrusted beyond surrounding system/run instructions.
- **Concrete failure mode:** Prompt-injection text in an agent-authored plan field can tell the next agent to ignore Tao's run protocol, skip verification, edit outside scope, or alter completion metadata incorrectly.
- **Severity:** Medium.
- **Recommendation:** Keep compact packets, but make future prompt templates mark plan fields as untrusted data and preserve stronger instructions outside agent-authored sections.

## Verification commands and verification step metadata

- **Agent-authored input:** `slices[].verification.commands`, optional `verification.steps`, and completed-slice `verification_results`.
- **Consumed by:** `internal/plan/verification.go`, `internal/plan/verification/*`, `internal/run/execute.go`, `internal/workspace/preparer.go`, and run-packet rendering.
- **Current trust assumption:** Commands are planned by agents but preflighted before the selected slice runs. Completed verification results are evidence, not re-executed facts; `execution_root` now takes precedence over legacy `cwd` drift heuristics.
- **Concrete failure mode:** A command can be too broad, shell-sensitive, or path-mismatched; result details can be false; legacy `cwd` values can still influence workspace drift when no `execution_root` exists; optional step `cwd` values are preserved but not deeply normalized.
- **Severity:** Medium to high depending on whether the command gates a run or only records history.
- **Recommendation:** Continue strict selected-slice preflight and add artifact validation for unsafe absolute/parent paths in verification metadata.

## Slice-commit scope warnings derived from `expected_files`

- **Agent-authored input:** The current and completed slices' `expected_files` entries.
- **Consumed by:** `internal/run/commit_safety.go` builds the expected-path comparison set, and `internal/run/slice_completion.go` warns when actual slice-commit paths fall outside it.
- **Current trust assumption:** Expected files describe intended implementation scope but are advisory only. Slice completion safety-screens and stages all non-`.tao`, unambiguous changed paths; a safe undeclared path is still committed and produces a warning.
- **Concrete failure mode:** A broad glob, absolute-looking path, or `..` path can suppress or confuse the advisory scope warning and hide drift from the declared implementation scope. It cannot expand staging because candidates come from Git status and every candidate is screened independently of `expected_files`.
- **Severity:** Medium.
- **Recommendation:** Preserve validation warnings for unsafe `expected_files` patterns and keep the field advisory; never use it as staging authorization or to bypass commit-safety screening.

## Pi RPC stdout events and shared `streamjson` transport

- **Agent-authored input:** JSONL stdout from `pi --mode rpc`, `claude --output-format stream-json`, `opencode run --format json`, and `codex exec --json`.
- **Consumed by:** `internal/agent/pi/transport.go`, `internal/agent/pi/session.go`, `internal/agent/streamjson/streamjson.go`, and provider-specific handlers in `internal/agent/{claude,opencode,codex}`.
- **Current trust assumption:** The selected local agent binary emits well-formed provider events. Parse errors abort loudly; final unterminated lines are parsed; stdout read errors abort; unsupported Pi UI requests are cancelled.
- **Concrete failure mode:** A provider bug or adversarial wrapper can emit false success/failure events, oversized nested JSON, misleading assistant text, negative or implausible metrics, or log-control content. Removing the historical scanner cap fixed false `token too long` crashes but leaves memory proportional to the largest JSONL line.
- **Severity:** High for run control and captured review/PR outputs; medium for metrics/log quality.
- **Recommendation:** Preserve current loud parse-error semantics, but consider high, documented output/metric caps and defensive metric normalization where doing so does not reintroduce the old 1 MiB failure mode.

## Provider-specific assistant text, tool logs, and metrics extraction

- **Agent-authored input:** Assistant text, result events, tool-call events, tool-result text, usage/cost/session metadata in provider JSON maps.
- **Consumed by:** `internal/agent/pi/session.go`, `internal/agent/pi/metrics.go`, `internal/agent/claude/stream.go`, `internal/agent/opencode/client.go`, `internal/agent/codex/stream.go`, `internal/agent/jsonmap`, and `internal/run/metrics.go`.
- **Current trust assumption:** Handlers defensively type-assert fields and treat missing metrics as best-effort warnings; raw text is accumulated into session output and logs.
- **Concrete failure mode:** Very large assistant/tool text can bloat memory and `run.log`; malformed numeric fields become zero or truncated; negative metrics can flow into summaries; duplicated final text can make review/PR extraction ambiguous.
- **Severity:** Medium.
- **Recommendation:** Clamp metric values to non-negative ranges and consider output/log size policies separately from transport line parsing.

## Merge-batch conflict packets and aggregate reviews

- **Agent-authored input:** Provider output from deferred `tao merge --all` conflict/verification resolution, aggregate review verdicts and findings, and aggregate rework sessions.
- **Consumed by:** `internal/merge/batch_agent.go` confines resolution edits and records bounded summaries/fingerprints; `internal/merge/batch_review.go` parses aggregate output through the ordinary review parser and stores full review artifacts under repository-scoped merge-batch data.
- **Current trust assumption:** Packets contain untrusted source plan titles, review summaries, changed paths, conflict status, verification output, and prior-plan evidence. The configured agent may edit only the isolated integration worktree; it never owns staging, commits, source/default refs, landing, plan events, or cleanup. Aggregate `approve` is authoritative only when bound to the exact default base and integration head after full verification.
- **Concrete failure mode:** Plan or diff text can inject instructions; an agent can return malformed/false review output, leave conflict markers, make unsafe metadata edits, change refs, repeatedly produce equivalent findings, or create very large summaries/reviews. Semantic edits can be wrong even when Git conflicts are resolved.
- **Severity:** High because the combined output can gate atomic landing.
- **Mitigation:** Prompts label packet sections as untrusted data. Tao checks protected refs and workspace status around every session, rejects unsafe/empty edits, bounds summaries and durable findings, fingerprints repeated failures, caps conflict and aggregate-rework attempts, runs full verification after edits, requires a fresh aggregate approval, and keeps default/source cleanup Tao-owned. Full review output remains an audit artifact; reruns resume durable state rather than trusting agent claims about prior work.
- **Recommendation:** Preserve these ownership boundaries and size caps. Treat additional batch packet fields as untrusted, and never let provider output directly select candidates, execute Git settlement, weaken verification, or mutate source plan reviews.

## Review verdict and findings extraction

- **Agent-authored input:** A fresh review agent's stdout, including an optional fenced ```tao-review-json``` block with verdict, summary, and findings.
- **Consumed by:** `internal/run/review.go` parses the last fenced JSON block, persists full output to `review.md`, records `state.plan.review`, appends a `plan_reviewed` event, and downstream `tao review`, `tao rework`, `tao merge`, and CLI views consume that metadata.
- **Current trust assumption:** Valid review JSON is authoritative. Malformed JSON, unknown verdicts, or malformed findings degrade to a `comment` verdict with freeform summary; findings are otherwise trusted and counted.
- **Concrete failure mode:** An oversized review can bloat `review.md`, `state.json`, and `events.jsonl`; a malformed block can accidentally downgrade changes-requested to comment; findings with absolute, parent-traversing, or empty paths can generate unsafe rework slices; a false approve can gate `tao merge` if other merge checks pass.
- **Severity:** High because review metadata gates rework/merge decisions.
- **Recommendation:** Add bounds and normalization for review JSON fields and findings, and treat unsafe finding paths as non-actionable for deterministic rework.

## Review findings reused by `tao rework`

- **Agent-authored input:** `state.plan.review.findings` or fallback findings parsed from `review.md`.
- **Consumed by:** `internal/rework/findings.go`, `internal/rework/generate.go`, and `internal/cli/rework.go`.
- **Current trust assumption:** Review findings can be converted directly into pending rework slices, with file strings normalized only enough to create slugs and package-level verification commands.
- **Concrete failure mode:** A finding can produce an empty, broad, parent-traversing, or misleading expected file; suggestions become tasks verbatim; generated verification can fall back to repo-wide tests when the file is not a Go path.
- **Severity:** Medium.
- **Recommendation:** Normalize and filter finding file paths before slice generation; leave richer review-to-work planning for a future design.

## PR body drafting and PR command output

- **Agent-authored input:** Optional Markdown body text from a best-effort PR-body agent session.
- **Consumed by:** `internal/prbody` validates or deterministically rebuilds the body, `internal/run/pull_request.go` orchestrates the lifecycle and writes the body to a temp file, `internal/run/pull_request_push.go` applies Tao-owned push policy, and `internal/forge/github.go` runs `gh pr create/view` and parses forge responses.
- **Current trust assumption:** The agent only drafts prose; branch push, PR creation, existing-PR detection, and URL/number parsing are Tao-owned.
- **Concrete failure mode:** The body can be misleading or oversized, but a body-agent failure falls back to a deterministic Tao body and no longer blocks completed work.
- **Severity:** Low to medium.
- **Recommendation:** Keep PR body generation best-effort and preserve deterministic `gh` creation as the source of PR metadata.

## Optional markdown and planning sidecars

- **Agent-authored input:** `planning-brief.md`, `plan.md`, legacy planning-session sidecars, and `review.md`.
- **Consumed by:** `internal/plan/artifact_io.go`, `internal/plan/derive.go`, `internal/plan/run_packet.go`, and `internal/cli/show.go`.
- **Current trust assumption:** Optional sidecars are local context and warnings; missing files usually stay non-fatal. Markdown is parsed with simple heading/list helpers or displayed as content.
- **Concrete failure mode:** Very large sidecars can inflate memory, prompts, and terminal output; misleading planning metrics can influence operator judgment even though lifecycle remains state/slice based.
- **Severity:** Low to medium.
- **Recommendation:** Add read-size caps for optional sidecars and surface truncation/unreadable warnings rather than failing core plan loads.

## Agent telemetry events and budget warnings

- **Agent-authored input:** Metrics extracted from agent stdout and durable `agent_metrics` events in `events.jsonl`.
- **Consumed by:** `internal/run/metrics.go`, `internal/plan/telemetry.go`, CLI status summaries, and pre-run budget warnings.
- **Current trust assumption:** Telemetry is best-effort and non-blocking; missing metrics cannot fail a run.
- **Concrete failure mode:** Forged or malformed metrics can create noisy budget warnings, negative totals, implausible costs, or misleading agent audit attribution.
- **Severity:** Low.
- **Recommendation:** Clamp negative values and keep telemetry out of lifecycle decisions.

## `Slice.Extra` / `State.Extra` and unknown-field preservation

- **Agent-authored input:** Unknown fields in `state.json` and `slices.json`, including nested unknown fields on known objects.
- **Consumed by:** Model structs expose `State.Extra` and `Slice.Extra`, but current round-tripping primarily relies on `internal/plan/artifact_io.go` merging new JSON into existing artifact JSON before atomic writes.
- **Current trust assumption:** Unknown fields are preserved for forward compatibility and ignored by current behavior.
- **Concrete failure mode:** Unknown fields can bloat artifacts, survive mutations indefinitely, and become surprising if future Tao versions start interpreting a previously ignored key. Because merge-by-`id` preserves unknown slice fields, a malicious or stale field can be long-lived.
- **Severity:** Low today; medium for future compatibility.
- **Recommendation:** Keep preservation, but document that unknown fields are untrusted and consider size warnings for unusually large unknown-field payloads.

## Small fixes

These are bounded validation, normalization, or ownership changes addressed from this audit.

1. **Fixed in slice 007** — `internal/cli/slice_complete.go` now caps notes and verification-results input file sizes, caps verification result count/details length, trims required result fields, and rejects result objects missing command/cwd/result fields; covered by `internal/cli/slice_complete_test.go`.
2. **Fixed in slice 007** — `internal/plan/artifact_io.go` replaced `readEvents` scanner usage with a bounded JSONL reader that reports malformed or oversized `events.jsonl` lines as warnings and skips them instead of failing plan load; covered by `internal/plan/artifact_io_test.go`.
3. **Fixed in slice 007** — `internal/run/review.go` caps review JSON block size, structured review summary length, findings count, finding field lengths, and negative finding line numbers before persisting review metadata; covered by `internal/run/review_test.go`.
4. **Fixed in slice 007** — `internal/rework/generate.go` normalizes review finding file paths and skips absolute, empty, parent-traversing, wildcard, and broad `...` paths when generating deterministic rework slices; covered by `internal/rework/generate_test.go`.
5. **Fixed in slice 007** — `internal/forge/github.go` rejects PR extraction when captured output contains multiple distinct GitHub pull request URLs while allowing repeated identical URLs; covered by `internal/forge/github_test.go`.
6. **Fixed after slice 007** — `internal/run/pull_request.go`, `internal/run/pull_request_push.go`, and `internal/forge/github.go` keep automatic `tao run --pull-request` creation behind Tao-owned push and forge commands; agents can only best-effort draft the Markdown body, with deterministic fallback.
7. **Fixed in slice 007** — `internal/plan/validate.go` now warns for unsafe `expected_files` entries with absolute paths or `..` traversal, while the existing broad-glob guardrail continues to warn for broad commit-scope patterns; covered by `internal/plan/validate_test.go`.
8. **Fixed in slice 007** — `internal/plan/telemetry.go` clamps negative token, cost, message, and tool-call metrics to zero when summarizing agent telemetry; covered by `internal/plan/telemetry_test.go`.
9. **Fixed by `tao slice-blocked`** — prompted exceptional stops now use bounded inputs and Tao-owned canonical state, slice, and event mutations, including optional `verification_command_invalid` evidence.

## Future-plan candidates

- Strengthen plan-artifact ownership against non-conforming direct writes; `slice-blocked` now owns the prompted blocked path, but writable artifacts still permit forged events.
- Make verification completion evidence Tao-owned by executing and recording verification commands outside the agent, rather than trusting agent-authored result JSON.
- Redesign review output as a stricter schema with explicit parse failures, human approval semantics, and safe rework planning.
- Add a general artifact quota/truncation policy for optional markdown, logs, review output, and unknown JSON fields.
- Introduce stronger prompt-injection boundaries for agent-authored plan text in run packets and review prompts.
- Strengthen mutation-journal ownership and artifact size limits if plan directories become writable by less-trusted processes.
