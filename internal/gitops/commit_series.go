package gitops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// CommitSeriesFingerprintVersion identifies the canonical commit-series proof
// format. The digest is prefixed so persisted evidence cannot be interpreted
// using a future algorithm accidentally.
const CommitSeriesFingerprintVersion = "v5:sha256:"

// CommitSeriesProof binds an exact ordered, linear feature-commit range.
type CommitSeriesProof struct {
	Count       int
	Fingerprint string
}

// CommitSeriesProof computes deterministic evidence for ordered base..head.
// It binds each commit's author identity/date, encoding, full message, and
// content delta with content-relative edit locations. Parent and committer
// fields are deliberately excluded because an ordinary rebase rewrites them.
func (c Client) CommitSeriesProof(ctx context.Context, base, head string) (CommitSeriesProof, error) {
	return c.commitSeriesProof(ctx, base, head, nil)
}

// ProveRebaseReplay runs the planned exact-range rebase in an isolated clone
// and requires it to preserve the recorded commit-series proof. This keeps
// Git's patch-equivalence and empty-commit behavior from being discovered only
// after the live worktree branch has been rewritten.
func (c Client) ProveRebaseReplay(ctx context.Context, oldBase, oldHead, newBase string, expected CommitSeriesProof) error {
	oldBaseSHA, err := c.output(ctx, "rev-parse", "--verify", oldBase+"^{commit}")
	if err != nil {
		return fmt.Errorf("resolve recorded old rebase base %q: %w", oldBase, err)
	}
	oldHeadSHA, err := c.output(ctx, "rev-parse", "--verify", oldHead+"^{commit}")
	if err != nil {
		return fmt.Errorf("resolve recorded old rebase head %q: %w", oldHead, err)
	}
	newBaseSHA, err := c.output(ctx, "rev-parse", "--verify", newBase+"^{commit}")
	if err != nil {
		return fmt.Errorf("resolve planned new rebase base %q: %w", newBase, err)
	}
	baseAdvances, err := c.IsAncestor(ctx, oldBaseSHA, newBaseSHA)
	if err != nil {
		return fmt.Errorf("check whether planned rebase base advances recorded base: %w", err)
	}
	if !baseAdvances {
		return fmt.Errorf("unsupported rebase: planned new base is not a descendant of the recorded old base")
	}

	tempRoot, err := os.MkdirTemp("", "tao-rebase-proof-")
	if err != nil {
		return fmt.Errorf("create isolated rebase proof directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(tempRoot) }()
	cloneRoot := filepath.Join(tempRoot, "repo")
	if err := c.runAt(ctx, c.repoRoot, "clone", "--quiet", "--shared", "--no-checkout", "--", c.repoRoot, cloneRoot); err != nil {
		return fmt.Errorf("create isolated rebase proof clone: %w", err)
	}
	proofClient := NewClient(cloneRoot, c.runner)
	// A clone does not inherit repository-local user identity. Give this
	// disposable proof clone its own identity so replay depends only on the
	// commit series, not on global Git configuration.
	if err := proofClient.run(ctx, "config", "--local", "user.name", "Tao Rebase Proof"); err != nil {
		return fmt.Errorf("configure isolated rebase proof committer name: %w", err)
	}
	if err := proofClient.run(ctx, "config", "--local", "user.email", "tao-rebase-proof@example.invalid"); err != nil {
		return fmt.Errorf("configure isolated rebase proof committer email: %w", err)
	}
	if err := proofClient.run(ctx, "checkout", "--quiet", "--detach", oldHeadSHA); err != nil {
		return fmt.Errorf("check out recorded head in isolated rebase proof: %w", err)
	}
	if err := proofClient.RebaseWorktree(ctx, cloneRoot, newBaseSHA, oldBaseSHA); err != nil {
		return fmt.Errorf("unsupported rebase: planned exact commit replay does not complete without intervention (a commit may be upstream-equivalent, become empty, or conflict): %w", err)
	}
	status, err := proofClient.WorktreeStatus(ctx, cloneRoot)
	if err != nil {
		return fmt.Errorf("inspect isolated rebased head: %w", err)
	}
	if status.Dirty {
		return fmt.Errorf("unsupported rebase: planned exact commit replay leaves a dirty worktree")
	}
	actual, err := proofClient.CommitSeriesRebaseProof(ctx, oldBaseSHA, newBaseSHA, newBaseSHA, status.HEAD)
	if err != nil {
		return fmt.Errorf("prove isolated rebased commit series: %w", err)
	}
	if actual.Count != expected.Count || actual.Fingerprint != expected.Fingerprint {
		return fmt.Errorf("unsupported rebase: planned replay changes the recorded commit series (recorded commits %d, replayed commits %d); a commit may be upstream-equivalent or become empty", expected.Count, actual.Count)
	}
	return nil
}

// CommitSeriesRebaseProof computes a commit-series proof that canonicalizes
// upstream-renamed paths to their destinations. seriesBase must resolve to
// either oldBase or newBase, allowing proofs from both sides of a rebase to be
// compared without discarding file identity.
func (c Client) CommitSeriesRebaseProof(ctx context.Context, oldBase, newBase, seriesBase, head string) (CommitSeriesProof, error) {
	oldBaseSHA, err := c.output(ctx, "rev-parse", "--verify", oldBase+"^{commit}")
	if err != nil {
		return CommitSeriesProof{}, fmt.Errorf("resolve pre-rebase base %q: %w", oldBase, err)
	}
	newBaseSHA, err := c.output(ctx, "rev-parse", "--verify", newBase+"^{commit}")
	if err != nil {
		return CommitSeriesProof{}, fmt.Errorf("resolve post-rebase base %q: %w", newBase, err)
	}
	seriesBaseSHA, err := c.output(ctx, "rev-parse", "--verify", seriesBase+"^{commit}")
	if err != nil {
		return CommitSeriesProof{}, fmt.Errorf("resolve commit-series base %q: %w", seriesBase, err)
	}
	var aliases map[string]string
	switch seriesBaseSHA {
	case oldBaseSHA:
		aliases, err = c.upstreamRenameAliases(ctx, oldBaseSHA, newBaseSHA)
		if err != nil {
			return CommitSeriesProof{}, err
		}
	case newBaseSHA:
		// The rewritten series already uses destination paths.
	default:
		return CommitSeriesProof{}, fmt.Errorf("unsupported commit series: base is neither the recorded old nor new rebase base")
	}
	return c.commitSeriesProof(ctx, seriesBaseSHA, head, aliases)
}

func (c Client) commitSeriesProof(ctx context.Context, base, head string, pathAliases map[string]string) (CommitSeriesProof, error) {
	baseSHA, err := c.output(ctx, "rev-parse", "--verify", base+"^{commit}")
	if err != nil {
		return CommitSeriesProof{}, fmt.Errorf("resolve commit-series base %q: %w", base, err)
	}
	headSHA, err := c.output(ctx, "rev-parse", "--verify", head+"^{commit}")
	if err != nil {
		return CommitSeriesProof{}, fmt.Errorf("resolve commit-series head %q: %w", head, err)
	}
	ancestor, err := c.IsAncestor(ctx, baseSHA, headSHA)
	if err != nil {
		return CommitSeriesProof{}, fmt.Errorf("check commit-series ancestry: %w", err)
	}
	if !ancestor {
		return CommitSeriesProof{}, fmt.Errorf("unsupported commit series: base is not an ancestor of head")
	}

	listing, err := c.output(ctx, "rev-list", "--reverse", "--topo-order", "--parents", baseSHA+".."+headSHA)
	if err != nil {
		return CommitSeriesProof{}, fmt.Errorf("list commit series: %w", err)
	}
	var commits []string
	expectedParent := baseSHA
	if listing != "" {
		for line := range strings.SplitSeq(listing, "\n") {
			fields := strings.Fields(line)
			if len(fields) != 2 || fields[1] != expectedParent {
				return CommitSeriesProof{}, fmt.Errorf("unsupported commit series: range is not a single linear first-parent chain")
			}
			commits = append(commits, fields[0])
			expectedParent = fields[0]
		}
	}
	if expectedParent != headSHA {
		return CommitSeriesProof{}, fmt.Errorf("unsupported commit series: ordered range does not end at head")
	}

	digest := sha256.New()
	writeSeriesPart(digest, []byte("tao.commit-series.v5"))
	for i, commit := range commits {
		metadata, err := c.commitRebaseStableMetadata(ctx, commit)
		if err != nil {
			return CommitSeriesProof{}, err
		}
		canonicalDelta, err := c.canonicalCommitDelta(ctx, expectedSeriesParent(baseSHA, commits, i), commit, pathAliases)
		if err != nil {
			return CommitSeriesProof{}, fmt.Errorf("commit %s: %w", commit, err)
		}
		writeSeriesPart(digest, metadata)
		writeSeriesPart(digest, canonicalDelta)
	}
	return CommitSeriesProof{Count: len(commits), Fingerprint: CommitSeriesFingerprintVersion + hex.EncodeToString(digest.Sum(nil))}, nil
}

func expectedSeriesParent(base string, commits []string, index int) string {
	if index == 0 {
		return base
	}
	return commits[index-1]
}

func (c Client) commitRebaseStableMetadata(ctx context.Context, commit string) ([]byte, error) {
	raw, err := c.rawOutput(ctx, "cat-file", "commit", commit)
	if err != nil {
		return nil, fmt.Errorf("read metadata for commit %s: %w", commit, err)
	}
	header, message, found := strings.Cut(raw, "\n\n")
	if !found {
		return nil, fmt.Errorf("unsupported commit series: commit %s has no message boundary", commit)
	}
	var author, encoding string
	for line := range strings.SplitSeq(header, "\n") {
		switch {
		case strings.HasPrefix(line, "author "):
			if author != "" {
				return nil, fmt.Errorf("unsupported commit series: commit %s has ambiguous author metadata", commit)
			}
			author = line
		case strings.HasPrefix(line, "encoding "):
			encoding = line
		}
	}
	if author == "" {
		return nil, fmt.Errorf("unsupported commit series: commit %s has no author metadata", commit)
	}
	metadata := make([]byte, 0, len(author)+len(encoding)+len(message)+3)
	metadata = append(metadata, author...)
	metadata = append(metadata, '\n')
	metadata = append(metadata, encoding...)
	metadata = append(metadata, '\n')
	metadata = append(metadata, message...)
	return metadata, nil
}

type commitPathChange struct {
	status        string
	oldPath       string
	path          string
	canonicalPath string
}

func (c Client) upstreamRenameAliases(ctx context.Context, oldBase, newBase string) (map[string]string, error) {
	raw, err := c.rawOutput(ctx, "diff", "-r", "--name-status", "-z", "--find-renames", oldBase, newBase)
	if err != nil {
		return nil, fmt.Errorf("inspect upstream renames for commit-series proof: %w", err)
	}
	changes, err := parseCommitPathChanges(raw)
	if err != nil {
		return nil, fmt.Errorf("inspect upstream renames for commit-series proof: %w", err)
	}
	aliases := make(map[string]string)
	destinations := make(map[string]string)
	for _, change := range changes {
		if !strings.HasPrefix(change.status, "R") {
			continue
		}
		if previous, exists := aliases[change.oldPath]; exists && previous != change.path {
			return nil, fmt.Errorf("unsupported commit series: upstream rename source %q is ambiguous", change.oldPath)
		}
		if previous, exists := destinations[change.path]; exists && previous != change.oldPath {
			return nil, fmt.Errorf("unsupported commit series: upstream rename destination %q is ambiguous", change.path)
		}
		aliases[change.oldPath] = change.path
		destinations[change.path] = change.oldPath
	}
	return aliases, nil
}

func (c Client) canonicalCommitDelta(ctx context.Context, parent, commit string, pathAliases map[string]string) ([]byte, error) {
	raw, err := c.rawOutput(ctx, "diff-tree", "-r", "--no-commit-id", "--no-renames", "--name-status", "-z", parent, commit)
	if err != nil {
		return nil, fmt.Errorf("list content delta: %w", err)
	}
	changes, err := parseCommitPathChanges(raw)
	if err != nil {
		return nil, err
	}
	for i := range changes {
		if changes[i].oldPath != "" {
			return nil, fmt.Errorf("unsupported commit series: feature rename remained after --no-renames")
		}
		changes[i].canonicalPath = changes[i].path
		if alias, ok := pathAliases[changes[i].path]; ok {
			changes[i].canonicalPath = alias
		}
	}
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].canonicalPath != changes[j].canonicalPath {
			return changes[i].canonicalPath < changes[j].canonicalPath
		}
		return changes[i].status < changes[j].status
	})
	for i := 1; i < len(changes); i++ {
		if changes[i-1].canonicalPath == changes[i].canonicalPath {
			return nil, fmt.Errorf("unsupported commit series: multiple changes canonicalize to path %q", changes[i].canonicalPath)
		}
	}

	var canonical bytes.Buffer
	writeBufferPart(&canonical, []byte("tao.commit-delta.v5"))
	for _, change := range changes {
		delta, err := c.rawOutput(ctx, "diff-tree", "-r", "--no-commit-id", "--no-renames", "--patch", "--unified=2147483647", parent, commit, "--", change.path)
		if err != nil {
			return nil, fmt.Errorf("read content delta for path %q: %w", change.path, err)
		}
		fileDelta, err := canonicalCommitDelta(delta)
		if err != nil {
			return nil, err
		}
		writeBufferPart(&canonical, []byte(change.status))
		writeBufferPart(&canonical, []byte(change.canonicalPath))
		writeBufferPart(&canonical, fileDelta)
	}
	return canonical.Bytes(), nil
}

func parseCommitPathChanges(raw string) ([]commitPathChange, error) {
	if raw == "" {
		return nil, nil
	}
	fields := strings.Split(raw, "\x00")
	if fields[len(fields)-1] != "" {
		return nil, fmt.Errorf("unsupported commit series: malformed NUL-terminated path delta")
	}
	fields = fields[:len(fields)-1]
	changes := make([]commitPathChange, 0, len(fields)/2)
	for i := 0; i < len(fields); {
		status := fields[i]
		i++
		if status == "" {
			return nil, fmt.Errorf("unsupported commit series: malformed path status")
		}
		change := commitPathChange{status: status}
		if strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C") {
			if i+1 >= len(fields) {
				return nil, fmt.Errorf("unsupported commit series: malformed renamed path delta")
			}
			change.oldPath, change.path = fields[i], fields[i+1]
			i += 2
		} else {
			if i >= len(fields) {
				return nil, fmt.Errorf("unsupported commit series: malformed path delta")
			}
			change.path = fields[i]
			i++
		}
		if change.path == "" || (change.oldPath == "" && (strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C"))) {
			return nil, fmt.Errorf("unsupported commit series: empty changed path")
		}
		changes = append(changes, change)
	}
	return changes, nil
}

func canonicalCommitDelta(delta string) ([]byte, error) {
	if strings.Contains(delta, "GIT binary patch") || strings.Contains(delta, "Binary files ") {
		return nil, fmt.Errorf("unsupported commit series: binary content change")
	}
	lines := strings.Split(delta, "\n")
	var canonical strings.Builder
	inHunk := false
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if !inHunk && (strings.HasPrefix(line, "diff --git ") || strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ")) {
			continue // canonical paths are framed separately from the patch
		}
		if strings.HasPrefix(line, "index ") {
			continue // blob IDs also bind the parent tree, which rebase rewrites
		}
		if strings.HasPrefix(line, "@@ ") {
			inHunk = true
			oldStart, oldCount, err := parseOldHunkRange(line)
			if err != nil {
				return nil, err
			}
			start := i + 1
			i = start
			for i < len(lines) && isHunkPayloadLine(lines[i]) {
				i++
			}
			if err := writeCanonicalHunk(&canonical, lines[start:i], oldStart, oldCount); err != nil {
				return nil, err
			}
			i--
			continue
		}
		canonical.WriteString(line)
		canonical.WriteByte('\n')
	}
	return []byte(canonical.String()), nil
}

type canonicalEdit struct {
	oldPosition int
	deleted     []string
	raw         []string
}

func parseOldHunkRange(header string) (int, int, error) {
	fields := strings.Fields(header)
	if len(fields) < 4 || fields[0] != "@@" || fields[3] != "@@" || !strings.HasPrefix(fields[1], "-") {
		return 0, 0, fmt.Errorf("unsupported commit series: malformed patch hunk")
	}
	value := strings.TrimPrefix(fields[1], "-")
	startText, countText, hasCount := strings.Cut(value, ",")
	start, err := strconv.Atoi(startText)
	if err != nil || start < 0 {
		return 0, 0, fmt.Errorf("unsupported commit series: malformed patch hunk")
	}
	count := 1
	if hasCount {
		count, err = strconv.Atoi(countText)
		if err != nil || count < 0 {
			return 0, 0, fmt.Errorf("unsupported commit series: malformed patch hunk")
		}
	}
	return start, count, nil
}

func isHunkPayloadLine(line string) bool {
	return line != "" && strings.ContainsRune(" +-\\", rune(line[0]))
}

func writeCanonicalHunk(canonical *strings.Builder, payload []string, oldStart, oldCount int) error {
	oldLines := make([]string, 0, oldCount)
	var edits []canonicalEdit
	var current *canonicalEdit
	oldPosition := 0
	flush := func() {
		if current != nil {
			edits = append(edits, *current)
			current = nil
		}
	}
	for _, line := range payload {
		switch line[0] {
		case ' ':
			flush()
			oldLines = append(oldLines, line[1:])
			oldPosition++
		case '-':
			if current == nil {
				current = &canonicalEdit{oldPosition: oldPosition}
			}
			current.deleted = append(current.deleted, line[1:])
			current.raw = append(current.raw, line)
			oldLines = append(oldLines, line[1:])
			oldPosition++
		case '+':
			if current == nil {
				current = &canonicalEdit{oldPosition: oldPosition}
			}
			current.raw = append(current.raw, line)
		case '\\':
			if current != nil {
				current.raw = append(current.raw, line)
			}
		default:
			return fmt.Errorf("unsupported commit series: malformed patch payload")
		}
	}
	flush()
	if oldCount != len(oldLines) || (oldCount > 0 && oldStart != 1) || (oldCount == 0 && oldStart != 0) {
		return fmt.Errorf("unsupported commit series: patch does not contain the complete parent file")
	}
	for _, edit := range edits {
		locator, err := canonicalEditLocator(oldLines, edit)
		if err != nil {
			return err
		}
		canonical.WriteString("@@ tao-location ")
		canonical.WriteString(locator)
		canonical.WriteString(" @@\n")
		for _, line := range edit.raw {
			canonical.WriteString(line)
			canonical.WriteByte('\n')
		}
	}
	return nil
}

func canonicalEditLocator(oldLines []string, edit canonicalEdit) (string, error) {
	deletedCount := len(edit.deleted)
	if edit.oldPosition < 0 || edit.oldPosition+deletedCount > len(oldLines) ||
		!equalLines(oldLines[edit.oldPosition:edit.oldPosition+deletedCount], edit.deleted) {
		return "", fmt.Errorf("unsupported commit series: changed lines do not identify their parent-file location")
	}

	// Prefer only the unchanged lines immediately adjacent to the edit. Unlike a
	// nearest-unique-line locator, this identity cannot be displaced by an
	// unrelated insertion elsewhere between an edit and a distant unique line.
	beforeAnchor := edit.oldPosition - 1
	afterAnchor := edit.oldPosition + deletedCount
	if contextMatchCount(oldLines, edit, beforeAnchor, afterAnchor) == 1 {
		digest := sha256.New()
		writeSeriesPart(digest, []byte("tao.edit-location.v2"))
		writeContextAnchor(digest, "before", "file-start", oldLines, beforeAnchor)
		writeContextAnchor(digest, "after", "file-end", oldLines, afterAnchor)
		return "context:sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
	}

	// Pure insertions between repeated formatting lines (most commonly blank
	// lines) have no deleted payload and cannot always be identified by one
	// adjacent line on each side. Expand contiguous context symmetrically until
	// it becomes unique, but never use a file boundary to distinguish otherwise
	// identical occurrences. Any upstream edit inside this local window then
	// changes the proof conservatively instead of silently relocating the edit.
	for radius := 2; edit.oldPosition-radius >= 0 && edit.oldPosition+deletedCount+radius <= len(oldLines); radius++ {
		before := oldLines[edit.oldPosition-radius : edit.oldPosition]
		after := oldLines[edit.oldPosition+deletedCount : edit.oldPosition+deletedCount+radius]
		if expandedContextMatchCount(oldLines, edit, before, after) != 1 {
			continue
		}
		digest := sha256.New()
		writeSeriesPart(digest, []byte("tao.edit-location.v2-expanded"))
		for _, line := range before {
			writeSeriesPart(digest, []byte(line))
		}
		for _, line := range after {
			writeSeriesPart(digest, []byte(line))
		}
		return "context:sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
	}
	return "", fmt.Errorf("unsupported commit series: edit location has ambiguous surrounding context")
}

func contextMatchCount(oldLines []string, edit canonicalEdit, beforeAnchor, afterAnchor int) int {
	matches := 0
	for position := 0; position+len(edit.deleted) <= len(oldLines); position++ {
		if len(edit.deleted) > 0 && !equalLines(oldLines[position:position+len(edit.deleted)], edit.deleted) {
			continue
		}
		if sameContextAnchor(oldLines, position-1, beforeAnchor) && sameContextAnchor(oldLines, position+len(edit.deleted), afterAnchor) {
			matches++
		}
	}
	return matches
}

func expandedContextMatchCount(oldLines []string, edit canonicalEdit, before, after []string) int {
	matches := 0
	for position := len(before); position+len(edit.deleted)+len(after) <= len(oldLines); position++ {
		if !equalLines(oldLines[position-len(before):position], before) ||
			!equalLines(oldLines[position:position+len(edit.deleted)], edit.deleted) ||
			!equalLines(oldLines[position+len(edit.deleted):position+len(edit.deleted)+len(after)], after) {
			continue
		}
		matches++
	}
	return matches
}

func sameContextAnchor(lines []string, candidate, expected int) bool {
	if expected < 0 || expected >= len(lines) {
		return candidate == expected
	}
	return candidate >= 0 && candidate < len(lines) && lines[candidate] == lines[expected]
}

func writeContextAnchor(digest hash.Hash, side, boundary string, lines []string, index int) {
	writeSeriesPart(digest, []byte(side))
	if index < 0 || index >= len(lines) {
		writeSeriesPart(digest, []byte(boundary))
		return
	}
	writeSeriesPart(digest, []byte("line"))
	writeSeriesPart(digest, []byte(lines[index]))
}

func equalLines(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func writeSeriesPart(digest hash.Hash, part []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(part)))
	_, _ = digest.Write(size[:])
	_, _ = digest.Write(part)
}

func writeBufferPart(buffer *bytes.Buffer, part []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(part)))
	_, _ = buffer.Write(size[:])
	_, _ = buffer.Write(part)
}
