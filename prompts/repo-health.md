---
description: Audit repository health and maintenance risks
agent: plan
---

Audit the current git repository for repository health risks and maintenance opportunities.

This is a read-only audit. Do not edit files, delete files, rewrite history, install dependencies, run cleanup commands, or make commits unless the user explicitly asks in a later request.

Focus on:
- Repository bloat, large files, generated artifacts, vendored dependencies, caches, and build outputs that may not belong in source control.
- Duplicate or near-duplicate files, copied implementations, redundant docs, and stale examples.
- Dependency, tooling, script, and configuration sprawl.
- Inconsistent project structure, naming, test layout, docs, and ownership boundaries.
- Maintenance risks that make future AI or human changes harder to review safely.

Workflow:
- Read repository guidance first, including `AGENTS.md`, `README.md`, and relevant docs when present.
- Inspect git status before auditing so user work is not mistaken for repository health debt.
- Use targeted file and content searches before broad scans.
- Prefer repository metadata and evidence over guesses, such as file paths, sizes, repeated names, lockfiles, generated headers, script lists, or duplicated snippets.
- Do not overstate certainty. Mark uncertain findings as hypotheses and explain the missing evidence.

Output format:
- Start with findings, ordered by severity and confidence.
- For each finding include: severity, evidence, impact, recommendation, and suggested validation.
- Include a short `No Finding` section for audited areas that looked healthy when useful.
- End with a concise prioritized action list.
- If no meaningful issues are found, say that explicitly and list the checks performed.

Additional focus from the user:
{{ .Arguments }}
