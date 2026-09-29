package commit

import (
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

func TestVerificationTrailers(t *testing.T) {
	digest := strings.Repeat("a", 64)
	zero := 0
	for _, command := range []string{"go test ./...", "echo \"hi\"\n\tgo test\r\nTao-Plan: forged", "echo \x00\x1b[31m\u2028\u2029\u202e界🐱", strings.Repeat("界🐱\n", 2000), strings.Repeat("x", 1024), "invalid\xff"} {
		t.Run(fmt.Sprintf("%x", sha256.Sum256([]byte(command)))[:12], func(t *testing.T) {
			trailers, err := VerificationTrailers(2, command, &zero, digest)
			if err != nil {
				t.Fatal(err)
			}
			if len(trailers) != 3 {
				t.Fatalf("trailers = %+v", trailers)
			}
			for i, key := range []string{"Tao-Verify-Command", "Tao-Verify-Exit", "Tao-Verify-Digest"} {
				trailer := trailers[i]
				if trailer.key != key || !strings.HasPrefix(trailer.value, "2: ") {
					t.Fatalf("trailer %d = %+v", i, trailer)
				}
				if !utf8.ValidString(trailer.value) || strings.IndexFunc(trailer.value, func(r rune) bool {
					return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029'
				}) >= 0 {
					t.Fatalf("unsafe display: %q", trailer.value)
				}
			}
			value := trailers[0].value
			identity := fmt.Sprintf("sha256=%x", sha256.Sum256([]byte(command)))
			if len(value) > 1024 || !strings.Contains(value, identity) {
				t.Fatalf("command value = %q (%d bytes)", value, len(value))
			}
			truncated := strings.HasSuffix(value, " [truncated]")
			quoted := strings.TrimSuffix(strings.TrimPrefix(value, "2: "+identity+" "), " [truncated]")
			preview, err := strconv.Unquote(quoted)
			if err != nil {
				t.Fatalf("invalid quoted preview %q: %v", quoted, err)
			}
			if !strings.HasPrefix(command, preview) || (!truncated && preview != command) || (truncated && preview == command) {
				t.Fatalf("preview not faithful; truncated=%v", truncated)
			}
			if len(command) > 1024 && !truncated {
				t.Fatal("missing truncation marker")
			}
			if trailers[1].value != "2: 0" || trailers[2].value != "2: "+digest {
				t.Fatalf("exit/digest = %+v", trailers)
			}
			message, err := Format(validProposal(), trailers...)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateMessage(message); err != nil {
				t.Fatalf("round trip: %v\n%s", err, message)
			}
		})
	}
}

func TestVerificationTrailersValidation(t *testing.T) {
	digest := strings.Repeat("0", 64)
	for _, bad := range []string{"", "abcd", strings.Repeat("g", 64), strings.Repeat("a", 63), strings.Repeat("a", 65), strings.Repeat("A", 64), "\n" + strings.Repeat("a", 63)} {
		if _, err := VerificationTrailers(1, "test", nil, bad); err == nil {
			t.Errorf("accepted digest %q", bad)
		}
	}
	for _, index := range []int{-1, 0} {
		if _, err := VerificationTrailers(index, "test", nil, digest); err == nil {
			t.Errorf("accepted index %d", index)
		}
	}
	if _, err := VerificationTrailers(1, "", nil, digest); err == nil {
		t.Fatal("accepted empty command")
	}
	negative := -1
	if _, err := VerificationTrailers(1, "test", &negative, digest); err == nil {
		t.Fatal("accepted negative exit")
	}
	maxInt := int(^uint(0) >> 1)
	for _, exit := range []*int{nil, new(int), new(127), &maxInt} {
		trailers, err := VerificationTrailers(1, "test", exit, digest)
		if err != nil {
			t.Fatal(err)
		}
		want := "1: unknown"
		if exit != nil {
			want = "1: " + strconv.Itoa(*exit)
		}
		if trailers[1].value != want || len(trailers[1].value) > 32 {
			t.Fatalf("exit = %q", trailers[1].value)
		}
	}
}

func TestVerificationTrailersTruncatedIdentity(t *testing.T) {
	prefix := strings.Repeat("界", 2000)
	var values []string
	for _, command := range []string{prefix + "first", prefix + "second"} {
		trailers, err := VerificationTrailers(int(^uint(0)>>1), command, nil, strings.Repeat("a", 64))
		if err != nil {
			t.Fatal(err)
		}
		value := trailers[0].value
		if len(value) > 1024 || !strings.HasSuffix(value, " [truncated]") {
			t.Fatalf("unbounded or unmarked preview: %q", value)
		}
		values = append(values, value)
	}
	if values[0] == values[1] {
		t.Fatal("commands with identical previews lost their distinct full identities")
	}
}

func TestVerificationTrailersAttemptOrdering(t *testing.T) {
	planTrailer, err := NewTrustedTrailer("Tao-Plan", "plan-a")
	if err != nil {
		t.Fatal(err)
	}
	sliceTrailer, err := NewTrustedTrailer("Tao-Slice", "004-claims")
	if err != nil {
		t.Fatal(err)
	}
	trailers := []TrustedTrailer{planTrailer, sliceTrailer}
	for index := 1; index <= 3; index++ {
		got, err := VerificationTrailers(index, "same command", nil, strings.Repeat("a", 64))
		if err != nil {
			t.Fatal(err)
		}
		trailers = append(trailers, got...)
	}
	message, err := Format(validProposal(), trailers...)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateMessage(message); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(message, "Tao-Plan: plan-a\nTao-Slice: 004-claims\nTao-Verify-Command: 1: ") {
		t.Fatalf("existing trailers changed: %s", message)
	}
	parsed, ok := parseTrustedTrailers(message[strings.LastIndex(message, "\n\n")+2:])
	if !ok {
		t.Fatal("parse failed")
	}
	if len(parsed) != len(trailers) {
		t.Fatal("lost trailers")
	}
	for i := range trailers {
		if parsed[i] != trailers[i] {
			t.Fatalf("trailer %d changed", i)
		}
	}
}
