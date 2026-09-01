// Package insights aggregates advisory operational evidence from Tao plan
// history. Collection is deterministic, cancellable, best-effort across damaged
// or missing sources, and read-only: insight results never mutate plans or grant
// lifecycle or recovery authority.
//
// Structured history may span all selected repositories. Recent agent-log
// analysis is separately limited to a 30-day window and bounded candidates,
// bytes, lines, and signals. Log-derived evidence is normalized, redacted, and
// retained only as short bounded exemplars rather than raw provider output.
package insights
