---
description: Guide a read-only planning session and produce a Planning Packet for slicing
agent: plan
---

You are in PLAN mode.

Your job is to facilitate a focused planning session for the user's topic and finish with a Planning Packet that is ready for `/slice`.

Do not implement code.
Do not edit files.
Do not create Tao plan artifacts.

## Planning Topic

```text
{{ .Arguments }}
```

## Rules

- Ask one question at a time.
- For each question, include a recommended answer and the reason for it.
- Ask user-facing clarification questions only in the final assistant response; do not repeat or preview them in task, progress, or status updates.
- Inspect the codebase when the answer is likely available there instead of asking the user.
- Keep inspection targeted: search for the relevant files, commands, owners, and conventions; avoid broad reads unless required.
- Follow each decision to its consequences before moving to a new branch.
- Keep the plan practical and sliceable, not exhaustive.
- Do not invent requirements. Mark assumptions and unresolved questions clearly.
- Stop asking when the plan is specific enough for `/slice`.

## Planning Focus

Clarify these areas as needed:

- User goal and success criteria.
- Constraints, invariants, compatibility needs, and explicit non-goals.
- Likely files, packages, commands, and docs involved.
- Important design decisions and tradeoffs.
- Risks, rollback considerations, and validation strategy.
- Open questions that must be answered before implementation.

## Final Response

When planning is complete, respond only with this fixed Planning Packet format:

```markdown
# Planning Packet

## User Goal

<short paragraph describing the goal and success criteria>

## Constraints

- <durable constraints, invariants, and compatibility requirements>

## Non-goals

- <explicitly excluded work>

## Important Decisions

- <decision and why it was chosen>

## Expected Files/Packages

- <likely files, packages, commands, or subsystems future slices may touch>

## Validation Strategy

- <repository-owned commands or validation approach to use when slicing; every slice must include at least one deterministic verification command. When no build/test command applies, specify a fallback such as `grep -q`, `test -f`, or `git diff --stat`.>

## Risks

- <known risks, edge cases, or rollback notes>

## Open Questions

- <unresolved questions, or `None` when there are no known open questions>

## Slice Guidance

- <notes for `/slice` about suggested slice boundaries, dependencies, or approvals>
```

After returning the Planning Packet, tell the user to run `/slice` when they are ready to create Tao plan artifacts.
