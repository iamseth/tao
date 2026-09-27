// Package insights aggregates advisory operational evidence from Tao plan
// history. Collection is deterministic, cancellable, best-effort across damaged
// or missing sources, and read-only: insight results never mutate plans or grant
// lifecycle or recovery authority.
//
// Scorecard observations are derived per plan at read time with an injectable
// clock and never grant authority. Their terminal-or-window maturity is broader
// than routing evidence's terminal-only maturity; these definitions are separate.
// Efficiency samples require a recorded measurement for each metric, including
// planning and per-original-slice metrics. Explicit zeros count; missing values
// do not. Totals sum recorded usage, not estimated complete usage: presence means
// at least one measurement, and availability never synthesizes a measurement.
// Session samples require metrics events; tool-call presence is retained at read
// time because the shared metrics model has presence flags only for tokens/cost.
//
// Structured history may span all selected repositories. Recent agent-log
// analysis is separately limited to a 30-day window and bounded candidates,
// bytes, lines, and signals. Log-derived evidence is normalized, redacted, and
// retained only as short bounded exemplars rather than raw provider output.
package insights
