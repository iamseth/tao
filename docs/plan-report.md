# Plan report format

Tao plan reports are internal coworker snapshots rendered as UTF-8 Markdown. The schema identifier is `tao.plan-report.v1`. They are intended for access-controlled sharing with coworkers who already have repository access. Reports are synthesized from a sanitized allowlist; they are not raw plan exports and are not a complete sensitive-data detection mechanism.

## Frontmatter and document shape

Every report starts with YAML frontmatter containing `schema`, `mode`, `snapshot`, `plan`, `plan-id`, and `status` values, safely quoting values when YAML requires it, followed by an H1 containing the sanitized plan title. `mode` is `full` or `planning-only`. A full report uses the known plan lifecycle status; a planning-only report always uses `planned`, because lifecycle state is outside that projection. The snapshot is UTC RFC 3339, or `Unavailable` when absent.

The `full` mode uses these fixed top-level sections, in order:

1. Planning Context
2. Implementation
3. Implementation Summary
4. Review and Outcome
5. Redactions and Omissions

Planning Context contains Goal, Constraints, Non-goals, Decisions, Risks, and Open Questions. Implementation contains one `Slice N: <title>` subsection per slice. Each slice starts with five inline-code values for status, planned/rework kind, total tokens, commit, and duration, followed by Goal, Rationale, and Dependencies subsections. Implementation Summary starts with an inline-code summary of duration, completed/total slices, passed/total slice verifications, and reported cost, then uses bold `Verification`, `Execution`, and `Tokens` labels with compact bullet groups. Review and Outcome starts with inline-code review status, verdict, finding count, and merged state, followed by the sanitized review summary; it never includes raw findings.

Lifecycle status and merged outcome are intentionally distinct. A qualifying PR plan is `completed` once its current approved review and recorded PR have the same non-empty head, but reports render the outcome as `not merged` until a current `plan_merged` event exists. A PR plan with that event renders `merged`. For compatibility, a legacy persisted `completed` plan with neither current qualifying PR evidence nor any merge-event history retains the historical inferred `merged` outcome; this inference does not create merge evidence or authorize merge-specific behavior.

The `planning-only` mode uses these fixed top-level sections, in order:

1. Planning Context
2. Planned Slices
3. Redactions and Omissions

Each planned slice is a flat group of `Slice N`, Goal, Rationale, and Dependencies bullets; slice titles never become headings in this mode. Planning-only reports exclude **all slice execution information**, including lifecycle status, rework slices, durations, verification, commits, events, execution telemetry, reviews, and outcomes. The planning-only projection is a separate type rather than a filtered full report, so unchanged planning inputs render independently of later execution state.

Headings and field labels are schema-owned, never source-controlled. Dynamic plan and slice titles are sanitized before they become headings. Other dynamic content is emitted only as escaped text in paragraphs, numbered lists, and bullets. The format intentionally excludes raw HTML, links, images, tables, ANSI control sequences, embedded assets, and user-controlled heading structure.

## Planning context and legacy effort

Known sections in `planning-brief.md` take precedence over corresponding known sections in the plan narrative. The projection recognizes user goal, constraints, non-goals, decisions, risks, and open questions; it does not pass through unknown Markdown sections. When no narrative goal is available, the first original planned slice goal is used. Structured global invariants and open questions are fallbacks for absent narrative constraints and questions. Slice dependencies are presented by original slice title when available.

Planning Effort is rendered only when valid aggregate duration, total-token, and message measurements are already present in legacy planning-session statistics. Invalid, partial, or absent legacy statistics omit the entire subsection. Agent, provider, model, session, prompt, cost, and tool-call details are never projected. Tao does not restore or create planning-session capture, so newly created plans do not gain these metrics; the optional subsection exists only to report valid historical aggregates safely.

## Execution measurements

A full report's per-slice Total tokens value sums recorded total-token measurements from all agent-metrics events attributed by that slice's exact ID, including retries and failed attempts. Prefix matches and unknown slice IDs are not attributed. A measured total of zero is displayed as `0`; no measurement is displayed as `Not recorded`.

Commit values describe completion outcomes rather than merely echoing a stored SHA. A valid `committed` outcome displays the first seven hexadecimal characters of its recorded commit SHA. `no_changes` displays `No changes`, and `manual_uncommitted` displays `Manual uncommitted`; neither is presented as a created commit even if malformed historical metadata contains a SHA. Missing, invalid, legacy, or unknown commit evidence is `Not recorded`. Full commit SHAs and commit messages are excluded.

Aggregate execution telemetry is best-effort and includes only counts, token totals, messages, tool calls, and reported cost. It excludes agent, provider, model, session, event, and slice identities. Full reports also exclude commands, working directories, logs, prompt text, and raw plan artifacts.

## Missing data and disclosures

Missing optional prose is shown as `Unavailable`; absent collections are `None recorded`; absent optional measurements are `Not recorded`. Missing measurements are never inferred as zero. Numeric zero is shown only when the projection has an actual measurement or computes a schema-defined count.

Redactions and Omissions contains stable aggregate counts grouped by report section and transformation category. It never discloses source values. Recognized credentials and common personal identifiers are redacted or omitted; malformed text may be normalized and long values truncated. Ordinary URLs and filesystem paths are preserved because the audience already has repository access, while credential-bearing URLs remain redacted. A final fail-closed scan rejects residual high-confidence credentials, secrets, and personal identifiers before bytes are returned.

## Safety limitations

The report is designed to reduce accidental disclosure within the internal coworker boundary, not to prove that prose is non-sensitive or to create a public export. Repository URLs, filesystem paths, context-specific confidential names, business facts, and unrecognized identifier formats can remain. Review a report before sharing it, use an appropriate access-controlled destination, and do not treat the report as a substitute for organizational data-loss-prevention policy.

## PDF conversion

The Markdown subset is converter-neutral and uses narrow linear layouts. Convert the saved report with an offline tool that does not fetch remote resources, enable raw HTML, resolve local links, or embed external assets. Keep network access disabled where practical. Tao emits Markdown only and does not invoke or endorse a particular PDF converter.
