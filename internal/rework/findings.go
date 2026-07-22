package rework

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/iamseth/tao/internal/plan"
)

var reviewJSONBlockRE = regexp.MustCompile("(?s)```\\s*tao-review-json\\s*(.*?)\\s*```")

// ReviewFindings returns persisted structured findings, falling back to review.md JSON.
func ReviewFindings(detail *plan.PlanDetail) []plan.ReviewFinding {
	if detail == nil {
		return nil
	}
	review := plan.PersistedReview(detail)
	if review != nil && len(review.Findings) > 0 {
		return cloneFindings(review.Findings)
	}
	return ParseReviewFindings(detail.Review.Content)
}

// ParseReviewFindings extracts findings from the last tao-review-json fenced block.
func ParseReviewFindings(content string) []plan.ReviewFinding {
	matches := reviewJSONBlockRE.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 || len(matches[len(matches)-1]) != 2 {
		return nil
	}
	var payload struct {
		Findings []plan.ReviewFinding `json:"findings"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(matches[len(matches)-1][1])), &payload); err != nil {
		return nil
	}
	return cloneFindings(payload.Findings)
}

// NormalizeFindings returns a canonical finding set suitable for comparison.
// Line numbers and input order are intentionally ignored.
func NormalizeFindings(findings []plan.ReviewFinding) []plan.ReviewFinding {
	normalized := make([]plan.ReviewFinding, 0, len(findings))
	for _, finding := range findings {
		normalized = append(normalized, plan.ReviewFinding{
			Severity:   strings.TrimSpace(finding.Severity),
			File:       strings.TrimSpace(finding.File),
			Message:    strings.TrimSpace(finding.Message),
			Suggestion: strings.TrimSpace(finding.Suggestion),
		})
	}
	slices.SortFunc(normalized, func(a, b plan.ReviewFinding) int {
		return strings.Compare(batchLocationFindingKey(a), batchLocationFindingKey(b))
	})
	return normalized
}

const reworkFingerprintV2Prefix = "rework:v2:"

// ReworkFindingsFingerprint returns a versioned, deterministic digest of the complete
// normalized finding set. The version prefix keeps persisted v2 identities distinct
// from historical location-oriented fingerprints without requiring migration.
func ReworkFindingsFingerprint(findings []plan.ReviewFinding) string {
	keys := make([]string, 0, len(findings))
	for _, finding := range findings {
		keys = append(keys, reworkFindingKey(finding))
	}
	slices.Sort(keys)

	hash := sha256.New()
	_, _ = hash.Write([]byte(reworkFingerprintV2Prefix))
	previousKey := ""
	hasPrevious := false
	for _, key := range keys {
		if hasPrevious && key == previousKey {
			continue
		}
		_, _ = hash.Write([]byte(key))
		_, _ = hash.Write([]byte{0})
		previousKey = key
		hasPrevious = true
	}
	return reworkFingerprintV2Prefix + hex.EncodeToString(hash.Sum(nil))
}

// BatchLocationFindingsFingerprint returns the historical location-oriented digest
// used by batch convergence. The name distinguishes its coarse recurring-location
// semantics from high-confidence rework finding identity.
func BatchLocationFindingsFingerprint(findings []plan.ReviewFinding) string {
	hash := sha256.New()
	previousKey := ""
	hasPrevious := false
	for _, finding := range NormalizeFindings(findings) {
		key := batchLocationFindingKey(finding)
		if hasPrevious && key == previousKey {
			continue
		}
		_, _ = hash.Write([]byte(key))
		_, _ = hash.Write([]byte{0})
		previousKey = key
		hasPrevious = true
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// batchLocationFindingKey identifies a finding by trimmed severity and normalized file path.
// Message and Suggestion are deliberately excluded for historical batch compatibility.
func batchLocationFindingKey(finding plan.ReviewFinding) string {
	return strings.TrimSpace(finding.Severity) + "\x00" + normalizePlanPath(finding.File)
}

func reworkFindingKey(finding plan.ReviewFinding) string {
	return strings.Join([]string{
		normalizeFindingText(finding.Severity),
		normalizePlanPath(finding.File),
		strconv.Itoa(finding.Line),
		normalizeFindingText(finding.Message),
		normalizeFindingText(finding.Suggestion),
	}, "\x00")
}

func normalizeFindingText(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func cloneFindings(findings []plan.ReviewFinding) []plan.ReviewFinding {
	if len(findings) == 0 {
		return nil
	}
	clone := make([]plan.ReviewFinding, len(findings))
	copy(clone, findings)
	return clone
}
