// Package build exposes build-time provenance read from the running binary.
//
// It owns interpretation of Go's embedded debug.BuildInfo settings into a short
// commit and a human-readable build age, falling back to "unknown" when the
// information is absent. It is read-only and depends on no other Tao package.
package build
