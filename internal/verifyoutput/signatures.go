// Package verifyoutput provides presentation-only line selection over untrusted
// verification output. It is never lifecycle or recovery authority.
package verifyoutput

import (
	"path"
	"regexp"
	"strings"
)

var (
	failingTestLine    = regexp.MustCompile(`^[ \t]*--- FAIL: ([^ \t(]+)`)
	failingPackageLine = regexp.MustCompile(`^FAIL[ \t]+([^ \t]+)(?:[ \t]+(?:\[build failed\]|\[?[0-9.]+s\]?))?[ \t]*$`)
	failingPathLine    = regexp.MustCompile(`^([^ \t:]+\.go):[0-9]+(?::[0-9]+)?:`)
)

// FailingTests returns up to 64 distinct failing test names in output order.
func FailingTests(output string) []string {
	return signatures(output, failingTestLine, false)
}

// FailingPackages returns up to 64 distinct failing import paths in output order.
func FailingPackages(output string) []string {
	return signatures(output, failingPackageLine, false)
}

// FailingPaths returns up to 64 distinct repository-relative diagnostic paths.
func FailingPaths(output string) []string {
	return signatures(output, failingPathLine, true)
}

func signatures(output string, pattern *regexp.Regexp, cleanPath bool) []string {
	var result []string
	seen := make(map[string]bool)
	for line := range strings.SplitSeq(output, "\n") {
		match := pattern.FindStringSubmatch(strings.TrimSuffix(line, "\r"))
		if match == nil {
			continue
		}
		value := match[1]
		if cleanPath {
			value = path.Clean(value)
			if path.IsAbs(value) || value == ".." || strings.HasPrefix(value, "../") || strings.ContainsAny(value, `\:`) {
				continue
			}
		}
		if seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
		if len(result) == 64 {
			break
		}
	}
	return result
}
