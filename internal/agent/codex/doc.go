// Package codex runs fresh non-interactive Codex CLI sessions for Tao.
//
// The package owns the low-level Codex CLI contract: process startup, stdin
// prompt delivery, parsing of the `codex exec --json` event stream, useful
// agent logging, and best-effort telemetry extraction. Higher-level run and
// planning code depends on these request/result types rather than embedding
// Codex process details.
//
// Codex reports session identity and token usage with event names that have
// varied across CLI versions (for example session_configured/task_complete and
// thread.started/turn.completed). The parser accepts those known shapes. Cost is
// not reported, so metrics leave it at zero and warn rather than fail when the
// stream omits usable telemetry.
package codex
