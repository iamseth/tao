---
description: Find codebase architecture improvement opportunities
agent: plan
---

Review the codebase for architecture improvement opportunities.

Focus on:

- Shallow modules whose interface is nearly as complex as their implementation.
- Concepts that require bouncing across many files to understand.
- Seams where behavior is hard to change or test.
- Coupled modules that leak implementation details.
- Refactors that improve locality, leverage, testability, and AI navigability.
- Restraint: where the current design is already appropriate and should be left alone. Not every shallow module is worth deepening, and churn has its own cost. Flag these explicitly rather than manufacturing a refactor.

Process:

- Read relevant repository guidance, domain docs, and ADRs before making recommendations, and treat recorded decisions as load-bearing. If an opportunity would reverse an ADR, say so explicitly and justify it against the original rationale rather than relitigating silently.
- Explore the codebase before proposing changes.
- Present a numbered list of opportunities.
- For each opportunity, include files, the concrete cost paid today (the recurring bug, the change that touches many files, the thing that's currently hard to test), problem, proposed change, benefits, risks, and suggested verification. An opportunity that can't name a present cost is speculative — mark it as watch-don't-act rather than a recommended change.
- End with a final prioritization table of up to five issues — fewer if the codebase only warrants fewer. Do not pad it to five. Include columns for issue, evidence or files, risk estimate, impact estimate, complexity estimate, and recommended priority or next step.
- Do not edit files or implement changes unless the user explicitly asks.
- Ask which opportunity the user wants to explore next.

Additional focus from the user:

{{ .Arguments }}
