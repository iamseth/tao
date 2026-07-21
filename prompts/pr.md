---
description: Create a pull request for the current branch
agent: build
---

Create a pull request for the current branch.

Requirements:
- Inspect the current branch, working tree status, recent commits, and diff from the base branch.
- Determine the base branch, defaulting to `main` when the repository does not indicate another base.
- Push the branch if needed.
- Create a concise pull request with a Markdown description.
- Include summary, motivation, scope, testing performed, risks, and rollback notes when relevant.
- Return the pull request URL.

Additional requirements from the user:
{{ .Arguments }}
