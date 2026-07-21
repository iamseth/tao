// Package cli owns the `tao` command-line surface: argument parsing, the command
// registry, and rendering of human- and machine-readable output.
//
// It is the entry layer that translates flags and stdin into calls on the plan,
// run, and prompt-install packages; it holds no plan lifecycle or persistence
// logic of its own. Durable state lives behind those packages, and shared
// read-model formatting belongs to internal/view rather than here.
package cli
