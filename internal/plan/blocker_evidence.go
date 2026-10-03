package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"path"
	"slices"
	"strings"
)

const maxBlockerPaths = 64

// LatestPlanOwnedBlocker returns the last qualifying slice block in event-log
// order. Prose-only blocks never establish plan ownership.
func LatestPlanOwnedBlocker(detail *PlanDetail, sliceID string) *Event {
	return latestSliceBlockedEvent(detail, sliceID, func(event *Event) bool {
		return event.BlockerClassification == BlockerClassificationPlanOwned && event.HeadSHA != "" && event.Fingerprint != ""
	})
}

// LatestSliceBlockedEvent returns the most recent slice block in event-log order.
func LatestSliceBlockedEvent(detail *PlanDetail, sliceID string) *Event {
	return latestSliceBlockedEvent(detail, sliceID, nil)
}

func latestSliceBlockedEvent(detail *PlanDetail, sliceID string, qualifies func(*Event) bool) *Event {
	if detail == nil {
		return nil
	}
	for i := len(detail.Events) - 1; i >= 0; i-- {
		event := &detail.Events[i]
		if event.Type == EventTypeSliceBlocked && event.SliceID == sliceID &&
			(qualifies == nil || qualifies(event)) {
			return event
		}
	}
	return nil
}

// PlanOwnershipBase returns the recorded base, without inferring one for legacy plans.
func PlanOwnershipBase(detail *PlanDetail) string {
	if detail == nil {
		return ""
	}
	return strings.TrimSpace(detail.State.Repo.BaseCommit)
}

// ClassifyBlockerPaths requires every failing path to belong to the caller's
// ownership set. Neither set is truncated to the event storage bound here.
func ClassifyBlockerPaths(failing []string, planOwned []string) bool {
	if len(failing) == 0 {
		return false
	}
	owned := make(map[string]struct{}, len(planOwned))
	for _, value := range planOwned {
		if normalized := normalizeBlockerPath(value); normalized != "" {
			owned[normalized] = struct{}{}
		}
	}
	for _, value := range failing {
		normalized := normalizeBlockerPath(value)
		if normalized == "" {
			return false
		}
		if _, ok := owned[normalized]; !ok {
			return false
		}
	}
	return true
}

// WorktreeFingerprint hashes ordered, NUL-separated caller-provided evidence.
// It does not inspect Git or the filesystem and conveys no ownership itself.
func WorktreeFingerprint(parts ...string) string {
	if !slices.ContainsFunc(parts, func(part string) bool { return part != "" }) {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func normalizeBlockerPaths(values []string) []string {
	var normalized []string
	for _, value := range values {
		if name := normalizeBlockerPath(value); name != "" {
			normalized = append(normalized, name)
		}
	}
	slices.Sort(normalized)
	normalized = slices.Compact(normalized)
	if len(normalized) > maxBlockerPaths {
		normalized = normalized[:maxBlockerPaths]
	}
	return normalized
}

func normalizeBlockerPath(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" || strings.HasPrefix(value, "/") || expectedFileHasWindowsDrivePrefix(value) ||
		strings.ContainsFunc(value, func(r rune) bool { return r < ' ' || r == '\x7f' }) {
		return ""
	}
	value = path.Clean(value)
	if value == "." || value == ".." || strings.HasPrefix(value, "../") {
		return ""
	}
	return value
}
