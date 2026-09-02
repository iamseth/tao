package textbound

import (
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTailRepairsCutBoundary(t *testing.T) {
	got, truncated := Tail("ab€cd", 4)
	if !truncated {
		t.Fatal("Tail reported untruncated output")
	}
	if got != "cd" {
		t.Fatalf("Tail() = %q, want %q", got, "cd")
	}
	if len(got) > 4 {
		t.Fatalf("Tail() length = %d, want at most 4", len(got))
	}
	if !utf8.ValidString(got) || strings.ContainsRune(got, utf8.RuneError) {
		t.Fatalf("Tail() returned invalid or replacement-containing output %q", got)
	}
}

func TestHeadRepairsCutBoundary(t *testing.T) {
	got, truncated := Head("ab€cd", 4)
	if !truncated {
		t.Fatal("Head reported untruncated output")
	}
	if got != "ab" {
		t.Fatalf("Head() = %q, want %q", got, "ab")
	}
	if len(got) > 4 {
		t.Fatalf("Head() length = %d, want at most 4", len(got))
	}
	if !utf8.ValidString(got) || strings.ContainsRune(got, utf8.RuneError) {
		t.Fatalf("Head() returned invalid or replacement-containing output %q", got)
	}
}

func TestBoundsReturnValuesWithinLimitUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name  string
		bound func(string, int) (string, bool)
	}{
		{name: "Tail", bound: Tail},
		{name: "Head", bound: Head},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, value := range []string{"abc", "abcd"} {
				got, truncated := tc.bound(value, 4)
				if truncated {
					t.Fatalf("bound(%q, 4) reported truncation", value)
				}
				if got != value {
					t.Fatalf("bound(%q, 4) = %q", value, got)
				}
			}
		})
	}
}

func TestBoundsPreserveInvalidByteAwayFromCut(t *testing.T) {
	headInput := "a\xffbcdef"
	if got, truncated := Head(headInput, 5); !truncated || got != "a\xffbcd" {
		t.Fatalf("Head() = (%q, %t), want (%q, true)", got, truncated, "a\xffbcd")
	}

	tailInput := "abcdef\xffz"
	if got, truncated := Tail(tailInput, 4); !truncated || got != "ef\xffz" {
		t.Fatalf("Tail() = (%q, %t), want (%q, true)", got, truncated, "ef\xffz")
	}
}

func TestBoundsBelowSingleRune(t *testing.T) {
	for _, tc := range []struct {
		name  string
		bound func(string, int) (string, bool)
	}{
		{name: "Tail", bound: Tail},
		{name: "Head", bound: Head},
	} {
		for _, maxBytes := range []int{0, 1, 2, 3} {
			t.Run(tc.name+"/max="+strconv.Itoa(maxBytes), func(t *testing.T) {
				got, truncated := tc.bound("😀😀", maxBytes)
				if !truncated {
					t.Fatal("bound reported untruncated output")
				}
				if got != "" {
					t.Fatalf("bound output = %q, want empty", got)
				}
			})
		}
	}
}

func TestBoundsEmptyInput(t *testing.T) {
	for name, bound := range map[string]func(string, int) (string, bool){
		"Tail": Tail,
		"Head": Head,
	} {
		t.Run(name, func(t *testing.T) {
			got, truncated := bound("", 1)
			if truncated || got != "" {
				t.Fatalf("bound empty input = (%q, %t), want (empty, false)", got, truncated)
			}
		})
	}
}
