package commit

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

const maxVerificationCommandTrailerBytes = 1024

// VerificationTrailers encodes one Tao-observed attempt as an ordered triple.
// Callers supply a one-based attempt index (including corrected attempts, not
// merely the declaration index). Every value carries that association. Only
// the command preview is truncated; its SHA-256 covers the unabridged bytes.
// This is display escaping, not secrets redaction. Cwd and output are excluded.
func VerificationTrailers(attemptIndex int, command string, exitCode *int, digest string) ([]TrustedTrailer, error) {
	if attemptIndex < 1 {
		return nil, fmt.Errorf("verification attempt index must be one-based")
	}
	if strings.TrimSpace(command) == "" {
		return nil, fmt.Errorf("verification command is required")
	}
	if len(digest) != 64 || digest != strings.ToLower(digest) {
		return nil, fmt.Errorf("verification digest must be 64 lowercase hex characters")
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return nil, fmt.Errorf("invalid verification digest: %w", err)
	}
	if exitCode != nil && *exitCode < 0 {
		return nil, fmt.Errorf("unknown verification exit must be nil")
	}
	association := strconv.Itoa(attemptIndex) + ": "
	prefix := fmt.Sprintf("%ssha256=%x ", association, sha256.Sum256([]byte(command)))
	commandValue := prefix + verificationCommandPreview(command, maxVerificationCommandTrailerBytes-len(prefix))
	exit := "unknown"
	if exitCode != nil {
		exit = strconv.Itoa(*exitCode)
	}
	fields := []struct{ key, value string }{
		{"Tao-Verify-Command", commandValue},
		{"Tao-Verify-Exit", association + exit},
		{"Tao-Verify-Digest", association + digest},
	}
	trailers := make([]TrustedTrailer, 0, len(fields))
	for _, field := range fields {
		trailer, err := NewTrustedTrailer(field.key, field.value)
		if err != nil {
			return nil, err
		}
		trailers = append(trailers, trailer)
	}
	return trailers, nil
}

// Bound allocation as well as the final display. Keep whole escape sequences
// and UTF-8 characters, quoting even malformed input bytes without raw controls.
func verificationCommandPreview(command string, budget int) string {
	const marker = " [truncated]"
	var escaped strings.Builder
	ends := []int{0}
	offset := 0
	for offset < len(command) {
		_, size := utf8.DecodeRuneInString(command[offset:])
		quoted := strconv.QuoteToASCII(command[offset : offset+size])
		piece := quoted[1 : len(quoted)-1]
		if escaped.Len()+len(piece)+2 > budget {
			break
		}
		escaped.WriteString(piece)
		ends = append(ends, escaped.Len())
		offset += size
	}
	preview := escaped.String()
	suffix := ""
	if offset < len(command) {
		suffix = marker
		for ends[len(ends)-1]+2+len(marker) > budget {
			ends = ends[:len(ends)-1]
		}
		preview = preview[:ends[len(ends)-1]]
	}
	return `"` + preview + `"` + suffix
}
