package run

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/iamseth/tao/internal/plan"
)

const verificationTailBytes = 8 * 1024

// verificationTail drains a stream without retaining more than 8 KiB. A writer
// belongs to one stream; commandrunner finishes its copy before evidence is read.
// Even a single arbitrarily large Write does not allocate or retain its input.
type verificationTail struct {
	data      [verificationTailBytes]byte
	size      int
	truncated bool
}

func (t *verificationTail) Write(p []byte) (int, error) {
	n := len(p)
	if n > verificationTailBytes-t.size {
		t.truncated = true
	}
	if n >= verificationTailBytes {
		copy(t.data[:], p[n-verificationTailBytes:])
		t.size = verificationTailBytes
	} else {
		keep := min(t.size, verificationTailBytes-n)
		copy(t.data[:], t.data[t.size-keep:t.size])
		copy(t.data[keep:], p)
		t.size = keep + n
	}
	return n, nil
}

func (t *verificationTail) bytes() []byte { return t.data[:t.size] }

// verificationOutputDigest is SHA-256 of uint64 big-endian stdout-tail length,
// raw stdout-tail bytes, uint64 big-endian stderr-tail length, raw stderr-tail
// bytes (including zero lengths). Stream interleaving is deliberately irrelevant.
// This is bounded-output evidence, NOT a full output/content hash or proof of
// shell confinement. Hash raw bytes before any presentation sanitization.
func verificationOutputDigest(stdout, stderr *verificationTail) string {
	h := sha256.New()
	var length [8]byte
	for _, tail := range []*verificationTail{stdout, stderr} {
		binary.BigEndian.PutUint64(length[:], uint64(tail.size)) //nolint:gosec // G115: size is in [0, verificationTailBytes].
		_, _ = h.Write(length[:])
		_, _ = h.Write(tail.bytes())
	}
	return hex.EncodeToString(h.Sum(nil))
}

func verificationOutputDetails(stdout, stderr *verificationTail) string {
	if stdout.size == 0 && stderr.size == 0 {
		return ""
	}
	const headers = "stdout (tail):\n\nstderr (tail):\n"
	budget := (plan.MaxVerificationDetailsBytes - len(headers)) / 2
	return "stdout (tail):\n" + verificationPresentation(string(stdout.bytes()), budget) +
		"\nstderr (tail):\n" + verificationPresentation(string(stderr.bytes()), budget)
}

// Sanitize only the display projection, keeping its end on a UTF-8 boundary.
// Controls (including terminal escapes and Unicode format controls) are replaced,
// not interpreted. Raw stream bytes remain the digest input.
func verificationPresentation(s string, limit int) string {
	if len(s) > limit {
		s = s[len(s)-limit:]
	}
	s = strings.Map(func(r rune) rune {
		if (unicode.IsControl(r) && r != '\n' && r != '\t') || unicode.Is(unicode.Cf, r) {
			return '\uFFFD'
		}
		return r
	}, s)
	if len(s) > limit {
		s = s[len(s)-limit:]
		for len(s) > 0 && !utf8.RuneStart(s[0]) {
			s = s[1:]
		}
	}
	return s
}
