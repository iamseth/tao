package commit

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/iamseth/tao/internal/gitops"
)

// StatusClassification is Tao's shared view of git status --porcelain output.
type StatusClassification struct {
	// CommitCandidates are non-.tao, unambiguous changed paths.
	CommitCandidates []string
	// TaoStagedPaths are staged .tao paths that must be excluded from commits.
	TaoStagedPaths []string
	// AmbiguousLines are entries that cannot safely be reduced to one path.
	AmbiguousLines []string
	// StartingDirtyPaths are candidates selected by the optional predicate.
	StartingDirtyPaths []string
}

// ClassifyStatus parses porcelain status once for commit and cleanliness checks.
func ClassifyStatus(status string, isStartingDirty func(string) bool) StatusClassification {
	var classification StatusClassification
	for line := range strings.SplitSeq(status, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		path, ambiguous := gitops.PorcelainPath(line)
		if ambiguous {
			if taoPaths, ok := taoMetadataRenamePaths(line); ok {
				classification.TaoStagedPaths = append(classification.TaoStagedPaths, taoPaths...)
				continue
			}
			classification.AmbiguousLines = append(classification.AmbiguousLines, line)
			continue
		}
		path = NormalizePath(path)
		if IsTaoMetadataPath(path) {
			if gitops.PorcelainIndexStatus(line) {
				classification.TaoStagedPaths = append(classification.TaoStagedPaths, path)
			}
			continue
		}
		if isStartingDirty != nil && isStartingDirty(path) {
			classification.StartingDirtyPaths = append(classification.StartingDirtyPaths, path)
		}
		classification.CommitCandidates = append(classification.CommitCandidates, path)
	}
	return classification
}

// StartingDirtyPredicate returns a normalized path-membership predicate.
func StartingDirtyPredicate(paths []string) func(string) bool {
	dirty := make(map[string]bool, len(paths))
	for _, path := range paths {
		dirty[NormalizePath(path)] = true
	}
	return func(path string) bool { return dirty[NormalizePath(path)] }
}

// NormalizePath converts a repository-relative path to its canonical slash form.
func NormalizePath(path string) string {
	return filepath.ToSlash(filepath.Clean(path))
}

// IsTaoMetadataPath reports whether path belongs to workspace-local Tao metadata.
func IsTaoMetadataPath(path string) bool {
	path = NormalizePath(path)
	return path == ".tao" || strings.HasPrefix(path, ".tao/")
}

func taoMetadataRenamePaths(line string) ([]string, bool) {
	if len(line) < 4 {
		return nil, false
	}
	from, to, found := strings.Cut(strings.TrimSpace(line[3:]), " -> ")
	if !found {
		return nil, false
	}
	from = strings.TrimSpace(from)
	to = strings.TrimSpace(to)
	if from == "" || to == "" || strings.Contains(from, `"`) || strings.Contains(to, `"`) {
		return nil, false
	}
	from = NormalizePath(from)
	to = NormalizePath(to)
	if !IsTaoMetadataPath(from) || !IsTaoMetadataPath(to) {
		return nil, false
	}
	return []string{from, to}, true
}

// ExpectedPaths is an advisory exact-and-glob path set.
type ExpectedPaths struct {
	exact map[string]bool
	globs []string
}

// NewExpectedPaths builds an advisory path set from normalized exact paths and
// repository-relative glob patterns.
func NewExpectedPaths(patterns ...string) ExpectedPaths {
	set := ExpectedPaths{exact: make(map[string]bool)}
	for _, pattern := range patterns {
		pattern = NormalizePath(pattern)
		if HasPathGlobMeta(pattern) {
			set.globs = append(set.globs, pattern)
		} else {
			set.exact[pattern] = true
		}
	}
	return set
}

// Allows reports whether an advisory expected-path pattern covers path.
func (s ExpectedPaths) Allows(path string) bool {
	path = NormalizePath(path)
	if s.exact[path] {
		return true
	}
	for _, pattern := range s.globs {
		if PathPatternMatch(pattern, path) {
			return true
		}
	}
	return false
}

// UnexpectedPaths returns paths not covered by the advisory expected-path set.
// It does not make those paths unsafe to commit.
func UnexpectedPaths(paths []string, expected ExpectedPaths) []string {
	var unexpected []string
	for _, path := range paths {
		if !expected.Allows(path) {
			unexpected = append(unexpected, path)
		}
	}
	return unexpected
}

// HasPathGlobMeta reports whether pattern contains supported glob syntax.
func HasPathGlobMeta(pattern string) bool {
	return strings.ContainsAny(pattern, "*?[")
}

// PathPatternMatch matches repository paths with Tao's expected-files glob semantics.
func PathPatternMatch(pattern, path string) bool {
	re, err := regexp.Compile(PathPatternRegexp(pattern))
	return err == nil && re.MatchString(path)
}

// PathPatternRegexp translates Tao's expected-files glob syntax to a regexp.
func PathPatternRegexp(pattern string) string {
	var builder strings.Builder
	builder.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				i++
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					i++
					builder.WriteString("(?:.*/)?")
				} else {
					builder.WriteString(".*")
				}
			} else {
				builder.WriteString("[^/]*")
			}
		case '?':
			builder.WriteString("[^/]")
		case '[':
			end := strings.IndexByte(pattern[i+1:], ']')
			if end < 0 {
				builder.WriteString(`\[`)
				continue
			}
			end += i + 1
			builder.WriteString(pattern[i : end+1])
			i = end
		default:
			builder.WriteString(regexp.QuoteMeta(string(pattern[i])))
		}
	}
	builder.WriteString("$")
	return builder.String()
}

var safetyPolicy = struct {
	secretBaseNamePrefixes     []string
	nonSecretTemplateBaseNames []string
	secretPathSubstrings       []string
	generatedExactPaths        []string
	generatedPathPrefixes      []string
}{
	secretBaseNamePrefixes:     []string{".env"},
	nonSecretTemplateBaseNames: []string{".env.example"},
	secretPathSubstrings:       []string{"credential", "secret", "private_key", "id_rsa"},
	generatedExactPaths:        []string{"coverage.out"},
	generatedPathPrefixes:      []string{"bin/"},
}

// SuspectedSecretPath applies the established automatic-slice secret policy.
func SuspectedSecretPath(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	lowerPath := strings.ToLower(path)
	for _, token := range safetyPolicy.secretPathSubstrings {
		if strings.Contains(lowerPath, token) {
			return true
		}
	}
	if slices.Contains(safetyPolicy.nonSecretTemplateBaseNames, base) {
		return false
	}
	for _, prefix := range safetyPolicy.secretBaseNamePrefixes {
		if strings.HasPrefix(base, prefix) {
			return true
		}
	}
	return false
}

// GeneratedPath applies the established automatic-slice generated-file policy.
func GeneratedPath(path string) bool {
	path = filepath.ToSlash(path)
	if slices.Contains(safetyPolicy.generatedExactPaths, path) {
		return true
	}
	for _, prefix := range safetyPolicy.generatedPathPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// SafetyError reports ambiguous, secret, and generated paths using the shared
// automatic-commit refusal text.
func SafetyError(paths, ambiguous []string) error {
	ambiguous = UniqueStrings(ambiguous)
	var secretPaths []string
	var generatedPaths []string
	for _, path := range UniquePaths(paths) {
		if SuspectedSecretPath(path) {
			secretPaths = append(secretPaths, path)
		}
		if GeneratedPath(path) {
			generatedPaths = append(generatedPaths, path)
		}
	}
	if len(ambiguous) == 0 && len(secretPaths) == 0 && len(generatedPaths) == 0 {
		return nil
	}
	var parts []string
	if len(ambiguous) > 0 {
		parts = append(parts, listLabel(len(ambiguous), "ambiguous git status entry", "ambiguous git status entries")+": "+strings.Join(ambiguous, ", "))
	}
	if len(secretPaths) > 0 {
		parts = append(parts, listLabel(len(secretPaths), "suspected secret path", "suspected secret paths")+": "+strings.Join(secretPaths, ", "))
	}
	if len(generatedPaths) > 0 {
		parts = append(parts, listLabel(len(generatedPaths), "generated artifact path", "generated artifact paths")+": "+strings.Join(generatedPaths, ", "))
	}
	return fmt.Errorf("commit safety checks refused to auto-commit file(s): %s; review and commit manually", strings.Join(parts, "; "))
}

// UniquePaths normalizes, deduplicates, and sorts non-empty repository paths.
func UniquePaths(paths []string) []string {
	unique := make(map[string]bool, len(paths))
	for _, path := range paths {
		path = NormalizePath(path)
		if path != "." && path != "" {
			unique[path] = true
		}
	}
	return sortedSet(unique)
}

// UniqueStrings deduplicates and sorts non-empty strings.
func UniqueStrings(values []string) []string {
	unique := make(map[string]bool, len(values))
	for _, value := range values {
		if value != "" {
			unique[value] = true
		}
	}
	return sortedSet(unique)
}

func sortedSet(set map[string]bool) []string {
	values := make([]string, 0, len(set))
	for value := range set {
		values = append(values, value)
	}
	slices.Sort(values)
	return values
}

func listLabel(count int, singular, plural string) string {
	if count == 1 {
		return singular
	}
	return plural
}
