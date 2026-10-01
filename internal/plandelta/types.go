package plandelta

import "time"

type Scope string

const (
	ScopeBranch   Scope = "branch"
	ScopeWorktree Scope = "worktree"
)

type Availability string

const (
	AvailabilityReady            Availability = "ready"
	AvailabilityNoRepoRoot       Availability = "no_repo_root"
	AvailabilityRepoInaccessible Availability = "repo_inaccessible"
	AvailabilityNoBranch         Availability = "no_branch"
	AvailabilityNoBase           Availability = "no_base"
	AvailabilityGitError         Availability = "git_error"
)

// Paths retain exact repository bytes for lookup, not terminal display.
type FileChange struct {
	Path, OldPath                                    string
	Status                                           byte
	Added, Deleted                                   int
	Binary, Untracked, Uncommitted, RevertsCommitted bool
}

type Stat struct{ Files, Added, Deleted int }

type ReviewParity struct {
	Recorded                             bool
	Verdict, Base, Head                  string
	BaseMatches, HeadMatches, Superseded bool
}

// Snapshot is advisory presentation, never execution or approval evidence.
type Snapshot struct {
	Target              Target
	Scope               Scope
	Base                Base
	Head, DefaultHead   string
	WorktreeMissing     bool
	ActiveOperation     string
	RebaseIntent, Dirty bool
	Merged              *bool
	Review              ReviewParity
	Files               []FileChange
	FilesTruncated      bool
	Total, Uncommitted  Stat
	UntrackedCount      int
	Signature           string
	Warnings            []string
	Availability        Availability
	Reason              string
	CollectedAt         time.Time
}

type LineKind uint8

const (
	Context LineKind = iota
	Add
	Del
	Hunk
	FileHeader
	Meta
	NoNewline
	Marker
)

type Line struct {
	Kind LineKind
	Text string
}

// Path is the raw lookup key; Lines contain terminal-safe display text.
type FileDiff struct {
	Path, Signature   string
	Binary, Truncated bool
	Lines             []Line
	Hunks             []int
}
