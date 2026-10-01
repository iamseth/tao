package plandelta

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/iamseth/tao/internal/gitops"
)

// ParseUnifiedDiff classifies raw patch records before terminal sanitization.
// Preamble suppression stops at hunks so +++/--- payload is never hidden.
func ParseUnifiedDiff(text string, truncated bool) FileDiff {
	return parseUnifiedDiff(text, truncated, MaxFileDiffLines)
}

func parseUnifiedDiff(text string, truncated bool, maxLines int) FileDiff {
	if len(text) > MaxFileDiffBytes {
		text = text[:MaxFileDiffBytes]
		truncated = true
	}
	d := FileDiff{Truncated: truncated}
	inHunk, preamble, plusHeader := false, false, false
	for text != "" {
		raw, rest, _ := strings.Cut(text, "\n")
		text = rest
		kind := Meta
		switch {
		case strings.HasPrefix(raw, "diff --git "):
			inHunk, preamble = false, true
			continue
		case !inHunk && preamble && strings.HasPrefix(raw, "index "):
			continue
		case !inHunk && strings.HasPrefix(raw, "--- ") && strings.HasPrefix(rest, "+++ "):
			plusHeader = true
			continue
		case !inHunk && plusHeader && strings.HasPrefix(raw, "+++ "):
			plusHeader = false
			continue
		case strings.HasPrefix(raw, "@@ ") || strings.HasPrefix(raw, "@@@ "):
			inHunk = true
			kind = Hunk
		case inHunk && strings.HasPrefix(raw, "+"):
			kind = Add
		case inHunk && strings.HasPrefix(raw, "-"):
			kind = Del
		case inHunk && strings.HasPrefix(raw, " "):
			kind = Context
		case strings.HasPrefix(raw, "\\ No newline at end of file"):
			kind = NoNewline
		case strings.HasPrefix(raw, "Binary files ") || raw == "GIT binary patch":
			d.Binary = true
		}
		if len(d.Lines) == maxLines {
			d.Truncated = true
			d.Lines = append(d.Lines, Line{Marker, "[showing first 4000 lines]"})
			break
		}
		if kind == Hunk {
			d.Hunks = append(d.Hunks, len(d.Lines))
		}
		d.Lines = append(d.Lines, Line{kind, SanitizeLine(raw)})
	}
	if truncated {
		d.Lines = append(d.Lines, Line{Marker, "[diff truncated at 512 KiB]"})
	}
	return d
}

func validatePath(path string) error {
	if path == "" || !filepath.IsLocal(path) || strings.ContainsRune(path, 0) {
		return errors.New("invalid repository-relative path")
	}
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." || part == "." || part == "" {
			return errors.New("invalid repository-relative path component")
		}
	}
	if path == ".git" || strings.HasPrefix(path, ".git/") || gitops.IsTaoMetadataPath(path) {
		return errors.New("repository metadata is not displayable")
	}
	return nil
}

func (c Collector) FileDiff(ctx context.Context, snapshot Snapshot, path string) (FileDiff, error) {
	d := FileDiff{Path: path, Signature: snapshot.Signature}
	if err := ctx.Err(); err != nil {
		return d, err
	}
	var selected *FileChange
	for i := range snapshot.Files {
		if snapshot.Files[i].Path == path {
			selected = &snapshot.Files[i]
			break
		}
	}
	if selected == nil {
		return d, errors.New("selected path is not in snapshot")
	}
	if err := validatePath(path); err != nil {
		return d, err
	}
	root := snapshot.Target.RepoRoot
	args := []string{snapshot.Base.SHA}
	switch snapshot.Scope {
	case ScopeBranch:
		args = append(args, snapshot.Head)
	case ScopeWorktree:
		root = snapshot.Target.WorktreePath
		if selected.RevertsCommitted {
			args = []string{"HEAD"}
		}
	default:
		return d, errors.New("invalid changes scope")
	}
	if !filepath.IsAbs(root) {
		return d, errors.New("changes root must be absolute")
	}
	var err error
	if selected.Untracked {
		if snapshot.Scope != ScopeWorktree {
			return d, errors.New("untracked files require worktree scope")
		}
		d, err = untrackedDiff(ctx, root, path)
	} else {
		for _, ref := range args {
			if ref == "" || strings.HasPrefix(ref, "-") || strings.ContainsAny(ref, "\x00\r\n") {
				return d, errors.New("invalid diff revision")
			}
		}
		var text string
		var truncated bool
		text, truncated, err = gitops.NewReadOnlyClient(root, c.Runner).DiffBoundedArgs(ctx, MaxFileDiffBytes, append(args, "--", path)...)
		if err == nil {
			limit := MaxFileDiffLines
			revert := selected.RevertsCommitted && snapshot.Scope == ScopeWorktree
			if revert {
				limit-- // Reserve one visible line for the comparison explanation.
			}
			d = parseUnifiedDiff(text, truncated, limit)
			if revert {
				d.Lines = append([]Line{{Meta, "Uncommitted changes relative to HEAD revert committed changes."}}, d.Lines...)
				for i := range d.Hunks {
					d.Hunks[i]++
				}
			}
		}
	}
	d.Path, d.Signature = path, snapshot.Signature
	if ctx.Err() != nil {
		return d, ctx.Err()
	}
	if errors.Is(err, context.Canceled) {
		return d, context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return d, context.DeadlineExceeded
	}
	if err != nil {
		return d, fmt.Errorf("%s", sanitizeReason(err.Error()))
	}
	return d, nil
}
