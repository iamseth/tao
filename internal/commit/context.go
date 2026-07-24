package commit

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

const (
	contextDiffLimit = 64 * 1024
	changeDiffLimit  = 4 * 1024 * 1024
	recentLogLimit   = 12
)

// ContextGit is the read-only Git boundary used to build standalone commit
// context. Building context never changes the index or writes Tao state.
type ContextGit interface {
	RevParse(context.Context, string) (string, error)
	StatusPorcelainAllUntracked(context.Context) (string, error)
	WorkingDiff(context.Context, ...string) (string, error)
	RecentLog(context.Context, int) (string, error)
}

// RejectedPath explains why a changed path is excluded from model context and
// standalone staging.
type RejectedPath struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
	Staged bool   `json:"staged,omitempty"`
}

// StandaloneContext is the bounded, safety-filtered handoff used by the active
// agent to propose a standalone commit message. Fingerprint binds finalization
// to the complete live identity, not merely the possibly truncated display.
type StandaloneContext struct {
	Head                 string         `json:"head"`
	Fingerprint          string         `json:"context_fingerprint"`
	AllowedPaths         []string       `json:"allowed_paths"`
	RejectedPaths        []RejectedPath `json:"rejected_paths"`
	AllowedDiff          string         `json:"allowed_diff"`
	AllowedDiffTruncated bool           `json:"allowed_diff_truncated"`
	RecentHistory        string         `json:"recent_history"`
	ExclusionSources     []string       `json:"exclusion_sources,omitempty"`
}

// RepoExclusions contains repository-local paths discovered from AGENTS.md.
type RepoExclusions struct {
	Patterns []string
	Sources  []string
}

var (
	backtickTokenPattern = regexp.MustCompile("`([^`]+)`")
	lineTokenPattern     = regexp.MustCompile(`[A-Za-z0-9._/-]+/?`)
	secretPathPattern    = regexp.MustCompile(`(?i)(^|/)(secret|secrets|credential|credentials|token|tokens|apikey|api-key|private-key)([._/-]|$)`)
	credentialPatterns   = []*regexp.Regexp{
		regexp.MustCompile(`(?i)aws_access_key_id\s*=`),
		regexp.MustCompile(`(?i)aws_secret_access_key\s*=`),
		regexp.MustCompile(`(?i)api[_-]?key\s*[:=]\s*['"]?[A-Za-z0-9_-]{16,}`),
		regexp.MustCompile(`(?i)secret[_-]?key\s*[:=]\s*['"]?[A-Za-z0-9_-]{16,}`),
		regexp.MustCompile(`(?i)password\s*[:=]\s*['"][^'"]{8,}['"]`),
		regexp.MustCompile(`(?i)-----BEGIN (RSA |OPENSSH |EC |DSA )?PRIVATE KEY-----`),
		regexp.MustCompile(`ghp_[A-Za-z0-9]{20,}`),
	}
)

// ReadRepoExclusions ports the standalone Pi command's narrow AGENTS.md
// discovery. Only established Tao local-only tokens are honored; prose cannot
// turn arbitrary source paths into exclusions.
func ReadRepoExclusions(repoRoot string) (RepoExclusions, error) {
	patterns := map[string]bool{".tao/": true}
	path := filepath.Join(repoRoot, "AGENTS.md")
	contents, err := os.ReadFile(path) // #nosec G304 -- repoRoot is the caller-selected Git top level.
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return RepoExclusions{Patterns: sortedSet(patterns)}, nil
		}
		return RepoExclusions{}, fmt.Errorf("read standalone commit exclusions: %w", err)
	}
	guidance := string(contents)
	for _, match := range backtickTokenPattern.FindAllStringSubmatch(guidance, -1) {
		addLocalOnlyPattern(patterns, strings.TrimSpace(match[1]))
	}
	if strings.Contains(strings.ToLower(guidance), "local-only") {
		for line := range strings.SplitSeq(guidance, "\n") {
			for _, token := range lineTokenPattern.FindAllString(line, -1) {
				addLocalOnlyPattern(patterns, token)
			}
		}
	}
	return RepoExclusions{Patterns: sortedSet(patterns), Sources: []string{"AGENTS.md"}}, nil
}

func addLocalOnlyPattern(patterns map[string]bool, token string) {
	token = NormalizePath(strings.TrimSpace(token))
	if token == ".tao" || strings.HasPrefix(token, ".tao/") || token == "planning-session.json" {
		if token != "planning-session.json" && !strings.HasSuffix(token, "/") {
			token += "/"
		}
		patterns[token] = true
	}
}

// BuildStandaloneContext captures the complete live identity while returning a
// bounded diff. Rejected content is classified before it can enter AllowedDiff.
func BuildStandaloneContext(ctx context.Context, git ContextGit, repoRoot string) (StandaloneContext, error) {
	if git == nil {
		return StandaloneContext{}, errors.New("standalone commit context requires Git")
	}
	head, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		return StandaloneContext{}, fmt.Errorf("resolve standalone commit HEAD: %w", err)
	}
	status, err := git.StatusPorcelainAllUntracked(ctx)
	if err != nil {
		return StandaloneContext{}, fmt.Errorf("read standalone commit status: %w", err)
	}
	exclusions, err := ReadRepoExclusions(repoRoot)
	if err != nil {
		return StandaloneContext{}, err
	}
	classification := ClassifyStatus(status, nil)
	paths := UniquePaths(classification.CommitCandidates)
	paths = slices.DeleteFunc(paths, func(path string) bool { return strings.HasPrefix(path, `"`) })
	untracked := untrackedPaths(status)
	staged := standaloneStagedPaths(status, classification.TaoStagedPaths)
	result := StandaloneContext{Head: head, AllowedPaths: []string{}, RejectedPaths: []RejectedPath{}, ExclusionSources: exclusions.Sources}
	ambiguousLines := append([]string(nil), classification.AmbiguousLines...)
	ambiguousLines = append(ambiguousLines, standaloneAmbiguousStatusLines(status)...)
	for _, line := range UniqueStrings(ambiguousLines) {
		result.RejectedPaths = append(result.RejectedPaths, RejectedPath{Path: line, Reason: "ambiguous git status entry"})
	}
	for _, path := range taoMetadataPaths(status, classification.TaoStagedPaths) {
		result.RejectedPaths = append(result.RejectedPaths, RejectedPath{Path: path, Reason: "local-only path excluded by .tao/", Staged: staged[path]})
	}

	var allowed strings.Builder
	identity := sha256.New()
	writeIdentity(identity, "schema", "tao.standalone-context.v2")
	writeIdentity(identity, "head", head)
	writeIdentity(identity, "status", status)
	for _, path := range paths {
		diff, pathReason, diffErr := standalonePathDiff(ctx, git, repoRoot, path, untracked[path])
		if diffErr != nil {
			return StandaloneContext{}, diffErr
		}
		reason := pathReason
		if reason == "" {
			reason = standaloneRejectionReason(path, diff, exclusions.Patterns)
		}
		if len(diff) > changeDiffLimit {
			reason = "change exceeds standalone diff safety limit"
		}
		if reason != "" {
			result.RejectedPaths = append(result.RejectedPaths, RejectedPath{Path: path, Reason: reason, Staged: staged[path]})
			continue
		}
		result.AllowedPaths = append(result.AllowedPaths, path)
		writeIdentity(identity, "path", path)
		writeIdentity(identity, "diff", diff)
		if allowed.Len() > 0 && allowed.Len() < contextDiffLimit {
			allowed.WriteByte('\n')
		}
		if allowed.Len() < contextDiffLimit {
			remaining := contextDiffLimit - allowed.Len()
			if len(diff) > remaining {
				allowed.WriteString(diff[:remaining])
				result.AllowedDiffTruncated = true
			} else {
				allowed.WriteString(diff)
			}
		} else if diff != "" {
			result.AllowedDiffTruncated = true
		}
	}
	for _, rejected := range result.RejectedPaths {
		writeIdentity(identity, "rejected", rejected.Path+"\x00"+rejected.Reason)
	}
	history, err := git.RecentLog(ctx, recentLogLimit)
	if err != nil {
		return StandaloneContext{}, fmt.Errorf("read standalone commit history: %w", err)
	}
	result.AllowedDiff = strings.ToValidUTF8(allowed.String(), "�")
	result.RecentHistory = boundedText(history, 16*1024)
	result.Fingerprint = hex.EncodeToString(identity.Sum(nil))
	return result, nil
}

func writeIdentity(writer io.Writer, label, value string) {
	writeIdentityField(writer, label)
	writeIdentityField(writer, value)
}

func writeIdentityField(writer io.Writer, value string) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = writer.Write(size[:])
	_, _ = io.WriteString(writer, value)
}

func standalonePathDiff(ctx context.Context, git ContextGit, repoRoot, path string, untracked bool) (string, string, error) {
	if !untracked {
		diff, err := git.WorkingDiff(ctx, path)
		if err != nil {
			return "", "", fmt.Errorf("read standalone diff for %s: %w", path, err)
		}
		return diff, "", nil
	}
	fullPath := filepath.Join(repoRoot, filepath.FromSlash(path))
	info, err := os.Lstat(fullPath)
	if err != nil {
		return "", "", fmt.Errorf("inspect untracked path %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return "", "non-regular untracked path", nil
	}
	file, err := os.Open(fullPath) // #nosec G304 -- path is a Git-reported repository-relative path.
	if err != nil {
		return "", "", fmt.Errorf("read untracked path %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	contents, err := io.ReadAll(io.LimitReader(file, changeDiffLimit+1))
	if err != nil {
		return "", "", fmt.Errorf("read untracked path %s: %w", path, err)
	}
	var diff strings.Builder
	fmt.Fprintf(&diff, "diff --git a/%s b/%s\nnew file mode %o\n--- /dev/null\n+++ b/%s\n", path, path, info.Mode().Perm(), path)
	// Keep untracked content byte-exact here so the fingerprint detects changes
	// between invalid UTF-8 sequences. AllowedDiff is sanitized separately.
	for line := range strings.SplitSeq(string(contents), "\n") {
		diff.WriteByte('+')
		diff.WriteString(line)
		diff.WriteByte('\n')
	}
	return diff.String(), "", nil
}

func untrackedPaths(status string) map[string]bool {
	paths := make(map[string]bool)
	for line := range strings.SplitSeq(status, "\n") {
		if strings.HasPrefix(line, "?? ") {
			if path, ambiguous := porcelainStandalonePath(line); !ambiguous {
				paths[path] = true
			}
		}
	}
	return paths
}

func standaloneStagedPaths(status string, taoStaged []string) map[string]bool {
	staged := make(map[string]bool, len(taoStaged))
	for _, path := range taoStaged {
		staged[NormalizePath(path)] = true
	}
	for line := range strings.SplitSeq(status, "\n") {
		path, ambiguous := porcelainStandalonePath(line)
		if !ambiguous && len(line) > 0 && line[0] != ' ' && line[0] != '?' {
			staged[path] = true
		}
	}
	return staged
}

func taoMetadataPaths(status string, staged []string) []string {
	paths := append([]string(nil), staged...)
	for line := range strings.SplitSeq(status, "\n") {
		path, ambiguous := porcelainStandalonePath(line)
		if !ambiguous && IsTaoMetadataPath(path) {
			paths = append(paths, path)
		}
	}
	return UniquePaths(paths)
}

func porcelainStandalonePath(line string) (string, bool) {
	if len(line) < 4 || strings.HasPrefix(line[3:], `"`) || strings.Contains(line[3:], " -> ") {
		return "", true
	}
	path := NormalizePath(strings.TrimSpace(line[3:]))
	return path, path == "." || path == ""
}

func standaloneAmbiguousStatusLines(status string) []string {
	var ambiguous []string
	for line := range strings.SplitSeq(status, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if _, unsafe := porcelainStandalonePath(line); unsafe {
			ambiguous = append(ambiguous, line)
		}
	}
	return ambiguous
}

func standaloneRejectionReason(path, diff string, exclusions []string) string {
	path = NormalizePath(path)
	for _, pattern := range exclusions {
		if matchesStandaloneExclusion(path, pattern) {
			return "local-only path excluded by " + pattern
		}
	}
	if standaloneGeneratedPath(path) {
		return "generated artifact"
	}
	if standaloneForbiddenPath(path) {
		return "forbidden credential path"
	}
	if secretPathPattern.MatchString(path) {
		return "secret-looking path"
	}
	added := addedDiffLines(diff)
	if slices.ContainsFunc(credentialPatterns, func(pattern *regexp.Regexp) bool { return pattern.MatchString(added) }) {
		return "credential-looking diff content"
	}
	return ""
}

func matchesStandaloneExclusion(path, pattern string) bool {
	pattern = strings.TrimPrefix(filepath.ToSlash(pattern), "./")
	if before, ok := strings.CutSuffix(pattern, "/"); ok {
		prefix := before
		return path == prefix || strings.HasPrefix(path, pattern)
	}
	return path == pattern || strings.HasPrefix(path, pattern+"/")
}

func standaloneGeneratedPath(path string) bool {
	lower := strings.ToLower(filepath.ToSlash(path))
	base := filepath.Base(lower)
	if lower == "coverage.out" || strings.HasPrefix(lower, "bin/") || strings.HasPrefix(lower, "dist/") || strings.HasPrefix(lower, "build/") || strings.HasPrefix(lower, "node_modules/") {
		return true
	}
	if slices.Contains([]string{"package-lock.json", "yarn.lock", "pnpm-lock.yaml"}, base) || matchesGeneratedDirectory(lower, "coverage") || matchesGeneratedDirectory(lower, ".turbo") || matchesGeneratedDirectory(lower, ".next") {
		return true
	}
	return strings.HasSuffix(lower, ".min.js") || strings.HasSuffix(lower, ".min.css") || strings.Contains(lower, ".generated.")
}

func matchesGeneratedDirectory(path, directory string) bool {
	return strings.HasPrefix(path, directory+"/") || strings.Contains(path, "/"+directory+"/")
}

func standaloneForbiddenPath(path string) bool {
	lower := strings.ToLower(filepath.ToSlash(path))
	base := filepath.Base(lower)
	return strings.HasPrefix(base, ".env.") || base == ".env" || slices.Contains([]string{".npmrc", ".pypirc", "id_rsa", "id_ed25519", "known_hosts"}, base)
}

func addedDiffLines(diff string) string {
	var added strings.Builder
	for line := range strings.SplitSeq(diff, "\n") {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			added.WriteString(line)
			added.WriteByte('\n')
		}
	}
	return added.String()
}

func boundedText(value string, limit int) string {
	if len(value) <= limit {
		return strings.ToValidUTF8(value, "�")
	}
	return strings.ToValidUTF8(value[:limit], "�")
}
