package gitops

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/testenv"
)

func TestPorcelainStatusZRealGitIdentity(t *testing.T) {
	testenv.SkipIfShort(t)
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...) //nolint:gosec // test invokes fixed Git with test-controlled arguments
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return string(out)
	}
	git("init", "-q")
	names := []string{"ordinary.go", "old -> new.go", "file.go", "file.go "}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("test"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	status := git("status", "--porcelain=v1", "--no-renames", "-z", "--untracked-files=all")
	for _, name := range names {
		record := "?? " + name
		if !strings.Contains(status, record+"\x00") {
			t.Fatalf("missing exact record %q in %q", record, status)
		}
		got, ambiguous := PorcelainPath(record)
		if ambiguous || got != name {
			t.Fatalf("raw record identity changed: %q, %v; want %q", got, ambiguous, name)
		}
	}
	entries, err := ParsePorcelainStatusZ(status)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(names) {
		t.Fatalf("entries: %#v", entries)
	}
	for _, name := range names {
		found := false
		for _, entry := range entries {
			if entry.Code == "??" && entry.Path == name {
				found = true
			}
		}
		if !found {
			t.Errorf("missing exact path bytes % x", name)
		}
	}
}

func TestParsePorcelainStatusZPaths(t *testing.T) {
	paths := []string{"ordinary.go", "old -> new.go", `a"b`, `a\b`, "日本語.go", "tab\tname", "line\nname", " leading", "trailing ", "\t", "raw\xff\xfe", "carriage\rreturn", "control\x01"}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			entries, err := ParsePorcelainStatusZ("?? " + path + "\x00")
			if err != nil || len(entries) != 1 || entries[0].Code != "??" || entries[0].Path != path {
				t.Fatalf("path bytes % x: entries=%#v error=%v", path, entries, err)
			}
		})
	}
	entries, err := ParsePorcelainStatusZ(" M ordinary.go\x00?? new.go\x00UU conflict.go\x00")
	if err != nil || len(entries) != 3 || entries[2].Code != "UU" || entries[2].Path != "conflict.go" {
		t.Fatalf("later conflict lost: %#v, %v", entries, err)
	}
	entries, err = ParsePorcelainStatusZ("")
	if err != nil || len(entries) != 0 {
		t.Fatalf("clean: %#v, %v", entries, err)
	}
}

func TestParsePorcelainStatusZCodes(t *testing.T) {
	// Conflicts and ignored entries are structurally valid, not admissions.
	for _, code := range []string{"??", "!!", " M", " T", " A", " D", "M ", "MM", "MT", "MD", "T ", "TM", "TT", "TD", "A ", "AM", "AT", "AD", "D ", "DD", "AU", "UD", "UA", "DU", "AA", "UU"} {
		if _, err := ParsePorcelainStatusZ(code + " path\x00"); err != nil {
			t.Errorf("code %q: %v", code, err)
		}
	}
}

func TestParsePorcelainStatusZRejectsMalformed(t *testing.T) {
	cases := []string{
		"?? path", "?? path\n", "\x00", "?? \x00", "?? path\x00\x00", "?? path\x00?? later",
		"? path\x00", "???path\x00", "XX path\x00", "  path\x00", "?M path\x00", "M? path\x00", "!M path\x00", "DM path\x00", "MU path\x00",
		"R  dest\x00source\x00", " C dest\x00?? injected\x00", "RM dest\x00source\x00", "?? good\x00R  dest\x00?? injected\x00",
	}
	for _, status := range cases {
		t.Run(status, func(t *testing.T) {
			for _, input := range []string{status, "?? earlier\x00" + status} {
				entries, err := ParsePorcelainStatusZ(input)
				if err == nil || entries != nil {
					t.Fatalf("accepted malformed input %q: %#v, %v", input, entries, err)
				}
				if strings.ContainsAny(err.Error(), "\x00\n\r\t") {
					t.Fatalf("unsafe error: %q", err.Error())
				}
				out, err := FormatPorcelainStatusZ(input)
				if err == nil || out != "" {
					t.Fatalf("rendered malformed input: %q, %v", out, err)
				}
			}
		})
	}
}

func TestFormatPorcelainStatusZ(t *testing.T) {
	cases := []struct{ input, want string }{
		{"", ""},
		{" M ordinary.go\x00?? another.go\x00", " M ordinary.go\n?? another.go"},
		{"?? old -> new.go\x00", "?? old -> new.go"},
		{"?? 日本語.go\x00", "?? 日本語.go"},
		{"?? tab\tname\x00", `?? "tab\tname"`},
		{"?? line\nname\x00", `?? "line\nname"`},
		{"?? quote\"name\x00", `?? "quote\"name"`},
		{"?? back\\slash\x00", `?? "back\\slash"`},
		{"??  leading\x00", `?? " leading"`},
		{"?? trailing \x00", `?? "trailing "`},
		{"?? raw\xff\x00", `?? "raw\xff"`},
		{"?? control\x01\x7f\x00", `?? "control\x01\x7f"`},
		{"?? \u0085name\x00", `?? "\u0085name"`},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got, err := FormatPorcelainStatusZ(tc.input)
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}
