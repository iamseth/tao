// Package workspace prepares and inspects git worktree-backed plan workspaces.
//
// It owns the mapping from a plan to an isolated worktree, dependency syncing,
// conflict detection, and cleanup. Low-level git invocations are delegated to
// internal/gitops, and commands run through the shared internal/commandrunner
// seam; this package only orchestrates them and enforces workspace invariants.
package workspace
