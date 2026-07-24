// Package commit owns Tao's validated commit-message and commit-safety contract.
package commit

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// Proposal is the untrusted, structured portion of a commit message. Tao
// evidence is deliberately absent; callers add it separately as trusted
// trailers when formatting the final message.
type Proposal struct {
	Type    string `json:"type"`
	Scope   string `json:"scope"`
	Summary string `json:"summary"`
	What    string `json:"what"`
	Why     string `json:"why"`
}

// TrustedTrailer is evidence supplied by Tao's Go workflows, never by an
// untrusted proposal. Its fields are private so every value is validated.
type TrustedTrailer struct {
	key   string
	value string
}

var (
	scopePattern   = regexp.MustCompile(`^[a-z0-9-]+$`)
	subjectPattern = regexp.MustCompile(`^([a-z]+)\(([a-z0-9-]+)\): (.+)$`)
	summaryVerb    = regexp.MustCompile(`^[a-z][a-z-]*$`)
	trailerPattern = regexp.MustCompile(`^Tao-[A-Za-z][A-Za-z0-9-]*$`)
)

var supportedTypes = map[string]bool{
	"feat": true, "fix": true, "docs": true, "style": true,
	"refactor": true, "perf": true, "test": true, "build": true,
	"ci": true, "chore": true, "revert": true,
}

// NewTrustedTrailer validates Tao-owned evidence for a final message.
func NewTrustedTrailer(key, value string) (TrustedTrailer, error) {
	if !trailerPattern.MatchString(key) {
		return TrustedTrailer{}, fmt.Errorf("trusted trailer key %q must match Tao-<name>", key)
	}
	if value == "" || value != strings.TrimSpace(value) || strings.ContainsAny(value, "\r\n") {
		return TrustedTrailer{}, fmt.Errorf("trusted trailer %s requires a non-empty single-line value", key)
	}
	return TrustedTrailer{key: key, value: value}, nil
}

// ValidateProposal checks the complete untrusted proposal contract.
func ValidateProposal(proposal Proposal) error {
	if !supportedTypes[proposal.Type] {
		return fmt.Errorf("unsupported commit type %q", proposal.Type)
	}
	if proposal.Scope == "" || proposal.Scope != strings.ToLower(proposal.Scope) || !scopePattern.MatchString(proposal.Scope) {
		return fmt.Errorf("scope must contain only lowercase letters, digits, and hyphens")
	}
	if err := validateSummary(proposal.Summary); err != nil {
		return err
	}
	if err := validateBodyPart("what", proposal.What); err != nil {
		return err
	}
	if err := validateBodyPart("why", proposal.Why); err != nil {
		return err
	}
	if slices.ContainsFunc([]string{proposal.Type, proposal.Scope, proposal.Summary, proposal.What, proposal.Why}, containsReservedTrailerLine) {
		return fmt.Errorf("proposal must not contain reserved Tao-* lines")
	}
	return nil
}

func validateSummary(summary string) error {
	if summary == "" || summary != strings.TrimSpace(summary) || strings.ContainsAny(summary, "\r\n") {
		return fmt.Errorf("summary must be a non-empty single line")
	}
	if utf8.RuneCountInString(summary) > 72 {
		return fmt.Errorf("summary must be 72 characters or fewer")
	}
	if summary != strings.ToLower(summary) {
		return fmt.Errorf("summary must be lowercase")
	}
	if strings.ContainsAny(summary[len(summary)-1:], ".!?") {
		return fmt.Errorf("summary must not end with punctuation")
	}
	first, _, _ := strings.Cut(summary, " ")
	if !summaryVerb.MatchString(first) {
		return fmt.Errorf("summary must start with an imperative verb")
	}
	nonImperative := map[string]bool{
		"added": true, "adds": true, "adding": true,
		"created": true, "creates": true, "creating": true,
		"fixed": true, "fixes": true, "fixing": true,
		"implemented": true, "implements": true, "implementing": true,
		"updated": true, "updates": true, "updating": true,
	}
	if nonImperative[first] {
		return fmt.Errorf("summary must use an imperative verb")
	}
	return nil
}

func validateBodyPart(name, value string) error {
	if value == "" || value != strings.TrimSpace(value) {
		return fmt.Errorf("%s body must be non-empty and trimmed", name)
	}
	if strings.ContainsRune(value, '\r') {
		return fmt.Errorf("%s body must use LF line endings", name)
	}
	if strings.Contains(value, "\n\nWhat:\n") || strings.Contains(value, "\n\nWhy:\n") {
		return fmt.Errorf("%s body must not contain canonical section markers", name)
	}
	return nil
}

func containsReservedTrailerLine(value string) bool {
	for line := range strings.SplitSeq(value, "\n") {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "tao-") {
			return true
		}
	}
	return false
}

// ValidateProposalMessage validates an untrusted subject/body pair in the
// canonical message format. Proposal messages cannot supply Tao evidence;
// workflows append trusted trailers only after this boundary.
func ValidateProposalMessage(subject, body string) error {
	if containsReservedTrailerLine(subject) || containsReservedTrailerLine(body) {
		return fmt.Errorf("proposal must not contain reserved Tao-* lines")
	}
	return ValidateMessage(subject + "\n\n" + body)
}

// Format validates a proposal and appends only separately supplied trusted
// trailers. Its return value is the exact message workflows must persist for
// intent and recovery comparisons.
func Format(proposal Proposal, trailers ...TrustedTrailer) (string, error) {
	if err := ValidateProposal(proposal); err != nil {
		return "", err
	}
	message := fmt.Sprintf("%s(%s): %s\n\nWhat:\n%s\n\nWhy:\n%s", proposal.Type, proposal.Scope, proposal.Summary, proposal.What, proposal.Why)
	return appendTrustedTrailers(message, trailers...)
}

// FormatProposalMessage validates an already-canonical untrusted subject/body
// pair and appends only separately supplied Tao-owned evidence trailers.
func FormatProposalMessage(subject, body string, trailers ...TrustedTrailer) (string, error) {
	if err := ValidateProposalMessage(subject, body); err != nil {
		return "", err
	}
	return appendTrustedTrailers(subject+"\n\n"+body, trailers...)
}

func appendTrustedTrailers(base string, trailers ...TrustedTrailer) (string, error) {
	var message strings.Builder
	message.WriteString(base)
	if len(trailers) > 0 {
		message.WriteString("\n\n")
		for i, trailer := range trailers {
			if !trailerPattern.MatchString(trailer.key) || trailer.value == "" || trailer.value != strings.TrimSpace(trailer.value) || strings.ContainsAny(trailer.value, "\r\n") {
				return "", fmt.Errorf("invalid trusted trailer")
			}
			if i > 0 {
				message.WriteByte('\n')
			}
			fmt.Fprintf(&message, "%s: %s", trailer.key, trailer.value)
		}
	}
	return message.String(), nil
}

// ValidateMessage validates a fully formatted message, including any trusted
// Tao evidence trailer paragraph.
func ValidateMessage(message string) error {
	if message == "" || message != strings.TrimSpace(message) || strings.ContainsRune(message, '\r') {
		return fmt.Errorf("commit message must be non-empty, trimmed, and use LF line endings")
	}
	const whatMarker = "\n\nWhat:\n"
	const whyMarker = "\n\nWhy:\n"
	whatAt := strings.Index(message, whatMarker)
	if whatAt < 0 {
		return fmt.Errorf("commit message requires a What section")
	}
	whyAtRelative := strings.Index(message[whatAt+len(whatMarker):], whyMarker)
	if whyAtRelative < 0 {
		return fmt.Errorf("commit message requires a Why section")
	}
	whyAt := whatAt + len(whatMarker) + whyAtRelative
	subject := message[:whatAt]
	match := subjectPattern.FindStringSubmatch(subject)
	if match == nil {
		return fmt.Errorf("subject must match <type>(<scope>): <summary>")
	}
	what := message[whatAt+len(whatMarker) : whyAt]
	whyAndTrailers := message[whyAt+len(whyMarker):]
	why := whyAndTrailers
	var trailers []TrustedTrailer
	if paragraph := strings.LastIndex(whyAndTrailers, "\n\n"); paragraph >= 0 {
		candidate := whyAndTrailers[paragraph+2:]
		parsed, ok := parseTrustedTrailers(candidate)
		if ok {
			why = whyAndTrailers[:paragraph]
			trailers = parsed
		}
	}
	proposal := Proposal{Type: match[1], Scope: match[2], Summary: match[3], What: what, Why: why}
	formatted, err := Format(proposal, trailers...)
	if err != nil {
		return err
	}
	if formatted != message {
		return fmt.Errorf("commit message is not in canonical format")
	}
	return nil
}

func parseTrustedTrailers(paragraph string) ([]TrustedTrailer, bool) {
	lines := strings.Split(paragraph, "\n")
	trailers := make([]TrustedTrailer, 0, len(lines))
	for _, line := range lines {
		key, value, found := strings.Cut(line, ": ")
		if !found {
			return nil, false
		}
		trailer, err := NewTrustedTrailer(key, value)
		if err != nil {
			return nil, false
		}
		trailers = append(trailers, trailer)
	}
	return trailers, len(trailers) > 0
}

func messageSubject(message string) string {
	subject, _, _ := strings.Cut(message, "\n")
	return subject
}
