// Package plandelta owns read-only, bounded, terminal-safe, cancellable plan
// change presentation. Its projections never confer lifecycle, approval,
// recovery, or merge authority and must not mutate Git or plan artifacts.
// Project dependencies are limited to plan, workspace, gitops, commandrunner,
// and agentinput; run, merge, tui, and cli must remain outside this package.
// Snapshot and diff paths are exact lookup keys, never display-ready labels.
// Diff lines and failure reasons are sanitized here, outside rendering. Untracked
// reads use root-confined, limited presentation reads rather than the rejecting
// agentinput.ReadBoundedFile contract; no filesystem or Git mutation is allowed.
package plandelta
