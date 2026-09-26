package steal

import (
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var slugSeparators = regexp.MustCompile(`[^a-z0-9]+`)

func sourceSlug(source Source) string {
	text := strings.ToLower(source.Host) + "-" + strings.TrimSuffix(strings.ToLower(source.Path), ".git")
	return strings.Trim(slugSeparators.ReplaceAllString(text, "-"), "-")
}

// CampaignSlug returns an at-most-80-byte tag with an intact UTC date suffix.
func CampaignSlug(source Source, day time.Time) string {
	suffix := "-" + day.UTC().Format("2006-01-02")
	prefix := "steal-" + sourceSlug(source)
	if len(prefix) > 80-len(suffix) {
		prefix = prefix[:80-len(suffix)]
	}
	return strings.TrimRight(prefix, "-") + suffix
}

// ScratchDir derives a local snapshot path; Fetch reserves it without overwriting.
func ScratchDir(dataHome string, source Source, now time.Time) string {
	return filepath.Join(dataHome, "steal", sourceSlug(source)+"-"+now.UTC().Format("20060102-150405"))
}
