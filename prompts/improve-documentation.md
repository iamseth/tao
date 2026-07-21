---
description: Find documentation improvement opportunities
agent: plan
---
Review the repository's documentation for improvement opportunities, covering both prose docs and code-level documentation.

Focus on:

- Accuracy and drift: docs that contradict the code, such as stale commands, wrong file paths, and outdated examples.
- Completeness, bounded by audience: whether the intended reader can finish their job — for a README, get oriented and started; for the usage guide, decide when and how. Judge against that reader's needs, not an absolute bar.
- Scope and fit: content sitting in the wrong doc for its audience, or exceeding what its reader needs. Over-inclusion is a defect, not just omission. A README that has grown into a manual, or detail that belongs in usage-guide.md or plan-format.md, should be flagged for relocation or cutting.
- Staleness: references to removed features, old versions, or abandoned workflows.
- Structure and discoverability: organization, duplication across README, AGENTS, and other docs, and broken cross-references or links.
- Clarity and the "why": explanations that omit intent, rationale, or the reasoning a reader needs.
- Code-level doc coverage and comment quality, matching the repository's existing documentation conventions.

Process:

- First, identify the intended reader and the job of each documentation surface before auditing it. Read the Documentation Boundaries section of AGENTS.md, which already defines these: README is the user-facing front door (install, workflow, commands, links out); docs/usage-guide.md is workflow judgment (when and how); docs/plan-format.md is the artifact contract; AGENTS.md is agent-facing; code comments serve maintainers. Evaluate every doc against its own reader, not a generic standard.
- Read relevant repository guidance, domain docs, and existing documentation before making recommendations.
- Explore the repository before proposing changes.
- Present a numbered list of opportunities.
- For each opportunity, include files, the specific reader it serves, problem, proposed change, benefits, risks, and suggested verification. Naming the reader keeps changes reader-driven and filters out additions that serve no one in particular.
- End with a final top-5 prioritization table for the most important issues. Include columns for issue, evidence or files, risk estimate, impact estimate, complexity estimate, and recommended priority or next step.
- Do not edit files or implement changes unless the user explicitly asks.
- Ask which opportunity the user wants to explore next.

Additional focus from the user:

{{ .Arguments }}
