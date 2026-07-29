---
description: Review a slice-complete Tao plan change set
agent: build
---

You are in REVIEW mode for a Tao plan whose implementation slices are complete.

Plan ID: `{{ .PlanID }}`
Plan directory: `{{ .PlanDir }}`
Base: `{{ .Base }}`
Head: `{{ .Head }}`

Your job is to review the plan's change set, not to implement fixes.

Treat the Prior Rework and Budget Context block as advisory history, not as steering toward approval or rejection.

## Scope

- Read the plan intent from `planning-brief.md` and/or `plan.md` in the plan directory when present.
- Read `slices.json` in the plan directory to understand the intended slice work and verification.
- Review only changes in the diff from the provided base to the provided head:

```sh
git diff --stat {{ .Base }}..{{ .Head }}
git diff {{ .Base }}..{{ .Head }}
```

- Do not report pre-existing issues unless the scoped diff introduced them, regressed them, or made them materially worse.
- Do not modify files, create commits, push branches, or update Tao metadata.

## Review criteria

Assess the scoped diff for:

- Correctness: behavior matches the plan intent and does not introduce obvious regressions.
- Scope adherence: implementation stays within the plan and completed slices.
- Test coverage: verification is meaningful for the changed behavior and important edge cases.
- Simplicity: solution is understandable, maintainable, and avoids unnecessary complexity.

## Output format

Write a concise human-readable review first. Include any important context, strengths, and risks.

Then end with exactly one fenced `tao-review-json` block containing valid JSON with this shape:

```tao-review-json
{
  "verdict": "approve",
  "summary": "One or two sentences summarizing the review result.",
  "commit_message": {
    "subject": "feat(scope): summarize the exact reviewed change",
    "body": "What:\nDescribe what the exact scoped diff changes.\n\nWhy:\nExplain why the change is needed."
  },
  "findings": [
    {
      "severity": "major",
      "file": "path/to/file.go",
      "line": 123,
      "message": "What is wrong and why it matters.",
      "suggestion": "Concrete next step, or empty string if none."
    }
  ]
}
```

Rules for the JSON block:

- `verdict` must be exactly one of `approve`, `changes_requested`, or `comment`.
- Use `changes_requested` for correctness, regression, scope, or missing-test issues that should be fixed before considering the plan done.
- Use `comment` for non-blocking risks or observations.
- Use `approve` only when there are no requested changes; use an empty `findings` array when there are no findings.
- An `approve` verdict must include `commit_message`; omit `commit_message` for `changes_requested` and `comment`.
- Derive `commit_message` from the complete exact `Base..Head` diff already reviewed. The subject must be a scoped Conventional Commit in the form `<type>(<lowercase-scope>): <lowercase-imperative-summary>`, and the summary must be at most 72 characters with no ending punctuation.
- The commit body must be non-empty canonical `What:` and `Why:` sections that explain the change and its motivation. Do not include verification output or any `Tao-*` trailers; Tao adds trusted evidence later.
- Every finding must be tied to the scoped diff and include the best available `file` and `line`; use `null` for `line` only when no specific line applies.
- Do not include Markdown comments inside the JSON block.
