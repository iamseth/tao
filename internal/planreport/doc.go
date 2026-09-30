// Package planreport owns share-safe report projection, sanitization, and
// Markdown rendering. Callers render only its explicit safe projection, never
// raw plan artifacts.
//
// Planning-only reports contain no prompt capture or execution-derived data.
// A final fail-closed scan rejects residual credentials and identifiers.
package planreport
