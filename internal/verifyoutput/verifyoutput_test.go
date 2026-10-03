package verifyoutput

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFailureLines(t *testing.T) {
	tests := []struct {
		name, output string
		want         []string
	}{
		{"patterns", "ok pkg\n--- FAIL: TestX (0s)  \nFAIL\tpkg\npanic: broken\nfile: Error: broken\nmake: *** target\n", []string{"--- FAIL: TestX (0s)", "FAIL\tpkg", "panic: broken", "file: Error: broken", "make: *** target"}},
		{"duplicates and CRLF", "FAIL\tpkg\r\nFAIL\tpkg  \r\n--- FAIL: TestX\r\n", []string{"FAIL\tpkg", "--- FAIL: TestX"}},
		{"anchored", "prefix FAIL pkg\n  --- FAIL: TestX\nnot panic: broken", nil},
		{"empty", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FailureLines(tt.output); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("FailureLines() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestReason(t *testing.T) {
	failure := "--- FAIL: TestChangesRunExitCancelsLoader\nFAIL\tgithub.com/iamseth/tao/internal/tui"
	vet := "# pkg\nfile.go:12:3: msg\nfile.go:15:2: another message"
	tests := []struct {
		name, output, fallback string
		max                    int
		want                   string
	}{
		{"passing prefix", strings.Repeat("ok pkg coverage\n", 1000) + failure, "unused", 1000, failure},
		{"fallback head", "ordinary output", "fallback text", 8, "fallback"},
		{"vet relies on caller fallback", vet, vet, 20, string([]rune(vet)[:20])},
		{"multibyte failure", "panic: 界🙂é", "unused", 9, "panic: 界🙂"},
		{"multibyte fallback", "", "界🙂é", 2, "界🙂"},
		{"duplicate failure", "FAIL pkg\nFAIL pkg", "unused", 1000, "FAIL pkg"},
		{"CRLF", "FAIL pkg\r\ncontext\r\n", "unused", 1000, "FAIL pkg\ncontext"},
		{"tail filtering", "FAIL pkg\nok pkg\n? pkg\ncontext\ncontext", "unused", 1000, "FAIL pkg\ncontext"},
		{"last twelve lines", "FAIL pkg\nexcluded\n" + strings.Repeat("ok pkg\n", 11) + "context", "unused", 1000, "FAIL pkg\ncontext"},
		{"failure exceeds bound", strings.Repeat("--- FAIL: Test界\n", 100), "unused", 12, "--- FAIL: Te"},
		{"zero", "FAIL pkg", "fallback", 0, ""},
		{"negative", "", "fallback", -1, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Reason(tt.output, tt.fallback, tt.max)
			if got != tt.want {
				t.Fatalf("Reason() = %q, want %q", got, tt.want)
			}
			if !utf8.ValidString(got) || (tt.max > 0 && utf8.RuneCountInString(got) > tt.max) {
				t.Fatalf("invalid rune bound: %q", got)
			}
		})
	}
}

func TestFirstFailingTest(t *testing.T) {
	tests := []struct{ text, want string }{
		{"--- FAIL: TestX (0.00s)", "TestX"},
		{"--- FAIL: TestX(0.00s)", "TestX"},
		{"FAIL\tpkg", "FAIL\tpkg"},
		{"FAIL\tpkg\r\n--- FAIL: TestX/sub (0s)\r\n--- FAIL: TestY", "TestX/sub"},
		{"FAIL\nFAIL pkg  \r\nFAIL other", "FAIL pkg"},
		{"FAILURE not a package line", ""},
		{"--- FAIL: \nFAIL pkg", "FAIL pkg"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			if got := FirstFailingTest(tt.text); got != tt.want {
				t.Fatalf("FirstFailingTest() = %q, want %q", got, tt.want)
			}
		})
	}
}
