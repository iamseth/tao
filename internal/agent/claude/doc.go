// Package claude runs fresh non-interactive Claude Code sessions for Tao.
//
// The package owns the low-level Claude CLI contract: process startup,
// stdin prompt delivery, stream-json parsing, useful agent logging, and
// best-effort telemetry extraction. Higher-level run and planning code should
// depend on these request/result types instead of embedding Claude process
// details.
package claude
