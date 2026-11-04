package verification

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNegativeSearchMatches(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{"a.txt": "needle needle\nother\nneedle", "b.txt": strings.Repeat("needle\n", 6), "literal.txt": "[needle]\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(root, "dir"), 0700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("needle"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "large"), make([]byte, (4<<20)+1), 0600); err != nil {
		t.Fatal(err)
	}
	a := NewAnalyzer(root)
	for _, tt := range []struct {
		command   string
		ok        bool
		total     int
		locations []string
	}{
		{"! grep -q needle a.txt", true, 2, []string{"a.txt:1", "a.txt:3"}},
		{"! rg -q needle a.txt", true, 2, []string{"a.txt:1", "a.txt:3"}},
		{"! grep -qF '[needle]' literal.txt", true, 1, []string{"literal.txt:1"}},
		{"! rg -qsF '[needle]' literal.txt", true, 1, []string{"literal.txt:1"}},
		{"! grep -s -F -q needle a.txt b.txt", true, 8, []string{"a.txt:1", "a.txt:3", "b.txt:1", "b.txt:2", "b.txt:3"}},
		{"! rg -q absent a.txt", true, 0, nil},
		{"! grep -q '[needle]' literal.txt", false, 0, nil},
		{"! rg -q 'nee+dle' a.txt", false, 0, nil},
		{"! grep -qE needle a.txt", false, 0, nil},
		{"! grep -qi needle a.txt", false, 0, nil},
		{"! grep -qr needle a.txt", false, 0, nil},
		{"! grep -q -e needle a.txt", false, 0, nil},
		{"! grep -F needle a.txt", false, 0, nil},
		{"! grep -q needle a.txt || true", false, 0, nil},
		{"! grep -q needle a.txt | sort", false, 0, nil},
		{"! grep -q needle a.txt > output", false, 0, nil},
		{"! grep -q needle a.txt>output", false, 0, nil},
		{"! grep -q needle a.txt;", false, 0, nil},
		{"! grep -q needle missing", false, 0, nil},
		{"! grep -q needle dir", false, 0, nil},
		{"! grep -q needle escape", false, 0, nil},
		{"! grep -q needle large", false, 0, nil},
		{"! grep -q needle '*.txt'", false, 0, nil},
		{"! grep -q needle a.txt -s", false, 0, nil},
		{"! grep -q '$PATTERN' a.txt", false, 0, nil},
		{"! grep -qF \"$PATTERN\" a.txt", false, 0, nil},
		{"! grep -q 'needle a.txt", false, 0, nil},
	} {
		t.Run(tt.command, func(t *testing.T) {
			got, ok := a.NegativeSearchMatches(tt.command)
			if ok != tt.ok || got.Total != tt.total || !reflect.DeepEqual(got.Locations, tt.locations) {
				t.Fatalf("got %+v, %v; want total %d locations %v, %v", got, ok, tt.total, tt.locations, tt.ok)
			}
			if got.Total > 0 && got.FirstPath != strings.SplitN(tt.locations[0], ":", 2)[0] {
				t.Fatalf("wrong first path: %+v", got)
			}
		})
	}
}
