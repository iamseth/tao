package note

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestExtractFixSitesClassification(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []FixSite
	}{
		{"path with slash", "see `internal/cli/note.go`", []FixSite{{Name: "internal/cli/note.go", Kind: "path"}}},
		{"path by extension", "edit `Makefile` and `README.md` and `fixture.golden`", []FixSite{{Name: "Makefile", Kind: "path"}, {Name: "README.md", Kind: "path"}, {Name: "fixture.golden", Kind: "path"}}},
		{"strip dot slash", "`./internal/note/fixsite.go`", []FixSite{{Name: "internal/note/fixsite.go", Kind: "path"}}},
		{"strip package suffix", "`./internal/note/...`", []FixSite{{Name: "internal/note", Kind: "path"}}},
		{"strip line suffix", "`internal/cli/note.go:445`", []FixSite{{Name: "internal/cli/note.go", Kind: "path"}}},
		{"strip line range suffix", "`internal/cli/note.go:10-20`", []FixSite{{Name: "internal/cli/note.go", Kind: "path"}}},
		{"qualified", "`note.LookupPlan` and `runtimeconfig.commandOptionRules`", []FixSite{{Name: "note.LookupPlan", Kind: "qualified"}, {Name: "runtimeconfig.commandOptionRules", Kind: "qualified"}}},
		{"identifier", "`PlanSummary` and `runEnvDefaults`", []FixSite{{Name: "PlanSummary", Kind: "identifier"}, {Name: "runEnvDefaults", Kind: "identifier"}}},
		{"all caps ignored", "`TAO_DATA_HOME`", nil},
		{"lowercase word ignored", "`tao` and `main`", nil},
		{"flags and commands ignored", "`--plans-dir` and `tao note run` and `go test ./...`", nil},
		{"absolute rejected", "`/Users/x/tao/internal/note.go`", nil},
		{"tilde rejected", "`~/.local/share/tao/notes.json`", nil},
		{"parent escape rejected", "`../internal/note.go` and `internal/../../x.go`", nil},
		{"url rejected", "`https://example.com/a.go`", nil},
		{"glob rejected", "`internal/*.go` and `cmd/?.go` and `a/[b].go`", nil},
		{"whitespace in path rejected", "`internal/cli note.go`", nil},
		{"newline span ignored", "`internal/\ncli/note.go`", nil},
		{"unterminated span ignored", "`internal/cli/note.go", nil},
		{"empty span ignored", "`` and `a`", nil},
		{"dedupe first mention", "`a.B` `Foo` `a.B` `Foo` `x/y.go` `x/y.go`", []FixSite{{Name: "a.B", Kind: "qualified"}, {Name: "Foo", Kind: "identifier"}, {Name: "x/y.go", Kind: "path"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractFixSites(tc.text)
			if !equalSites(got, tc.want) {
				t.Fatalf("ExtractFixSites(%q) = %+v, want %+v", tc.text, got, tc.want)
			}
		})
	}
}

func TestExtractFixSitesDedupeOrderAndKinds(t *testing.T) {
	// The same name with a different kind is a distinct site.
	got := ExtractFixSites("`note.Foo` then `Foo` then `note.Foo`")
	want := []FixSite{{Name: "note.Foo", Kind: "qualified"}, {Name: "Foo", Kind: "identifier"}}
	if !equalSites(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestExtractFixSitesCap(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&sb, "`Site%d` ", i)
	}
	got := ExtractFixSites(sb.String())
	if len(got) != 64 {
		t.Fatalf("len = %d, want 64", len(got))
	}
	if got[0].Name != "Site0" || got[63].Name != "Site63" {
		t.Fatalf("unexpected cap order: first %q last %q", got[0].Name, got[63].Name)
	}
}

func TestExtractFixSitesBounds(t *testing.T) {
	long := "`" + strings.Repeat("a", 201) + "Z`"
	if got := ExtractFixSites(long + " `Ok`"); !equalSites(got, []FixSite{{Name: "Ok", Kind: "identifier"}}) {
		t.Fatalf("long span should be skipped, got %+v", got)
	}
	text := strings.Repeat("x", MaxText) + " `Late`"
	if got := ExtractFixSites(text); len(got) != 0 {
		t.Fatalf("spans past MaxText must be ignored, got %+v", got)
	}
}

func TestExtractFixSitesTranscriptRegression(t *testing.T) {
	text := "the fix site is `runEnvDefaults` but the real path is `runtimeconfig.commandOptionRules` in `internal/runtimeconfig/command_options.go`"
	got := ExtractFixSites(text)
	want := []FixSite{
		{Name: "runEnvDefaults", Kind: "identifier"},
		{Name: "runtimeconfig.commandOptionRules", Kind: "qualified"},
		{Name: "internal/runtimeconfig/command_options.go", Kind: "path"},
	}
	if !equalSites(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func equalSites(a, b []FixSite) bool {
	return slices.Equal(a, b)
}

func writeFakeModule(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/fake\n\ngo 1.22\n",
		"internal/alpha/alpha.go": `package alpha

type Widget struct{}

func (w *Widget) Spin() {}

func NewWidget() *Widget { return &Widget{} }

var DefaultWidget = NewWidget()

const MaxWidgets = 3
`,
		"internal/alpha/alpha_test.go": `package alpha

func helperOnlyInTest() {}
`,
		"internal/alpha/testdata/skipped.go": `package alpha

func InTestdata() {}
`,
		"internal/beta-dir/beta.go": `package gamma

func lookupThing() {}
`,
		"internal/broken/broken.go": "package broken\n\nfunc Unterminated( {\n",
		"README.md":                 "hello\n",
	}
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func addSymlinkEscape(t *testing.T, root string) bool {
	t.Helper()
	if runtime.GOOS == "windows" {
		return false
	}
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outside, "pkg"), 0o700); err != nil {
		t.Fatal(err)
	}
	src := "package outside\n\nfunc OutsideRoot() {}\n"
	if err := os.WriteFile(filepath.Join(outside, "pkg", "o.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "pkg", "o.go"), filepath.Join(root, "linked.go")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	return true
}

func TestCheckFixSites(t *testing.T) {
	root := writeFakeModule(t)
	linked := addSymlinkEscape(t, root)
	sites := []FixSite{
		{Name: "internal/alpha/alpha.go", Kind: "path"},
		{Name: "README.md", Kind: "path"},
		{Name: "internal/alpha", Kind: "path"},
		{Name: "internal/missing.go", Kind: "path"},
		{Name: "../escape.go", Kind: "path"},
		{Name: "internal/../../escape.go", Kind: "path"},
		{Name: "/etc/passwd", Kind: "path"},
		{Name: "alpha.Widget", Kind: "qualified"},
		{Name: "alpha.Spin", Kind: "qualified"},
		{Name: "alpha.NewWidget", Kind: "qualified"},
		{Name: "alpha.DefaultWidget", Kind: "qualified"},
		{Name: "alpha.MaxWidgets", Kind: "qualified"},
		{Name: "alpha.helperOnlyInTest", Kind: "qualified"},
		{Name: "alpha.Nope", Kind: "qualified"},
		{Name: "gamma.lookupThing", Kind: "qualified"},
		{Name: "beta-dir.lookupThing", Kind: "qualified"},
		{Name: "other.Widget", Kind: "qualified"},
		{Name: "Widget", Kind: "identifier"},
		{Name: "lookupThing", Kind: "identifier"},
		{Name: "InTestdata", Kind: "identifier"},
		{Name: "OutsideRoot", Kind: "identifier"},
		{Name: "Unterminated", Kind: "identifier"},
		{Name: "Missing", Kind: "identifier"},
	}
	checks, err := CheckFixSites(context.Background(), root, sites)
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != len(sites) {
		t.Fatalf("len = %d, want %d", len(checks), len(sites))
	}
	wantFound := map[string]bool{
		"path:internal/alpha/alpha.go":     true,
		"path:README.md":                   true,
		"path:internal/alpha":              true,
		"path:internal/missing.go":         false,
		"path:../escape.go":                false,
		"path:internal/../../escape.go":    false,
		"path:/etc/passwd":                 false,
		"qualified:alpha.Widget":           true,
		"qualified:alpha.Spin":             true,
		"qualified:alpha.NewWidget":        true,
		"qualified:alpha.DefaultWidget":    true,
		"qualified:alpha.MaxWidgets":       true,
		"qualified:alpha.helperOnlyInTest": true,
		"qualified:alpha.Nope":             false,
		"qualified:gamma.lookupThing":      true,
		"qualified:beta-dir.lookupThing":   true,
		"qualified:other.Widget":           false,
		"identifier:Widget":                true,
		"identifier:lookupThing":           true,
		"identifier:InTestdata":            false,
		"identifier:OutsideRoot":           false,
		"identifier:Unterminated":          false,
		"identifier:Missing":               false,
	}
	for i, check := range checks {
		if check.Name != sites[i].Name || check.Kind != sites[i].Kind {
			t.Fatalf("check %d = %+v, want name/kind from %+v", i, check, sites[i])
		}
		key := check.Kind + ":" + check.Name
		want, ok := wantFound[key]
		if !ok {
			t.Fatalf("missing expectation for %s", key)
		}
		if check.Found != want {
			t.Errorf("%s Found = %v (detail %q), want %v", key, check.Found, check.Detail, want)
		}
	}
	byKey := func(key string) FixSiteCheck {
		for _, c := range checks {
			if c.Kind+":"+c.Name == key {
				return c
			}
		}
		t.Fatalf("no check %s", key)
		return FixSiteCheck{}
	}
	if d := byKey("qualified:alpha.Widget").Detail; d != "internal/alpha/alpha.go" {
		t.Errorf("Detail = %q, want repository-relative file", d)
	}
	for _, key := range []string{"path:../escape.go", "path:internal/../../escape.go", "path:/etc/passwd"} {
		if d := byKey(key).Detail; d != "outside repository root" {
			t.Errorf("%s Detail = %q", key, d)
		}
	}
	if !linked {
		t.Log("symlink cases not exercised on this platform")
	}
}

func TestCheckFixSitesCancelled(t *testing.T) {
	root := writeFakeModule(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CheckFixSites(ctx, root, []FixSite{{Name: "Widget", Kind: "identifier"}}); err == nil {
		t.Fatal("expected cancellation error")
	}
	// Path-only checks never walk and so never observe cancellation.
	checks, err := CheckFixSites(ctx, root, []FixSite{{Name: "README.md", Kind: "path"}})
	if err != nil || len(checks) != 1 || !checks[0].Found {
		t.Fatalf("path-only check: %+v, %v", checks, err)
	}
}

func TestCheckFixSitesEmptyAndUnknownKind(t *testing.T) {
	root := writeFakeModule(t)
	checks, err := CheckFixSites(context.Background(), root, nil)
	if err != nil || len(checks) != 0 {
		t.Fatalf("nil sites: %+v, %v", checks, err)
	}
	checks, err = CheckFixSites(context.Background(), root, []FixSite{{Name: "x", Kind: "weird"}})
	if err != nil || len(checks) != 1 || checks[0].Found || checks[0].Detail == "" {
		t.Fatalf("unknown kind: %+v, %v", checks, err)
	}
}
