// Package view builds shared presentation read models and render helpers.
//
// It owns display-only transformations shared by CLI and run validation output. Plan artifact loading, lifecycle rules, and validation
// semantics remain in package plan; view only combines those results with
// timestamps, optional logs, telemetry rows, and terminal-safe formatting.
package view
