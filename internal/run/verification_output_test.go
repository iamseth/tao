package run

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestVerificationOutputTails(t *testing.T) {
	var out, errOut verificationTail
	input := bytes.Repeat([]byte("x"), 1024*1024)
	for range 8 {
		if n, err := out.Write(input); n != len(input) || err != nil {
			t.Fatalf("write = %d, %v", n, err)
		}
	}
	_, _ = out.Write([]byte{0xff, 0, 27, '\n'})
	_, _ = errOut.Write([]byte("error"))
	if len(out.bytes()) != 8192 || !out.truncated || len(out.data) != 8192 {
		t.Fatalf("unbounded tail: %d", len(out.bytes()))
	}
	if got := testing.AllocsPerRun(100, func() { _, _ = out.Write(input) }); got != 0 {
		t.Fatalf("write allocated %v times", got)
	}
	details := verificationOutputDetails(&out, &errOut)
	if len(details) > 16*1024 || !utf8.ValidString(details) || strings.ContainsAny(details, "\x00\x1b") || !strings.Contains(details, "error") {
		t.Fatalf("unsafe details: %q", details)
	}
}

func TestVerificationOutputDigestFraming(t *testing.T) {
	for _, pair := range [][2]string{{"", ""}, {"ab", "c"}, {"a", "bc"}, {"\xff", "\x00"}} {
		var out, errOut verificationTail
		_, _ = out.Write([]byte(pair[0]))
		_, _ = errOut.Write([]byte(pair[1]))
		var frame bytes.Buffer
		for _, value := range pair {
			if err := binary.Write(&frame, binary.BigEndian, uint64(len(value))); err != nil {
				t.Fatal(err)
			}
			frame.WriteString(value)
		}
		sum := sha256.Sum256(frame.Bytes())
		if got := verificationOutputDigest(&out, &errOut); got != hex.EncodeToString(sum[:]) {
			t.Fatalf("digest = %s", got)
		}
	}
}

func TestVerificationOutputInterleaving(t *testing.T) {
	var a, b, c, d verificationTail
	_, _ = a.Write([]byte("one"))
	_, _ = b.Write([]byte("err"))
	_, _ = a.Write([]byte("two"))
	_, _ = d.Write([]byte("err"))
	_, _ = c.Write([]byte("onetwo"))
	if verificationOutputDigest(&a, &b) != verificationOutputDigest(&c, &d) {
		t.Fatal("digest depends on stream interleaving")
	}
	var tail verificationTail
	for _, part := range []string{strings.Repeat("a", 8190), "bc", "def"} {
		_, _ = tail.Write([]byte(part))
	}
	if got := string(tail.bytes()); got != strings.Repeat("a", 8187)+"bcdef" || !tail.truncated {
		t.Fatal("incorrect rolling tail")
	}
}
