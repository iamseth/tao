---
description: Create a pull request for the current branch
agent: build
---

Create a pull request for the current branch using Tao's reviewer-facing conventions. This is an agent-driven helper: do not read or mutate Tao plan lifecycle state.

Requirements:
- Inspect the current branch, working tree status, recent commits, and complete diff from the base branch.
- Determine the base branch, defaulting to `main` when the repository does not indicate another base.
- Push the branch if needed.
- Choose a title in the exact form `<type>(<scope>): <summary>`: a scoped Conventional Commit subject with a supported type (`feat`, `fix`, `docs`, `style`, `refactor`, `perf`, `test`, `build`, `ci`, `chore`, or `revert`), a narrow scope containing only lowercase letters, digits, and hyphens, and a lowercase imperative summary of at most 72 characters with no ending punctuation.
- Create a Markdown body containing exactly these level-two sections in this order: `## Problem`, `## Fix`, `## Tests`, `## Deploy`, and `## Scope`. Do not add other level-two sections.
- Explain reviewer-facing context in Problem and Fix. Keep the reviewer-authored narrative in Problem, Fix, and Deploy free of Tao plan or slice details, lifecycle state, merge guidance, and other Tao-specific planning narrative.
- In Tests, truthfully report repository test commands actually run and their results. Do not report `tao` lifecycle commands as tests. Truthful repository paths and commands may contain `tao`. If no tests were run, say so plainly.
- In Deploy, state any real deployment steps or that none are required.
- The narrative exclusion does not apply to Tests or Scope: preserve truthful repository commands in Tests and the exact unmodified diff stat in Scope, including paths or commands containing `tao`.
- Generate the exact diff stat for the complete base-to-head change with `git diff --stat <base>...HEAD` and place it in Scope using this exact structure, without editing, summarizing, or omitting its output:

      <details>
      <summary>Changed files</summary>

      ```text
      <exact diff-stat output>
      ```

      </details>
- Derive the category label from the title type: map `feat` to `feature`; keep every other supported type unchanged. Search labels case-insensitively and preserve the exact name of an existing matching label. If none exists, create the derived lowercase label with `--color 1D76DB --description "Repository change category"`.
- Create the PR with the selected label and `--assignee @me`.
- If label inspection/creation or assignment fails because repository metadata permissions are unavailable, report the failure clearly; do not imply that the missing metadata was applied. If the PR was created despite a metadata failure, still return its URL and identify the incomplete metadata.
- Return the pull request URL.

Additional requirements from the user:
{{ .Arguments }}
