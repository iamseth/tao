package insights

import (
	"strings"

	"github.com/iamseth/tao/internal/plan"
)

// blockedEvidence retains only a bounded display exemplar and a fixed category;
// commands, paths, heads and fingerprints never enter the report projection.
type blockedEvidence struct {
	category string
	exemplar string
}

func blockedEvent(event plan.Event) blockedEvidence {
	reason := eventReason(event)
	category := classifyBlocked(event.Command, event.Paths, reason)
	reason = routingText(reason)
	if len(reason) > 1024 {
		reason = strings.ToValidUTF8(reason[:1024], "")
	}
	return blockedEvidence{category: category, exemplar: reason}
}

func classifyBlocked(command string, paths []string, reason string) string {
	family := blockedCommandFamily(command)
	if family == "" {
		return NormalizeBlockedReason(reason)
	}
	if allTestPaths(paths) {
		return family + "_test_files"
	}
	return family + "_failure"
}

// Recognize only literal golangci-lint run, go test/build, and make lint/test/build.
// This is deliberately not a shell parser: quotes, substitutions, assignments,
// newlines, wrappers and composite commands are unsupported, as is make verify.
func blockedCommandFamily(command string) string {
	if len(command) > 4096 {
		return ""
	}
	for _, r := range command {
		if !blockedASCIIAlphanumeric(r) && !strings.ContainsRune(" \t-_=./,:", r) {
			return ""
		}
	}
	words := strings.Fields(command)
	if len(words) < 2 {
		return ""
	}
	family := ""
	switch words[0] {
	case "golangci-lint":
		if words[1] == "run" {
			family = "lint"
		}
	case "go":
		if words[1] == "test" || words[1] == "build" {
			family = words[1]
		}
	case "make":
		if words[1] == "lint" || words[1] == "test" || words[1] == "build" {
			family = words[1]
		}
	}
	for _, arg := range words[2:] {
		if arg == "go" || arg == "make" || arg == "golangci-lint" {
			return ""
		}
		// Make options can select another file/directory or additional targets.
		// Only the explicit single-target invocation is supported.
		if words[0] == "make" {
			return ""
		}
	}
	return family
}

func blockedASCIIAlphanumeric(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

// Test-only means every supplied path is a lexical relative _test.go file.
// It says nothing about ownership, worktree scope, cache state or root cause.
func allTestPaths(paths []string) bool {
	if len(paths) == 0 || len(paths) > 256 {
		return false
	}
	for _, raw := range paths {
		if len(raw) == 0 || len(raw) > 1024 {
			return false
		}
		path := strings.ReplaceAll(raw, "\\", "/")
		for _, r := range path {
			if !blockedASCIIAlphanumeric(r) && !strings.ContainsRune("_./-", r) {
				return false
			}
		}
		for _, part := range strings.Split(path, "/") {
			if part == "" || part == ".." {
				return false
			}
		}
		if !strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, "/_test.go") || path == "_test.go" {
			return false
		}
	}
	return true
}

// NormalizeBlockedReason is the conservative historical text-only fallback.
// Unrecognized and neutral prose collapses to a single counted bucket.
func NormalizeBlockedReason(message string) string {
	normalized := strings.ToLower(strings.Join(strings.Fields(message), " "))
	if containsAny(" "+normalized+" ", " not ", " no ", " without ", " never ", " isn't ", " wasn't ", " aren't ", " doesn't ") {
		return "other"
	}
	switch {
	case containsAny(normalized, "connection refused", "service unreachable", "service is unreachable", "network unavailable", "service unavailable"):
		return "unreachable_service"
	case containsAny(normalized, "unrelated failure", "unrelated test failure", "pre-existing lint failure", "pre-existing failure", "preexisting failure"):
		return "unrelated_failure"
	case containsAny(normalized, "invalid verification command", "verification command is invalid", "invalid command"):
		return "invalid_verification_command"
	case containsAny(normalized, "timed out", "timeout exceeded", "session timeout") || normalized == "timeout":
		return "timeout"
	case containsAny(normalized, "dependency missing", "missing dependency", "dependency unavailable", "dependency failed", "prerequisite missing", "missing prerequisite"):
		return "dependency"
	default:
		return "other"
	}
}
