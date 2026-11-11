package note

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// FixSite is one concrete code location named by a note as inline code. Kind
// is path, qualified (pkg.Ident), or identifier, matching FixSiteCheck.Kind.
type FixSite struct {
	Name string
	Kind string
}

const (
	// FixSiteKindPath names a repository-relative file or directory.
	FixSiteKindPath = "path"
	// FixSiteKindQualified names a package-qualified Go declaration.
	FixSiteKindQualified = "qualified"
	// FixSiteKindIdentifier names a bare Go declaration.
	FixSiteKindIdentifier = "identifier"

	maxFixSites        = 64
	maxFixSiteSpanRune = 200
	maxFixSiteFiles    = 50000
	maxFixSiteBytes    = 256 << 20
)

var (
	fixSitePathSuffixes = []string{".go", ".md", ".json", ".jsonl", ".yml", ".yaml", ".golden", ".sh", ".toml", ".txt"}
	fixSiteLinePattern  = regexp.MustCompile(`:\d+(-\d+)?$`)
	fixSiteQualified    = regexp.MustCompile(`^[a-z][a-z0-9_]*\.[A-Za-z_][A-Za-z0-9_]*$`)
	fixSiteIdentifier   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	fixSiteSkippedDirs  = map[string]bool{".git": true, ".tao": true, "bin": true, "node_modules": true, "vendor": true, "testdata": true}
)

// ExtractFixSites scans single-backtick inline code spans in note text and
// returns the concrete fix sites they name, deduplicated by kind and name in
// first-mention order and capped at 64. Scanning stops after MaxText bytes.
// Flags, commands, prose, all-caps tokens, and lowercase words are ignored.
func ExtractFixSites(text string) []FixSite {
	if len(text) > MaxText {
		text = text[:MaxText]
	}
	seen := map[FixSite]bool{}
	var sites []FixSite
	for len(sites) < maxFixSites {
		start := strings.IndexByte(text, '`')
		if start < 0 {
			break
		}
		rest := text[start+1:]
		end := strings.IndexByte(rest, '`')
		if end < 0 {
			break
		}
		span := rest[:end]
		text = rest[end+1:]
		site, ok := classifyFixSite(span)
		if !ok || seen[site] {
			continue
		}
		seen[site] = true
		sites = append(sites, site)
	}
	return sites
}

func classifyFixSite(span string) (FixSite, bool) {
	if span == "" || strings.ContainsRune(span, '\n') || utf8.RuneCountInString(span) > maxFixSiteSpanRune {
		return FixSite{}, false
	}
	if looksLikePath(span) {
		name, ok := normalizeFixSitePath(span)
		if !ok {
			return FixSite{}, false
		}
		return FixSite{Name: name, Kind: FixSiteKindPath}, true
	}
	if fixSiteQualified.MatchString(span) {
		return FixSite{Name: span, Kind: FixSiteKindQualified}, true
	}
	if fixSiteIdentifier.MatchString(span) && hasMixedCase(span) {
		return FixSite{Name: span, Kind: FixSiteKindIdentifier}, true
	}
	return FixSite{}, false
}

func looksLikePath(span string) bool {
	if span == "Makefile" || strings.Contains(span, "/") {
		return true
	}
	for _, suffix := range fixSitePathSuffixes {
		if strings.HasSuffix(span, suffix) {
			return true
		}
	}
	return false
}

func normalizeFixSitePath(span string) (string, bool) {
	if strings.HasPrefix(span, "/") || strings.HasPrefix(span, "~") || strings.Contains(span, "://") {
		return "", false
	}
	if strings.ContainsAny(span, "*?[") || strings.IndexFunc(span, unicode.IsSpace) >= 0 {
		return "", false
	}
	name := strings.TrimPrefix(span, "./")
	name = strings.TrimSuffix(name, "/...")
	name = fixSiteLinePattern.ReplaceAllString(name, "")
	if name == "" || strings.HasPrefix(name, "/") {
		return "", false
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return "", false
		}
	}
	return name, true
}

func hasMixedCase(s string) bool {
	var upper, lower bool
	for _, r := range s {
		switch {
		case unicode.IsUpper(r):
			upper = true
		case unicode.IsLower(r):
			lower = true
		}
	}
	return upper && lower
}

// CheckFixSites reports whether each site still exists beneath root, the
// registered checkout. Path sites are stat-ed without leaving root; qualified
// and identifier sites are resolved against a single-pass index of top-level
// Go declarations built with the standard-library parser. The check is
// read-only, never follows symlinked entries, never runs a subprocess, and is
// advisory only. One FixSiteCheck is returned per site in input order.
func CheckFixSites(ctx context.Context, root string, sites []FixSite) ([]FixSiteCheck, error) {
	checks := make([]FixSiteCheck, 0, len(sites))
	wanted := map[string]bool{}
	for _, site := range sites {
		check := FixSiteCheck{Name: site.Name, Kind: site.Kind}
		switch site.Kind {
		case FixSiteKindPath:
			check.Found, check.Detail = checkFixSitePath(root, site.Name)
		case FixSiteKindQualified:
			if _, ident, ok := strings.Cut(site.Name, "."); ok && ident != "" {
				wanted[ident] = true
			}
		case FixSiteKindIdentifier:
			if site.Name != "" {
				wanted[site.Name] = true
			}
		default:
			check.Detail = "unsupported fix site kind"
		}
		checks = append(checks, check)
	}
	if len(wanted) == 0 {
		return checks, nil
	}
	if root == "" {
		return nil, errors.New("fix site check requires a repository root")
	}
	index, err := indexDeclarations(ctx, root, wanted)
	if err != nil {
		return nil, err
	}
	for i := range checks {
		check := &checks[i]
		var found []declRef
		switch check.Kind {
		case FixSiteKindQualified:
			pkg, ident, ok := strings.Cut(check.Name, ".")
			if !ok {
				continue
			}
			for _, ref := range index.decls[ident] {
				if ref.pkg == pkg || ref.dir == pkg {
					found = append(found, ref)
				}
			}
		case FixSiteKindIdentifier:
			found = index.decls[check.Name]
		default:
			continue
		}
		if len(found) > 0 {
			check.Found = true
			check.Detail = found[0].rel
			continue
		}
		check.Detail = "not found"
		if index.unparsable != "" {
			check.Detail = "not found; skipped unparsable file " + index.unparsable
		}
	}
	return checks, nil
}

func checkFixSitePath(root, name string) (bool, string) {
	cleaned := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return false, "outside repository root"
	}
	if _, err := os.Lstat(filepath.Join(root, cleaned)); err != nil {
		return false, "not found"
	}
	return true, ""
}

type declRef struct {
	rel string
	pkg string
	dir string
}

type declIndex struct {
	decls      map[string][]declRef
	unparsable string
}

func indexDeclarations(ctx context.Context, root string, wanted map[string]bool) (*declIndex, error) {
	index := &declIndex{decls: map[string][]declRef{}}
	needles := make([][]byte, 0, len(wanted))
	for name := range wanted {
		needles = append(needles, []byte(name))
	}
	fset := token.NewFileSet()
	files, read := 0, 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if path == root {
				return walkErr
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			if err := ctx.Err(); err != nil {
				return err
			}
			if path != root && fixSiteSkippedDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		files++
		if files > maxFixSiteFiles {
			return fmt.Errorf("fix site index exceeded %d Go files", maxFixSiteFiles)
		}
		src, err := os.ReadFile(path) //nolint:gosec // G304: path comes from walking the registered checkout root.
		if err != nil {
			return nil //nolint:nilerr // An unreadable Go file is skipped, never fatal.
		}
		read += len(src)
		if read > maxFixSiteBytes {
			return fmt.Errorf("fix site index exceeded %d bytes of Go source", maxFixSiteBytes)
		}
		if !containsAny(src, needles) {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil //nolint:nilerr // A file that cannot be made root-relative is skipped, never fatal.
		}
		rel = filepath.ToSlash(rel)
		file, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
		if err != nil {
			if index.unparsable == "" {
				index.unparsable = rel
			}
			return nil
		}
		ref := declRef{rel: rel, pkg: file.Name.Name, dir: filepath.Base(filepath.Dir(path))}
		for _, name := range topLevelNames(file) {
			if wanted[name] {
				index.decls[name] = append(index.decls[name], ref)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return index, nil
}

func containsAny(src []byte, needles [][]byte) bool {
	for _, needle := range needles {
		if bytes.Contains(src, needle) {
			return true
		}
	}
	return false
}

func topLevelNames(file *ast.File) []string {
	var names []string
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Name != nil {
				names = append(names, d.Name.Name)
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if s.Name != nil {
						names = append(names, s.Name.Name)
					}
				case *ast.ValueSpec:
					for _, n := range s.Names {
						names = append(names, n.Name)
					}
				}
			}
		}
	}
	return names
}
