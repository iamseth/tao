package gitops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// CommitPathState describes the exact committed state of one path changed
// between a parent and its child. An empty mode denotes a deleted path.
type CommitPathState struct {
	Path               string
	Mode               string
	ContentFingerprint string
}

// CommitPathStates returns the exact path set, Git modes, and content for the
// delta from parent to commit. Regular-file content is represented by its
// SHA-256 digest; symlink content is represented by its exact target.
func (c Client) CommitPathStates(ctx context.Context, parent, commit string) ([]CommitPathState, error) {
	raw, err := c.rawOutput(ctx, "diff-tree", "-r", "--no-commit-id", "--no-renames", "--name-status", "-z", parent, commit)
	if err != nil {
		return nil, fmt.Errorf("list committed paths: %w", err)
	}
	changes, err := parseCommitPathChanges(raw)
	if err != nil {
		return nil, fmt.Errorf("parse committed paths: %w", err)
	}
	states := make([]CommitPathState, 0, len(changes))
	for _, change := range changes {
		if change.oldPath != "" {
			return nil, errors.New("inspect committed paths: rename remained after --no-renames")
		}
		state := CommitPathState{Path: change.path}
		if change.status == "D" {
			states = append(states, state)
			continue
		}
		entry, entryErr := c.rawOutput(ctx, "ls-tree", "-z", commit, "--", change.path)
		if entryErr != nil {
			return nil, fmt.Errorf("inspect committed path %q: %w", change.path, entryErr)
		}
		mode, objectType, objectID, entryPath, parseErr := parseExactTreeEntry(entry)
		if parseErr != nil {
			return nil, fmt.Errorf("inspect committed path %q: %w", change.path, parseErr)
		}
		if entryPath != change.path || objectType != "blob" {
			return nil, fmt.Errorf("inspect committed path %q: unsupported tree entry", change.path)
		}
		contentFingerprint, contentErr := c.commitBlobFingerprint(ctx, objectID, mode)
		if contentErr != nil {
			return nil, fmt.Errorf("read committed path %q: %w", change.path, contentErr)
		}
		state.Mode = mode
		state.ContentFingerprint = contentFingerprint
		states = append(states, state)
	}
	sort.Slice(states, func(i, j int) bool { return states[i].Path < states[j].Path })
	for i := 1; i < len(states); i++ {
		if states[i-1].Path == states[i].Path {
			return nil, fmt.Errorf("inspect committed paths: duplicate path %q", states[i].Path)
		}
	}
	return states, nil
}

const maxCommitSymlinkTargetBytes = 64 * 1024

func (c Client) commitBlobFingerprint(ctx context.Context, objectID, mode string) (string, error) {
	switch mode {
	case "100644", "100755":
		digest := sha256.New()
		if err := c.streamBlob(ctx, objectID, digest); err != nil {
			return "", err
		}
		return hex.EncodeToString(digest.Sum(nil)), nil
	case "120000":
		// Worktree symlink targets are much smaller in practice, but a crafted
		// Git object must not make exact commit inspection retain an unbounded blob.
		target := boundedWriter{limit: maxCommitSymlinkTargetBytes}
		if err := c.streamBlob(ctx, objectID, &target); err != nil {
			return "", err
		}
		if target.truncated {
			return "", fmt.Errorf("symlink target exceeds %d bytes", maxCommitSymlinkTargetBytes)
		}
		return target.String(), nil
	default:
		return "", fmt.Errorf("unsupported Git mode %s", mode)
	}
}

func (c Client) streamBlob(ctx context.Context, objectID string, destination io.Writer) error {
	const maxStderrBytes = 64 * 1024
	stderr := boundedWriter{limit: maxStderrBytes}
	args := []string{"cat-file", "blob", objectID}
	if err := c.git(ctx, args, destination, &stderr); err != nil {
		message := stderr.String()
		if stderr.truncated {
			message += "\n[git stderr truncated]"
		}
		return commandError(args, err, message)
	}
	return nil
}

func parseExactTreeEntry(raw string) (mode, objectType, objectID, path string, err error) {
	if raw == "" || !strings.HasSuffix(raw, "\x00") || strings.Count(raw, "\x00") != 1 {
		return "", "", "", "", errors.New("malformed tree entry")
	}
	metadata, path, found := strings.Cut(strings.TrimSuffix(raw, "\x00"), "\t")
	if !found || path == "" {
		return "", "", "", "", errors.New("malformed tree entry")
	}
	fields := strings.Fields(metadata)
	if len(fields) != 3 {
		return "", "", "", "", errors.New("malformed tree entry metadata")
	}
	return fields[0], fields[1], fields[2], path, nil
}
