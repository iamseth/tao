---
description: Interview the user about a plan or design until decisions are clear
agent: plan
---

Interview the user about the proposed plan or design until the important decisions, constraints, risks, and open questions are clear.

Rules:
- Ask one question at a time.
- For each question, provide a recommended answer and the reason for it.
- If the answer can be determined from the codebase, inspect the codebase instead of asking.
- Follow each decision to its consequences before moving to the next branch.
- Stop when the plan is specific enough to slice or implement.

Topic or starting point:
{{ .Arguments }}
