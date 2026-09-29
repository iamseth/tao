package runtimeconfig

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

// This is a bounded syntax guard, not general dataflow analysis. It recognizes
// os import aliases, function/value aliases, runtimeconfig constants, injected
// string readers, simple wrappers and literal key lists. It scans all production
// Go files (including platform files), not help text, comments or test fixtures.
// Ordinary non-table reads remain out of scope: data-home, terminal/color,
// USER/USERNAME fallback, editor, prompt paths, Herdr and Git transport settings.
// Unknown dynamic keys fail closed unless they are a wrapper's parameter; calls
// to such wrappers are checked in turn. Arbitrary reflection/computed keys and
// inter-package custom wrappers are not a dataflow proof furnished by this test.
func TestRuntimeEnvironmentLoadingBoundary(t *testing.T) {
	root := filepath.Join("..", "..")
	violations, err := scanEnvSources(os.DirFS(root))
	if err != nil {
		t.Fatal(err)
	}
	for _, violation := range violations {
		t.Error(violation)
	}
}

func scanEnvSources(root fs.FS) ([]string, error) {
	var violations []string
	err := fs.WalkDir(root, ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".tao", "bin", "dist", "build", "node_modules":
				return fs.SkipDir
			}
			// Linked checkouts carry a .git file. Never descend into them.
			if path != "." {
				if _, err := fs.Stat(root, path+"/.git"); err == nil {
					return fs.SkipDir
				} else if !os.IsNotExist(err) {
					return err
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("production source symlink outside scan contract: %s", path)
		}
		data, err := fs.ReadFile(root, path)
		if err != nil {
			return err
		}
		found, err := checkEnvSource(path, data)
		violations = append(violations, found...)
		return err
	})
	return violations, err
}

func checkEnvSource(path string, source []byte) ([]string, error) {
	positions := token.NewFileSet()
	file, err := parser.ParseFile(positions, path, source, 0)
	if err != nil {
		return nil, err
	}
	imports := map[string]string{}
	runtimeDot := false
	for _, spec := range file.Imports {
		name, _ := strconv.Unquote(spec.Path.Value)
		alias := filepath.Base(name)
		if spec.Name != nil {
			alias = spec.Name.Name
		}
		imports[alias] = name
		runtimeDot = runtimeDot || (alias == "." && name == "github.com/iamseth/tao/internal/runtimeconfig")
	}
	keys := map[string]bool{}
	for _, key := range RuntimeEnvKeys() {
		keys[key] = true
	}
	// Constants come from the actual canonical declaration, not a second list.
	constants := map[string]string{}
	constantSource, err := parser.ParseFile(token.NewFileSet(), "env_status.go", nil, 0)
	if err != nil {
		return nil, err
	}
	ast.Inspect(constantSource, func(node ast.Node) bool {
		if spec, ok := node.(*ast.ValueSpec); ok {
			for i, value := range spec.Values {
				if literal, ok := value.(*ast.BasicLit); ok && i < len(spec.Names) {
					constants[spec.Names[i].Name], _ = strconv.Unquote(literal.Value)
				}
			}
		}
		return true
	})
	values := map[string]ast.Expr{}
	// Parser object links suffice for these lexical value/range aliases. This
	// guard deliberately does not infer field identity or claim type analysis.
	scopedValues := map[*ast.Object]ast.Expr{} //nolint:staticcheck // SA1019: bounded lexical resolution, not type/field resolution.
	readers := map[string]bool{}
	var exprName func(ast.Expr) string
	exprName = func(expr ast.Expr) string {
		switch expr := expr.(type) {
		case *ast.Ident:
			return expr.Name
		case *ast.SelectorExpr:
			return exprName(expr.X) + "." + expr.Sel.Name
		case *ast.ParenExpr:
			return exprName(expr.X)
		}
		return ""
	}
	for _, spec := range file.Imports {
		imported, _ := strconv.Unquote(spec.Path.Value)
		if imported == "os" {
			alias := "os"
			if spec.Name != nil {
				alias = spec.Name.Name
			}
			prefix := alias + "."
			if alias == "." {
				prefix = ""
			}
			readers[prefix+"Getenv"], readers[prefix+"LookupEnv"] = true, true
		}
	}
	recordValue := func(name ast.Expr, value ast.Expr) {
		values[exprName(name)] = value
		if ident, ok := name.(*ast.Ident); ok && ident.Obj != nil {
			scopedValues[ident.Obj] = value
		}
	}
	// Capture ordinary aliases and literal ranges; collect injected readers by
	// signature rather than requiring a particular parameter name.
	ast.Inspect(file, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.ValueSpec:
			for i, value := range node.Values {
				if i < len(node.Names) {
					recordValue(node.Names[i], value)
				}
			}
		case *ast.AssignStmt:
			for i, value := range node.Rhs {
				if i < len(node.Lhs) {
					recordValue(node.Lhs[i], value)
				}
			}
		case *ast.RangeStmt:
			if list, ok := node.X.(*ast.CompositeLit); ok && node.Value != nil {
				recordValue(node.Value, list)
			}
		case *ast.Field:
			if typ, ok := node.Type.(*ast.FuncType); ok && typ.Params.NumFields() == 1 && typ.Results.NumFields() >= 1 && exprName(typ.Params.List[0].Type) == "string" && exprName(typ.Results.List[0].Type) == "string" {
				for _, name := range node.Names {
					readers[name.Name] = true
				}
			}
		}
		return true
	})
	// Bounded fixed point handles alias chains and same-file wrappers.
	for changed := true; changed; {
		changed = false
		for name, value := range values {
			if !readers[name] && readers[exprName(value)] {
				readers[name], changed = true, true
			}
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || readers[fn.Name.Name] || fn.Type.Params.NumFields() != 1 || fn.Type.Results.NumFields() < 1 || exprName(fn.Type.Params.List[0].Type) != "string" || exprName(fn.Type.Results.List[0].Type) != "string" {
				continue
			}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				if call, ok := node.(*ast.CallExpr); ok && readers[exprName(call.Fun)] {
					readers[fn.Name.Name], changed = true, true
				}
				return true
			})
		}
	}
	var keyValues func(ast.Expr, int) []string
	keyValues = func(expr ast.Expr, depth int) []string {
		if depth > 20 {
			return nil
		}
		switch expr := expr.(type) {
		case *ast.BasicLit:
			if value, err := strconv.Unquote(expr.Value); err == nil {
				return []string{value}
			}
		case *ast.Ident:
			if expr.Obj != nil {
				if value := scopedValues[expr.Obj]; value != nil {
					return keyValues(value, depth+1)
				}
				return nil
			}
			if file.Name.Name == "runtimeconfig" || runtimeDot {
				if value, ok := constants[expr.Name]; ok {
					return []string{value}
				}
			}
		case *ast.SelectorExpr:
			if imports[exprName(expr.X)] == "github.com/iamseth/tao/internal/runtimeconfig" {
				if value, ok := constants[expr.Sel.Name]; ok {
					return []string{value}
				}
			}
		case *ast.CompositeLit:
			var result []string
			for _, elt := range expr.Elts {
				part := keyValues(elt, depth+1)
				if len(part) == 0 {
					return nil
				}
				result = append(result, part...)
			}
			return result
		case *ast.ParenExpr:
			return keyValues(expr.X, depth+1)
		}
		return nil
	}
	var violations []string
	for _, decl := range file.Decls {
		fn, _ := decl.(*ast.FuncDecl)
		ast.Inspect(decl, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := exprName(call.Fun)
			function := call.Fun
			for range 20 {
				alias := values[exprName(function)]
				if alias == nil {
					break
				}
				function = alias
			}
			localName := exprName(function)
			if selector, ok := function.(*ast.SelectorExpr); ok && imports[exprName(selector.X)] == "github.com/iamseth/tao/internal/runtimeconfig" {
				localName = selector.Sel.Name
			} else if file.Name.Name != "runtimeconfig" && !runtimeDot {
				localName = ""
			}
			// Pure built-ins and synthetic preview lookups are not ambient reads.
			// Environment-reader binding belongs only to RuntimeEnv.
			binding := localName == "LoadEnv" && len(call.Args) == 1 && readers[exprName(call.Args[0])]
			capture := localName == "RuntimeEnv"
			allowedBinding := binding && path == "internal/runtimeconfig/env_snapshot.go" && fn != nil && fn.Name.Name == "RuntimeEnv" && exprName(call.Args[0]) == "os.LookupEnv"
			allowedCapture := path == "internal/cli/cli.go" && fn != nil && fn.Name.Name == "Run"
			if (binding && !allowedBinding) || (capture && !allowedCapture) {
				violations = append(violations, fmt.Sprintf("%s: snapshot reload outside invocation boundary", positions.Position(call.Pos())))
			}
			if !readers[name] || len(call.Args) == 0 {
				return true
			}
			// The loader is the only permitted dynamic table lookup. No whole-file
			// exemption: another lookup even here must pass the same checks.
			if path == "internal/runtimeconfig/env_snapshot.go" && fn != nil && fn.Name.Name == "LoadEnv" && exprName(call.Fun) == "lookup" && exprName(call.Args[0]) == "v.name" {
				return true
			}
			resolved := keyValues(call.Args[0], 0)
			bad := len(resolved) == 0
			// A wrapper's parameter is checked at its call sites.
			if bad && fn != nil {
				for _, field := range fn.Type.Params.List {
					for _, name := range field.Names {
						if exprName(call.Args[0]) == name.Name {
							bad = false
						}
					}
				}
			}
			for _, key := range resolved {
				bad = bad || keys[key]
			}
			if bad {
				violations = append(violations, fmt.Sprintf("%s: runtime-table or unresolved environment read through %s; use EnvSnapshot", positions.Position(call.Pos()), exprName(call.Fun)))
			}
			return true
		})
	}
	return violations, nil
}

func TestEnvironmentGuardExamples(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		bad          bool
	}{
		{"direct", `import "os"; var x = os.Getenv("TAO_AGENT")`, true},
		{"constant", `import ("os"; rc "github.com/iamseth/tao/internal/runtimeconfig"); var x = os.Getenv(rc.EnvAgent)`, true},
		{"aliases", `import process "os"; const key = "TAO_AGENT"; var read = process.LookupEnv; func f() { read(key) }`, true},
		{"injected", `func f(read func(string) string) { read("TAO_MODEL") }`, true},
		{"wrapper", `import "os"; func read(k string) string { return os.Getenv(k) }; func f() { read("TAO_REVIEW") }`, true},
		{"wrapped alias", `import "os"; func read(k string) string { return os.Getenv(k) }; var alias = read; var x = alias("TAO_REVIEW")`, true},
		{"ordinary", `import "os"; var x = os.Getenv("HOME")`, false},
		{"prose", `// os.Getenv("TAO_AGENT")\n` + "\n" + `const help = "TAO_AGENT: use os.Getenv for ordinary settings"`, false},
		{"non-table tao", `import "os"; var x = os.Getenv("TAO_DATA_HOME")`, false},
		{"identity fallback", `func f(getenv func(string) string) { for _, key := range []string{"USER", "USERNAME"} { getenv(key) } }`, false},
		{"identity table leak", `func f(getenv func(string) string) { for _, key := range []string{"USER", "TAO_APPROVED_BY"} { getenv(key) } }`, true},
		{"unknown", `import "os"; var x = os.Getenv(computed())`, true},
		{"dot aliases", `import (. "os"; . "github.com/iamseth/tao/internal/runtimeconfig"); var x = Getenv(EnvAgent)`, true},
		{"capture", `import rc "github.com/iamseth/tao/internal/runtimeconfig"; var x = rc.RuntimeEnv()`, true},
		{"capture alias", `import rc "github.com/iamseth/tao/internal/runtimeconfig"; var capture = rc.RuntimeEnv; var x = capture()`, true},
		{"second binding", `import ("os"; rc "github.com/iamseth/tao/internal/runtimeconfig"); var x = rc.LoadEnv(os.LookupEnv)`, true},
		{"builtins", `import rc "github.com/iamseth/tao/internal/runtimeconfig"; var x = rc.LoadEnv(nil)`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			found, err := checkEnvSource("internal/example/example.go", []byte("package example;\n"+tc.source))
			if err != nil || (len(found) > 0) != tc.bad {
				t.Fatalf("violations=%v error=%v, want bad=%t", found, err, tc.bad)
			}
		})
	}
}

func TestEnvironmentGuardLoaderExceptionIsPrecise(t *testing.T) {
	loader := []byte(`package runtimeconfig; func LoadEnv(lookup func(string) (string, bool)) { lookup(v.name) }`)
	for _, path := range []string{"internal/runtimeconfig/env_snapshot.go", "internal/runtimeconfig/other.go"} {
		found, err := checkEnvSource(path, loader)
		if err != nil || (len(found) == 0) != strings.HasSuffix(path, "env_snapshot.go") {
			t.Fatalf("loader exemption %s: %v, %v", path, found, err)
		}
	}
	for _, source := range []string{
		`package runtimeconfig; import "os"; func RuntimeEnv() { LoadEnv(os.LookupEnv) }`,
		`package runtimeconfig; import "os"; func RuntimeEnv() { LoadEnv(os.LookupEnv); os.Getenv("TAO_AGENT") }`,
		`package runtimeconfig; func LoadEnv(lookup func(string) (string, bool)) { lookup(v.name); lookup("TAO_AGENT") }`,
	} {
		found, err := checkEnvSource("internal/runtimeconfig/env_snapshot.go", []byte(source))
		wantBad := strings.Contains(source, "TAO_AGENT")
		if err != nil || (len(found) > 0) != wantBad {
			t.Fatalf("binding exemption: %v, %v", found, err)
		}
	}
	for _, key := range RuntimeEnvKeys() {
		source := fmt.Sprintf("package example; import process %q; var x = process.Getenv(%q)", "os", key)
		found, err := checkEnvSource("internal/example/example.go", []byte(source))
		if err != nil || len(found) != 1 {
			t.Fatalf("unguarded table key %s: %v, %v", key, found, err)
		}
	}
}

func TestEnvironmentGuardScanScopeAndReadErrors(t *testing.T) {
	bad := &fstest.MapFile{Data: []byte(`package p; import "os"; var x = os.Getenv("TAO_AGENT")`)}
	tree := fstest.MapFS{
		"internal/p/p.go": bad, "internal/p/p_test.go": bad,
		".git/hidden.go": bad, ".tao/workspaces/w/p.go": bad,
		"bin/generated.go": bad, "dist/generated.go": bad, "build/generated.go": bad,
		"linked/.git": {Data: []byte("gitdir: elsewhere")}, "linked/p.go": bad,
	}
	found, err := scanEnvSources(tree)
	if err != nil || len(found) != 1 || !strings.Contains(found[0], "internal/p/p.go:") {
		t.Fatalf("scan scope: %v, %v", found, err)
	}
	if _, err := scanEnvSources(unreadableEnvFS{tree}); err == nil {
		t.Fatal("unreadable production source silently ignored")
	}
}

type unreadableEnvFS struct{ fs.FS }

func (f unreadableEnvFS) ReadFile(path string) ([]byte, error) {
	if path == "internal/p/p.go" {
		return nil, fs.ErrPermission
	}
	return fs.ReadFile(f.FS, path)
}
