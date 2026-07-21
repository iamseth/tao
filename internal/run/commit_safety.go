package run

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/iamseth/tao/internal/gitops"
	"github.com/iamseth/tao/internal/plan"
)

// gitStatusClassification is the shared git status --porcelain view for
// automatic slice commits and review cleanliness. The helpers in this file
// also identify expected slice-commit paths, exclude Tao metadata, and apply
// commit-safety patterns for secrets and generated artifacts.
type gitStatusClassification struct {
	// CommitCandidates are non-.tao, non-ambiguous paths available to slice
	// completion or review-leftover checks.
	CommitCandidates []string
	// TaoStagedPaths are .tao/ paths with a staged index entry. Slice completion
	// unstages them so Tao metadata stays out of automatic commits.
	TaoStagedPaths []string
	// AmbiguousLines are raw porcelain lines that PorcelainPath could not
	// resolve to a single path (rename/copy). Slice completion and review
	// cleanliness both treat them as a hard stop.
	AmbiguousLines []string
	// StartingDirtyPaths are paths already dirty when the run started. Review
	// leftover checks may tolerate them.
	StartingDirtyPaths []string
}

// classifyGitStatus parses a git status --porcelain string in one pass and
// classifies each entry. isStartingDirty may be nil; when non-nil it identifies
// paths that were dirty before the run started for review-leftover checks.
func classifyGitStatus(status string, isStartingDirty func(string) bool) gitStatusClassification {
	var c gitStatusClassification
	for line := range strings.SplitSeq(status, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		path, ambiguous := gitops.PorcelainPath(line)
		if ambiguous {
			// A rename/copy entirely inside .tao/ is Tao's own metadata, which
			// automatic slice commits exclude and the review gate must tolerate;
			// renames are index entries, so both sides are queued for
			// unstaging. Anything else ambiguous stays a hard stop.
			if taoPaths, ok := taoMetadataRenamePaths(line); ok {
				c.TaoStagedPaths = append(c.TaoStagedPaths, taoPaths...)
				continue
			}
			c.AmbiguousLines = append(c.AmbiguousLines, line)
			continue
		}
		path = normalizePlanCommitPath(path)
		if isTaoMetadataPath(path) {
			if gitops.PorcelainIndexStatus(line) {
				c.TaoStagedPaths = append(c.TaoStagedPaths, path)
			}
			continue
		}
		if isStartingDirty != nil && isStartingDirty(path) {
			c.StartingDirtyPaths = append(c.StartingDirtyPaths, path)
		}
		c.CommitCandidates = append(c.CommitCandidates, path)
	}
	return c
}

func startingDirtyPredicate(paths []string) func(string) bool {
	dirty := make(map[string]bool, len(paths))
	for _, path := range paths {
		dirty[normalizePlanCommitPath(path)] = true
	}
	return func(path string) bool {
		return dirty[normalizePlanCommitPath(path)]
	}
}

func unexpectedPlanCommitPaths(paths []string, allowed expectedPlanCommitPathSet) []string {
	var unexpected []string
	for _, path := range paths {
		if !allowed.Allows(path) {
			unexpected = append(unexpected, path)
		}
	}
	return unexpected
}

type expectedPlanCommitPathSet struct {
	exact map[string]bool
	globs []string
}

func (s expectedPlanCommitPathSet) Allows(path string) bool {
	path = normalizePlanCommitPath(path)
	if s.exact[path] {
		return true
	}
	for _, pattern := range s.globs {
		if planCommitGlobMatch(pattern, path) {
			return true
		}
	}
	return false
}

func expectedPlanCommitPaths(detail *plan.PlanDetail, additionallyCompleted ...string) expectedPlanCommitPathSet {
	allowed := expectedPlanCommitPathSet{exact: map[string]bool{}}
	completed := map[string]bool{}
	for _, id := range detail.State.Plan.CompletedSlices {
		completed[id] = true
	}
	for _, id := range additionallyCompleted {
		completed[id] = true
	}
	for _, slice := range detail.Slices.Slices {
		if !completed[slice.ID] {
			continue
		}
		for _, path := range slice.ExpectedFiles {
			path = normalizePlanCommitPath(path)
			if hasPlanCommitGlobMeta(path) {
				allowed.globs = append(allowed.globs, path)
				continue
			}
			allowed.exact[path] = true
		}
	}
	return allowed
}

func normalizePlanCommitPath(path string) string {
	return filepath.ToSlash(filepath.Clean(path))
}

// isTaoMetadataPath reports whether a normalized path is Tao's local metadata
// directory. Automatic slice commits exclude these paths, so review cleanliness
// checks tolerate them too.
func isTaoMetadataPath(path string) bool {
	return path == ".tao" || strings.HasPrefix(path, ".tao/")
}

// taoMetadataRenamePaths extracts both sides of a rename/copy porcelain line
// when they are plain (unquoted) paths inside .tao/. Quoted paths or any side
// outside .tao/ report false, keeping the line ambiguous — a conservative
// hard stop, matching the pre-existing behavior for every other rename.
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
	from = normalizePlanCommitPath(from)
	to = normalizePlanCommitPath(to)
	if !isTaoMetadataPath(from) || !isTaoMetadataPath(to) {
		return nil, false
	}
	return []string{from, to}, true
}

func hasPlanCommitGlobMeta(pattern string) bool {
	return strings.ContainsAny(pattern, "*?[")
}

func planCommitGlobMatch(pattern string, path string) bool {
	re, err := regexp.Compile(planCommitGlobRegexp(pattern))
	if err != nil {
		return false
	}
	return re.MatchString(path)
}

func planCommitGlobRegexp(pattern string) string {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				i++
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					i++
					b.WriteString("(?:.*/)?")
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '[':
			end := strings.IndexByte(pattern[i+1:], ']')
			if end < 0 {
				b.WriteString(`\[`)
				continue
			}
			end += i + 1
			b.WriteString(pattern[i : end+1])
			i = end
		default:
			b.WriteString(regexp.QuoteMeta(string(pattern[i])))
		}
	}
	b.WriteString("$")
	return b.String()
}

// commitSafetyPolicy decides which changed paths are unsafe for automatic
// slice commits. The pattern lists preserve the established detection behavior;
// do not add or broaden patterns without a corresponding behavior decision.
type commitSafetyPolicy struct {
	secretBaseNamePrefixes     []string
	nonSecretTemplateBaseNames []string
	secretPathSubstrings       []string
	generatedExactPaths        []string
	generatedPathPrefixes      []string
}

var defaultCommitSafetyPolicy = commitSafetyPolicy{
	secretBaseNamePrefixes:     []string{".env"},
	nonSecretTemplateBaseNames: []string{".env.example"},
	secretPathSubstrings:       []string{"credential", "secret", "private_key", "id_rsa"},
	generatedExactPaths:        []string{"coverage.out"},
	generatedPathPrefixes:      []string{"bin/"},
}

func (p commitSafetyPolicy) suspectedSecret(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	lowerPath := strings.ToLower(path)
	for _, token := range p.secretPathSubstrings {
		if strings.Contains(lowerPath, token) {
			return true
		}
	}
	if slices.Contains(p.nonSecretTemplateBaseNames, base) {
		return false
	}
	for _, prefix := range p.secretBaseNamePrefixes {
		if strings.HasPrefix(base, prefix) {
			return true
		}
	}
	return false
}

func (p commitSafetyPolicy) generated(path string) bool {
	path = filepath.ToSlash(path)
	if slices.Contains(p.generatedExactPaths, path) {
		return true
	}
	for _, prefix := range p.generatedPathPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func suspectedSecretPath(path string) bool {
	return defaultCommitSafetyPolicy.suspectedSecret(path)
}

func generatedPath(path string) bool {
	return defaultCommitSafetyPolicy.generated(path)
}

func commitSafetyScreenError(paths []string, ambiguous []string) error {
	ambiguous = sortedUniqueStrings(ambiguous)
	var secretPaths []string
	var generatedPaths []string
	for _, path := range sortedUniquePlanCommitPaths(paths) {
		if suspectedSecretPath(path) {
			secretPaths = append(secretPaths, path)
		}
		if generatedPath(path) {
			generatedPaths = append(generatedPaths, path)
		}
	}
	if len(ambiguous) == 0 && len(secretPaths) == 0 && len(generatedPaths) == 0 {
		return nil
	}
	var parts []string
	if len(ambiguous) > 0 {
		parts = append(parts, commitSafetyListLabel(len(ambiguous), "ambiguous git status entry", "ambiguous git status entries")+": "+strings.Join(ambiguous, ", "))
	}
	if len(secretPaths) > 0 {
		parts = append(parts, commitSafetyListLabel(len(secretPaths), "suspected secret path", "suspected secret paths")+": "+strings.Join(secretPaths, ", "))
	}
	if len(generatedPaths) > 0 {
		parts = append(parts, commitSafetyListLabel(len(generatedPaths), "generated artifact path", "generated artifact paths")+": "+strings.Join(generatedPaths, ", "))
	}
	return fmt.Errorf("commit safety checks refused to auto-commit file(s): %s; review and commit manually", strings.Join(parts, "; "))
}

func sortedUniquePlanCommitPaths(paths []string) []string {
	unique := map[string]bool{}
	for _, path := range paths {
		path = normalizePlanCommitPath(path)
		if path != "." && path != "" {
			unique[path] = true
		}
	}
	return sortedPathSet(unique)
}

func sortedUniqueStrings(values []string) []string {
	unique := map[string]bool{}
	for _, value := range values {
		if value != "" {
			unique[value] = true
		}
	}
	return sortedPathSet(unique)
}

func commitSafetyListLabel(count int, singular string, plural string) string {
	if count == 1 {
		return singular
	}
	return plural
}
