package promptcapture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestRoundTrip(t *testing.T) {
	dir := Dir(t.TempDir())
	target := Target{Dir: dir, Role: "Review", Template: "review", Label: "A label/with spaces"}
	meta := Meta{Agent: "pi", Model: "model", Effort: "high", StartedAt: time.Date(2026, 10, 3, 6, 0, 0, 0, time.FixedZone("offset", 3600))}
	body := "role: x\n\nbody\n"
	first, err := Write(target, meta, body)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Write(target, meta, body)
	if err != nil {
		t.Fatal(err)
	}
	if first.Path == second.Path || !strings.HasSuffix(second.Path, "-2.md") {
		t.Fatalf("collision: %v %v", first, second)
	}
	wantName := "20261003T050000Z-review-a-label-with-spaces-" + Hash(body)[:8] + ".md"
	if filepath.Base(first.Path) != wantName {
		t.Fatalf("name: %s", first.Path)
	}
	entries, err := List(dir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("list: %v %v", entries, err)
	}
	if entries[0].Name >= entries[1].Name {
		t.Fatal("entries not sorted")
	}
	want := Header{Role: "Review", Template: "review", TemplateHash: "unknown", Agent: "pi", Model: "model", Effort: "high", StartedAt: "2026-10-03T05:00:00Z", SHA256: Hash(body), Bytes: int64(len(body))}
	for _, entry := range entries {
		h, got, err := Read(entry.Path)
		if err != nil || h != want || entry.Header != want || got != body {
			t.Fatalf("read: %+v %q %v", h, got, err)
		}
		data, err := os.ReadFile(entry.Path)
		if err != nil {
			t.Fatal(err)
		}
		h, got, err = Parse(data)
		if err != nil || h != want || got != body {
			t.Fatalf("parse: %+v %q %v", h, got, err)
		}
		info, err := os.Stat(entry.Path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("mode: %v", info.Mode())
		}
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode: %v", info.Mode())
	}
	if first.Hash != Hash(body) || first.Truncated {
		t.Fatalf("written: %+v", first)
	}
}

func TestTruncation(t *testing.T) {
	body := strings.Repeat("a", MaxPromptBytes-1) + "€tail"
	w, err := Write(Target{Dir: Dir(t.TempDir())}, Meta{}, body)
	if err != nil {
		t.Fatal(err)
	}
	h, got, err := Read(w.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !w.Truncated || !h.Truncated || h.Bytes != int64(len(body)) || h.SHA256 != Hash(body) || w.Hash != Hash(body) {
		t.Fatalf("metadata: %+v %+v", w, h)
	}
	if len(got) != MaxPromptBytes-1 || !utf8.ValidString(got) || got != body[:MaxPromptBytes-1] {
		t.Fatal("incorrect truncation")
	}
	if !strings.Contains(filepath.Base(w.Path), "-unknown-") {
		t.Fatal("missing default role")
	}
}

func TestFilesystemErrorsAndInvalidFiles(t *testing.T) {
	root := t.TempDir()
	entries, err := List(filepath.Join(root, "missing"))
	if entries != nil || err != nil {
		t.Fatalf("missing directory: %v %v", entries, err)
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(Target{Dir: filepath.Join(file, "prompts")}, Meta{}, "prompt"); err == nil {
		t.Fatal("expected write error")
	}
	if err := os.WriteFile(filepath.Join(root, "invalid.md"), []byte("role: x\n\nbody"), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err = List(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("invalid files: %v %v", entries, err)
	}
	if _, _, err := Parse([]byte("no boundary")); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestHash(t *testing.T) {
	if got := Hash("abc"); got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatal(got)
	}
}
