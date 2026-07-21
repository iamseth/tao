// Package opencode runs fresh non-interactive OpenCode sessions for Tao.
//
// The package owns the low-level OpenCode CLI contract: process startup,
// stdin prompt delivery, parsing of the `opencode run --format json` event
// stream, useful agent logging, and best-effort telemetry extraction. It is a
// self-contained transport mirroring internal/agent/claude; higher-level run
// and planning code depends on these request/result types rather than
// embedding OpenCode process details.
//
// OpenCode's JSON event stream omits a model identifier and reports token
// usage per step (one step_finish event per assistant step), so metrics are
// accumulated across the stream and treated as best-effort: parse misses warn
// rather than fail a run.
package opencode
