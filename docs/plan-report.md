# Plan report format

Tao plan reports are internal coworker snapshots rendered as UTF-8 Markdown. The schema identifier is `tao.plan-report.v1`. They are intended for access-controlled sharing with coworkers who already have repository access. Reports are synthesized from a sanitized allowlist; they are not raw plan exports and are not a complete sensitive-data detection mechanism.

## Modes and section contracts

The `full` mode uses these fixed sections, in order:

1. Executive Summary
2. Planning Context
3. Slice Overview
4. Execution Summary
5. Review and Outcome
6. Redactions and Omissions

It includes plan and slice lifecycle status, aggregate durations and verification results, aggregate agent metrics when recorded, review status and summary, finding count, and merged outcome. It does not include commands, working directories, logs, events, session or provider identifiers, commit evidence, or raw review findings.

The `planning-only` mode uses these fixed sections, in order:

1. Executive Summary
2. Planning Context
3. Planned Slices
4. Redactions and Omissions

Planning-only reports carry an explicit notice that they are synthesized and non-verbatim. Their projection has no execution, review, rework, event, or telemetry fields. Generated rework slices are excluded. Consequently, generating at the same time from unchanged planning inputs produces the same report regardless of later execution state.

Headings and field labels are schema-owned, never source-controlled. Dynamic content is emitted only as escaped text in paragraphs and bullets. The format intentionally excludes raw HTML, links, images, tables, ANSI control sequences, embedded assets, and user-controlled headings.

## Planning source precedence

Known sections in `planning-brief.md` take precedence over corresponding known sections in the plan narrative. The renderer recognizes user goal, constraints, non-goals, decisions, risks, and open questions; it does not pass through unknown Markdown sections. When no narrative goal is available, the first original planned slice goal is used. Structured global invariants and open questions are fallbacks for absent narrative constraints and questions. Slice dependencies are presented by original slice title when available.

This extraction is synthesized. It does not retain a prompt, planning conversation, or planning-session transcript.

## Missing data and disclosures

Missing optional prose is shown as `Unavailable`; absent collections and optional measurements are shown as `None recorded` or `Not recorded`. Missing best-effort metrics are not inferred as zero. Numeric zero is shown only when the projection records a measurement or aggregate.

Redactions and Omissions contains stable aggregate counts by report section and transformation category. It never discloses source values. Recognized credentials and common personal identifiers are redacted or omitted; malformed text may be normalized and long values truncated. Ordinary URLs and filesystem paths are preserved because the audience already has repository access, while credential-bearing URLs remain redacted. A final fail-closed scan rejects residual high-confidence credentials, secrets, and personal identifiers before bytes are returned.

## Safety limitations

The report is designed to reduce accidental disclosure within the internal coworker boundary, not to prove that prose is non-sensitive or to create a public export. Repository URLs, filesystem paths, context-specific confidential names, business facts, and unrecognized identifier formats can remain. Review a report before sharing it, use an appropriate access-controlled destination, and do not treat the report as a substitute for organizational data-loss-prevention policy.

## PDF conversion

The Markdown subset is converter-neutral and uses narrow linear layouts. Convert the saved report with an offline tool that does not fetch remote resources, enable raw HTML, resolve local links, or embed external assets. Keep network access disabled where practical. Tao emits Markdown only and does not invoke or endorse a particular PDF converter.
