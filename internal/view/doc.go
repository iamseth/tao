// Package view builds shared presentation read models and render helpers.
//
// It owns display-only transformations shared by CLI and run validation output,
// including the option-driven insights report and digest renderer. Plan artifact
// loading, lifecycle rules, aggregation, and validation semantics remain in their
// domain packages; view only combines those results with timestamps, optional
// logs, telemetry rows, and terminal-safe formatting.
package view
