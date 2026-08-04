// Package planreport builds share-safe projections and Markdown reports from plan data.
package planreport

import (
	"bytes"
	"errors"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const defaultTextLimit = 4096

// Section identifies a fixed report section for disclosure accounting.
type Section string

// DisclosureCategory describes a safety transformation without retaining its input.
type DisclosureCategory string

const (
	DisclosureNormalized DisclosureCategory = "normalized"
	DisclosureOmitted    DisclosureCategory = "omitted"
	DisclosureRedacted   DisclosureCategory = "redacted"
	DisclosureTruncated  DisclosureCategory = "truncated"
)

// Disclosure is an aggregate safety transformation. It deliberately contains no
// source text or other value-derived detail.
type Disclosure struct {
	Section  Section
	Category DisclosureCategory
	Count    int
}

// SafeText can only be created by Sanitizer. Its contents remain private to the
// report package so renderers cannot accidentally accept untrusted strings.
type SafeText struct {
	text string
}

type disclosureKey struct {
	section  Section
	category DisclosureCategory
}

// Sanitizer owns the conversion of untrusted prose into report-safe text.
type Sanitizer struct {
	limit       int
	disclosures map[disclosureKey]int
}

// NewSanitizer creates a sanitizer. Non-positive limits use a conservative
// package default.
func NewSanitizer(maxRunes int) *Sanitizer {
	if maxRunes <= 0 {
		maxRunes = defaultTextLimit
	}
	return &Sanitizer{limit: maxRunes, disclosures: make(map[disclosureKey]int)}
}

// Disclosures returns stable aggregates ordered by section and category.
func (s *Sanitizer) Disclosures() []Disclosure {
	out := make([]Disclosure, 0, len(s.disclosures))
	for key, count := range s.disclosures {
		out = append(out, Disclosure{Section: key.section, Category: key.category, Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Section != out[j].Section {
			return out[i].Section < out[j].Section
		}
		return out[i].Category < out[j].Category
	})
	return out
}

func (s *Sanitizer) disclose(section Section, category DisclosureCategory, count int) {
	if count > 0 {
		s.disclosures[disclosureKey{section: section, category: category}] += count
	}
}

var (
	privateKeyStart  = regexp.MustCompile(`(?i)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`)
	credentialURL    = regexp.MustCompile(`(?i)(?:\b[A-Z][A-Z0-9+.-]{0,31}:)?//[^\s/@:]+:[^\s/@]+@[^\s]+`)
	emailAddress     = regexp.MustCompile(`(?i)\b[A-Z0-9._%+\-]+@[A-Z0-9.\-]+\.[A-Z]{2,63}\b`)
	awsKey           = regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`)
	githubToken      = regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}\b`)
	slackToken       = regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}\b`)
	jwtToken         = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`)
	bearerToken      = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]{12,}`)
	secretSetting    = regexp.MustCompile(`(?i)\b(password|passwd|pwd|api[_-]?key|access[_-]?token|auth[_-]?token|secret)\s*([:=])\s*([^\s,;]+)`)
	phoneCandidate   = regexp.MustCompile(`(?:\+?\d[\d .()\-]{7,}\d)`)
	taoPlanTimestamp = regexp.MustCompile(`^\d{8}-\d{6}$`)

	activeMarkdownLine         = regexp.MustCompile("(?m)^[ \\t]*(?:#{1,6}[ \\t]|>|[-+*][ \\t]|\\d+[.)][ \\t]|```|~~~)")
	markdownLink               = regexp.MustCompile(`!?\[[^\]]*\]\([^)]*\)`)
	canonicalPlaceholder       = regexp.MustCompile(`\\\[(?:credential|credential URL|email|phone) redacted\\\]`)
	canonicalCredentialSetting = regexp.MustCompile(`(?i)\b(?:password|passwd|pwd|api[_-]?key|access[_-]?token|auth[_-]?token|secret)\s*[:=]\s*\\\[credential redacted\\\]`)
)

type redactionRule struct {
	re       *regexp.Regexp
	label    string
	preserve func([]string) string
}

// Sanitize converts source prose to bounded inert Markdown text. Standalone
// sensitive values and private-key material are omitted rather than retained as
// placeholders because they have no safe surrounding context.
func (s *Sanitizer) Sanitize(section Section, source string) SafeText {
	text, normalized := normalizeText(source)
	s.disclose(section, DisclosureNormalized, normalized)

	if privateKeyStart.MatchString(text) || isStandaloneSensitive(text) {
		s.disclose(section, DisclosureOmitted, 1)
		return SafeText{}
	}

	rules := []redactionRule{
		{credentialURL, "[credential URL redacted]", nil},
		{awsKey, "[credential redacted]", nil},
		{githubToken, "[credential redacted]", nil},
		{slackToken, "[credential redacted]", nil},
		{jwtToken, "[credential redacted]", nil},
		{bearerToken, "[credential redacted]", nil},
		{secretSetting, "", func(parts []string) string { return parts[1] + parts[2] + " [credential redacted]" }},
		{emailAddress, "[email redacted]", nil},
	}
	for _, rule := range rules {
		var count int
		if rule.preserve == nil {
			count = len(rule.re.FindAllStringIndex(text, -1))
			text = rule.re.ReplaceAllString(text, rule.label)
		} else {
			text = rule.re.ReplaceAllStringFunc(text, func(match string) string {
				count++
				return rule.preserve(rule.re.FindStringSubmatch(match))
			})
		}
		s.disclose(section, DisclosureRedacted, count)
	}

	text, phoneCount := replacePhones(text)
	s.disclose(section, DisclosureRedacted, phoneCount)

	text = escapeMarkdown(text)
	text, truncated := truncateRunes(text, s.limit)
	if truncated {
		s.disclose(section, DisclosureTruncated, 1)
	}
	return SafeText{text: text}
}

func normalizeText(source string) (string, int) {
	count := 0
	if !utf8.ValidString(source) {
		source = strings.ToValidUTF8(source, "�")
		count++
	}
	source = strings.ReplaceAll(source, "\r\n", "\n")
	source = strings.ReplaceAll(source, "\r", "\n")
	var b strings.Builder
	for _, r := range source {
		switch {
		case r == '\n':
			b.WriteRune(r)
		case r == '\t':
			b.WriteByte(' ')
			count++
		case r < 0x20 || r == 0x7f:
			b.WriteRune('�')
			count++
		default:
			b.WriteRune(r)
		}
	}
	return b.String(), count
}

func isStandaloneSensitive(text string) bool {
	trimmed := strings.TrimSpace(text)
	for _, re := range []*regexp.Regexp{credentialURL, emailAddress, awsKey, githubToken, slackToken, jwtToken, bearerToken} {
		loc := re.FindStringIndex(trimmed)
		if loc != nil && loc[0] == 0 && loc[1] == len(trimmed) {
			return true
		}
	}
	matches := phoneCandidate.FindAllStringIndex(trimmed, -1)
	return len(matches) == 1 && matches[0][0] == 0 && matches[0][1] == len(trimmed) && plausiblePhone(trimmed)
}

func replacePhones(text string) (string, int) {
	count := 0
	text = phoneCandidate.ReplaceAllStringFunc(text, func(match string) string {
		if !plausiblePhone(match) {
			return match
		}
		count++
		return "[phone redacted]"
	})
	return text, count
}

func plausiblePhone(value string) bool {
	// Tao plan IDs begin with YYYYMMDD-HHMMSS. The phone candidate stops at
	// the following slug, so exclude that timestamp-shaped prefix explicitly.
	if taoPlanTimestamp.MatchString(value) {
		return false
	}
	digits := 0
	for _, r := range value {
		if r >= '0' && r <= '9' {
			digits++
		}
	}
	return digits >= 10 && digits <= 15
}

func escapeMarkdown(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		switch r {
		case '\\', '`', '*', '_', '[', ']', '<', '>', '!':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	text = b.String()
	return activeMarkdownLine.ReplaceAllStringFunc(text, func(line string) string {
		marker := 0
		for marker < len(line) && (line[marker] == ' ' || line[marker] == '\t') {
			marker++
		}
		return line[:marker] + "\\" + line[marker:]
	})
}

func truncateRunes(text string, limit int) (string, bool) {
	if utf8.RuneCountInString(text) <= limit {
		return text, false
	}
	runes := []rune(text)
	return string(runes[:limit]), true
}

var errUnsafeDocument = errors.New("plan report failed final safety scan")

// ValidateDocument performs the fail-closed scan required immediately before a
// report is written. Its error intentionally contains no matched source value.
func ValidateDocument(document []byte) error {
	if !utf8.Valid(document) || bytes.IndexByte(document, 0) >= 0 {
		return errUnsafeDocument
	}
	text := string(document)
	for _, r := range text {
		if (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f {
			return errUnsafeDocument
		}
	}
	scannable := canonicalCredentialSetting.ReplaceAllString(text, "")
	scannable = canonicalPlaceholder.ReplaceAllString(scannable, "")
	for _, re := range []*regexp.Regexp{privateKeyStart, credentialURL, emailAddress, awsKey, githubToken, slackToken, jwtToken, bearerToken, secretSetting} {
		if re.MatchString(scannable) {
			return errUnsafeDocument
		}
	}
	for _, candidate := range phoneCandidate.FindAllString(scannable, -1) {
		if plausiblePhone(candidate) {
			return errUnsafeDocument
		}
	}
	if containsUnescaped(text, '<') || containsUnescaped(text, '>') || activeMarkdownLine.MatchString(text) || hasUnescapedMatch(text, markdownLink) {
		return errUnsafeDocument
	}
	return nil
}

func containsUnescaped(text string, target byte) bool {
	for i := 0; i < len(text); i++ {
		if text[i] == target && !isEscaped(text, i) {
			return true
		}
	}
	return false
}

func hasUnescapedMatch(text string, re *regexp.Regexp) bool {
	for _, loc := range re.FindAllStringIndex(text, -1) {
		if !isEscaped(text, loc[0]) {
			return true
		}
	}
	return false
}

func isEscaped(text string, index int) bool {
	backslashes := 0
	for index > 0 && text[index-1] == '\\' {
		backslashes++
		index--
	}
	return backslashes%2 == 1
}
