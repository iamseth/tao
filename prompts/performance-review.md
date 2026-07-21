---
description: Review Tao planning and run performance patterns
agent: build
---

You are reviewing Tao's own planning and execution performance. Do not modify files.

Goal: identify recurring process problems and concrete improvements, such as slices that are too large, plans that should have been split into multiple plans, timeout-prone agent/model choices, repeated review failures, commit/review/merge lifecycle gaps, and verification bottlenecks.

## Suggested data to inspect

Run read-only commands as needed:

```sh
tao status
tao list --limit 50
find "${TAO_DATA_HOME:-${XDG_DATA_HOME:-$HOME/.local/share}/tao}" -path '*/plans/*/events.jsonl' -print
```

For suspicious plans, inspect `state.json`, `slices.json`, `planning-brief.md`, and `events.jsonl`. Prefer aggregate patterns over isolated anecdotes.

## Questions to answer

- Which plans or slices exceeded the normal session timeout, required repeated attempts, or needed manual intervention?
- Are planners creating slices that are too broad, cross too many packages, or require broad verification too early?
- Are related but independently mergeable themes being bundled into one plan?
- Which agent/model combinations complete runs, reviews, and commits efficiently, and which correlate with failures or high cost?
- Are plan statuses, review outcomes, or merge events inconsistent with the actual lifecycle?
- What changes to prompts, slicing policy, verification selection, or Tao code would reduce future failures?

## Output

Return a concise report with:

1. Executive summary.
2. Evidence-backed patterns, including plan IDs and event timestamps where useful.
3. Recommended fixes, split into prompt/process changes and code/tooling changes.
4. Optional candidate follow-up plan topics.

Do not include secrets or full logs; quote only the minimal evidence needed.
