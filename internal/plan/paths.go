package plan

import (
	"path"
	"slices"
	"strings"
	"unicode"
)

// PathsOverlap reports whether file matches an expected file or directory prefix.
func PathsOverlap(file string, expected string) bool {
	cleanFile := normalizePlanPath(file)
	cleanExpected := normalizePlanPath(expected)
	if cleanFile == "" || cleanExpected == "" {
		return false
	}
	if cleanFile == cleanExpected {
		return true
	}
	if strings.HasSuffix(strings.TrimSpace(expected), "/") {
		return strings.HasPrefix(cleanFile, cleanExpected+"/")
	}
	if path.Ext(cleanExpected) == "" && strings.HasPrefix(cleanFile, cleanExpected+"/") {
		return true
	}
	return false
}

// NormalizeReviewFindingPath cleans an untrusted repository-relative review path.
func NormalizeReviewFindingPath(value string) (string, bool) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	for strings.HasPrefix(value, "./") {
		value = strings.TrimPrefix(value, "./")
	}
	if value == "" || strings.HasPrefix(value, "/") || hasWindowsDrivePrefix(value) || hasParentPathSegment(value) || hasWildcardPathSegment(value) {
		return "", false
	}
	clean := path.Clean(value)
	if clean == "." || clean == "" || clean == "..." || strings.HasPrefix(clean, "../") || strings.HasSuffix(clean, "/...") || strings.Contains(clean, "/.../") {
		return "", false
	}
	return clean, true
}

func normalizePlanPath(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	value = strings.TrimPrefix(value, "./")
	value = strings.Trim(value, "/")
	if value == "" {
		return ""
	}
	clean := path.Clean(value)
	if clean == "." {
		return ""
	}
	return clean
}

func hasWindowsDrivePrefix(value string) bool {
	return len(value) >= 2 && value[1] == ':' && unicode.IsLetter(rune(value[0]))
}

func hasParentPathSegment(value string) bool {
	return slices.Contains(strings.Split(value, "/"), "..")
}

func hasWildcardPathSegment(value string) bool {
	return strings.ContainsAny(value, "*?[]{}") || value == "..." || strings.HasPrefix(value, ".../") || strings.HasSuffix(value, "/...") || strings.Contains(value, "/.../")
}
