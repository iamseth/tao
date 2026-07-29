---
description: Distill the current session into a Tao follow-up note
agent: build
---

Distill the current session into a durable follow-up note. Distill rather than transcribe the conversation.

The first line must be a one-line title. Follow it with a self-contained body that records:

- the problem and motivation, including concrete evidence available in the session such as file paths, commits, or exact error text;
- the goal;
- a requirements sketch; and
- what is out of scope.

Write for someone reading the note months later without access to this conversation. Never reduce the note to a one-liner when the session contains richer evidence.

Create the note by piping the distilled text to `tao note create` with a quoted heredoc. Add one or more `--tag` flags when an obvious topic tag applies:

```sh
cat <<'TAO_NOTE' | tao note create [--tag <topic>]
<one-line title>

<self-contained body>
TAO_NOTE
```

Report the resulting `Created note <id>` output and tell the user that the note can be promoted later with `tao note plan <id>`.

If creation fails because the repository is not registered, surface the error verbatim and suggest running `tao init`. Do not work around the error.

This is capture only. Do not promote the note, create plans, edit code, or run any other commands.

Topic or focus from the user (may be empty; if empty, capture the session's main follow-up):
{{ .Arguments }}
