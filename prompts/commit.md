---
description: Commit the current changes locally
agent: build
---

Create a local Git commit for the current repository changes.

This is a standalone manual command. Automatic `tao run` slice completion is owned by `tao slice-complete` and never falls back to this prompt.

Requirements:
- Review `git status` and `git diff` before committing.
- Review `git log --oneline -12` before choosing the message, and match the repository's recent commit style.
- Include relevant staged and unstaged changes, but do not commit `.tao/` or other local-only artifacts.
- Do not commit secrets, credentials, or generated build artifacts.
- Use a concise conventional commit message: `<type>(<scope>): <summary>`.
- Use `feat` for user-visible capabilities, `fix` for bug fixes, `refactor` for behavior-preserving code structure changes, `test` for test-only changes, `docs` for documentation-only changes, and `chore` for tooling or maintenance changes.
- Use the narrowest package or product scope, such as `web`, `cli`, `plan`, `run`, `telemetry`, `prompts`, or `docs`.
- Keep the summary imperative, lowercase after the scope, and focused on why the change exists.
- If one commit spans multiple areas, choose the user-visible scope that best explains the change.
- Do not push to a remote.
- If there are no changes to commit, report that clearly and do not create an empty commit.

Additional requirements from the user:
{{ .Arguments }}
