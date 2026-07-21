// Package verification implements command-level verification analysis for plan facades.
//
// It intentionally depends on narrow value types instead of internal/plan artifact
// models so the plan package keeps ownership of on-disk schemas and lifecycle
// state while delegating shell, cwd, path, and failure-classification details.
package verification
