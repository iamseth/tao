package plandelta

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizeLine(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"\t  x\r", "      x"}, {"a\x1b[31mb\x1b]0;title\a", "a�[31mb�]0;title�"},
		{"\u009b\u009d\u202e\u2066\u200e\u200f\x7f\x00", "��������"},
		{"a\xffb", "a�b"}, {"x\r\r", "x�"}, {" a  b ", " a  b "},
	} {
		if got := SanitizeLine(tc.in); got != tc.want {
			t.Errorf("%q: got %q want %q", tc.in, got, tc.want)
		}
	}
	if n := utf8.RuneCountInString(SanitizeLine(strings.Repeat("界", MaxLineRunes+10))); n > MaxLineRunes {
		t.Fatal(n)
	}
	if got := sanitizeReason("\x1bfirst\nsecond"); got != "�first" {
		t.Fatal(got)
	}
	if n := utf8.RuneCountInString(sanitizeReason(strings.Repeat("界", 300))); n > MaxReasonChars {
		t.Fatal(n)
	}
}
