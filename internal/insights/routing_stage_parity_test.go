package insights

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

type routingStageDeclaration struct {
	name, value string
	position    token.Position
}

type routingStageConstant struct {
	name     string
	typ      ast.Expr
	value    ast.Expr
	position token.Position
}

// Scan every production file, including platform-specific files, without compiling
// planning or depending on its imports. Unsupported stage expressions fail closed.
func discoverRoutingStages(source fs.FS) ([]routingStageDeclaration, error) {
	entries, err := fs.ReadDir(source, ".")
	if err != nil {
		return nil, fmt.Errorf("read planning sources: %w", err)
	}
	fset := token.NewFileSet()
	constants := make(map[string]routingStageConstant)
	var order []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := fs.ReadFile(source, name)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		file, err := parser.ParseFile(fset, name, data, 0)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		for _, decl := range file.Decls {
			group, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			// A new alias could hide stages from direct type matching; require an
			// explicit scanner extension rather than silently overlooking it.
			if group.Tok == token.TYPE {
				for _, spec := range group.Specs {
					ts := spec.(*ast.TypeSpec)
					_, directType := ast.Unparen(ts.Type).(*ast.Ident)
					if ts.Name.Name != "GenerationStage" && (directType || ts.Assign.IsValid()) && mentionsGenerationStage(ts.Type) {
						return nil, fmt.Errorf("%s: unsupported type derived from GenerationStage: %s", fset.Position(ts.Pos()), ts.Name.Name)
					}
				}
			}
			if group.Tok != token.CONST {
				continue
			}
			var inheritedType ast.Expr
			var inheritedValues []ast.Expr
			for _, spec := range group.Specs {
				vs := spec.(*ast.ValueSpec)
				typ, values := vs.Type, vs.Values
				if len(values) == 0 {
					typ, values = inheritedType, inheritedValues
				} else {
					inheritedType, inheritedValues = typ, values
				}
				if len(values) != len(vs.Names) {
					return nil, fmt.Errorf("%s: unsupported constant value arity", fset.Position(vs.Pos()))
				}
				for i, id := range vs.Names {
					if _, exists := constants[id.Name]; exists {
						return nil, fmt.Errorf("%s: duplicate constant %s", fset.Position(id.Pos()), id.Name)
					}
					// Blank constants still matter when explicitly stage-typed.
					key := id.Name
					if key == "_" {
						key = fmt.Sprintf("_@%s", fset.Position(id.Pos()))
					}
					constants[key] = routingStageConstant{id.Name, typ, values[i], fset.Position(id.Pos())}
					order = append(order, key)
				}
			}
		}
	}
	var resolve func(string, map[string]bool) (string, bool, error)
	resolve = func(name string, visiting map[string]bool) (string, bool, error) {
		c, exists := constants[name]
		if !exists {
			return "", false, nil
		}
		if visiting[name] {
			return "", false, fmt.Errorf("%s: cyclic constant %s", c.position, c.name)
		}
		visiting[name] = true
		defer delete(visiting, name)
		relevant := mentionsGenerationStage(c.typ) || mentionsGenerationStage(c.value)
		var dependencyErr error
		ast.Inspect(c.value, func(node ast.Node) bool {
			if id, ok := node.(*ast.Ident); ok {
				_, stage, err := resolve(id.Name, visiting)
				relevant = relevant || stage
				if err != nil {
					dependencyErr = err
				}
			}
			return true
		})
		if dependencyErr != nil {
			return "", relevant, dependencyErr
		}
		if !relevant {
			return "", false, nil
		}
		fail := func() (string, bool, error) {
			return "", true, fmt.Errorf("%s: unsupported GenerationStage expression for %s (type %T, value %T)", c.position, c.name, c.typ, c.value)
		}
		if c.typ != nil {
			id, ok := c.typ.(*ast.Ident)
			if !ok || id.Name != "GenerationStage" {
				return fail()
			}
		}
		switch value := c.value.(type) {
		case *ast.BasicLit:
			if value.Kind != token.STRING {
				return fail()
			}
			decoded, err := strconv.Unquote(value.Value)
			if err != nil {
				return "", true, fmt.Errorf("%s: decode %s: %w", c.position, c.name, err)
			}
			return decoded, true, nil
		case *ast.Ident:
			decoded, stage, err := resolve(value.Name, visiting)
			if err != nil {
				return "", true, err
			}
			if stage {
				return decoded, true, nil
			}
		}
		return fail()
	}
	var stages []routingStageDeclaration
	for _, name := range order {
		value, stage, err := resolve(name, make(map[string]bool))
		if err != nil {
			return nil, err
		}
		if stage {
			c := constants[name]
			stages = append(stages, routingStageDeclaration{c.name, value, c.position})
		}
	}
	if len(stages) == 0 {
		return nil, fmt.Errorf("no GenerationStage constants discovered in production Go files")
	}
	return stages, nil
}

func mentionsGenerationStage(expr ast.Expr) bool {
	found := false
	if expr != nil {
		ast.Inspect(expr, func(node ast.Node) bool {
			if id, ok := node.(*ast.Ident); ok && id.Name == "GenerationStage" {
				found = true
			}
			return true
		})
	}
	return found
}

func routingStageParity(stage routingStageDeclaration) error {
	if got := RoutingStageLabel(stage.value); got != stage.value {
		return fmt.Errorf("%s: %s = %q: RoutingStageLabel = %q; add an identity routing label", stage.position, stage.name, stage.value, got)
	}
	return nil
}

func TestGenerationStageRoutingParity(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate routing parity test source")
	}
	stages, err := discoverRoutingStages(os.DirFS(filepath.Join(filepath.Dir(filename), "..", "planning")))
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range stages {
		if err := routingStageParity(stage); err != nil {
			t.Error(err)
		}
	}
}

func TestDiscoverRoutingStages(t *testing.T) {
	source := fstest.MapFS{
		"stages.go": {Data: []byte("package planning\ntype GenerationStage string\nconst (\nFirst GenerationStage = `slice_prompt`\nInherited\nLeft, Right GenerationStage = `slice_agent`, `slice_validation`\n)\nconst Alias = First\n")},
		"added.go":  {Data: []byte("package planning\nconst Added GenerationStage = `future_unlabeled_stage`\n")},
		// Invalid syntax also proves test files are never parsed.
		"ignored_test.go": {Data: []byte("const TestOnly GenerationStage = `secret`\ninvalid syntax")},
	}
	stages, err := discoverRoutingStages(source)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"First": "slice_prompt", "Inherited": "slice_prompt", "Left": "slice_agent", "Right": "slice_validation", "Alias": "slice_prompt", "Added": "future_unlabeled_stage"}
	if len(stages) != len(want) {
		t.Fatalf("declarations = %+v", stages)
	}
	for _, stage := range stages {
		if value, ok := want[stage.name]; !ok || value != stage.value {
			t.Errorf("unexpected declaration: %+v", stage)
		}
		err := routingStageParity(stage)
		if stage.name == "Added" {
			if stage.position.Filename != "added.go" || stage.position.Line != 2 {
				t.Errorf("location = %s", stage.position)
			}
			if err == nil || !strings.Contains(err.Error(), "Added") || !strings.Contains(err.Error(), "added.go:2:") {
				t.Errorf("missing actionable parity failure: %v", err)
			}
		} else if err != nil {
			t.Error(err)
		}
	}
}

func TestDiscoverRoutingStagesFailures(t *testing.T) {
	for _, tc := range []struct{ name, source, want string }{
		{"empty", "package planning", "no GenerationStage"},
		{"malformed", "package planning\nconst (", "parse stages.go"},
		{"expression", "package planning\nconst Added GenerationStage = `slice_` + `prompt`", "unsupported GenerationStage expression for Added"},
		{"inherited expression", "package planning\nconst (Added GenerationStage = iota; Next)", "unsupported GenerationStage expression"},
		{"conversion", "package planning\nconst Added = GenerationStage(`future`)", "unsupported GenerationStage expression for Added"},
		{"derived expression", "package planning\nconst First GenerationStage = `slice_prompt`\nconst Added = First + `future`", "unsupported GenerationStage expression for Added"},
		{"alias type", "package planning\ntype Other = GenerationStage\nconst Added Other = `future`", "unsupported type derived"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := discoverRoutingStages(fstest.MapFS{"stages.go": {Data: []byte(tc.source)}})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
	t.Run("read directory", func(t *testing.T) {
		_, err := discoverRoutingStages(os.DirFS(filepath.Join(t.TempDir(), "missing")))
		if err == nil || !strings.Contains(err.Error(), "read planning sources") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestRoutingStageLabelSanitization(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"", "unspecified"},
		{"arbitrary-stage", "unknown"},
		{"password=secret\n/home/private/plan", "unknown"},
	} {
		if got := RoutingStageLabel(tc.input); got != tc.want {
			t.Errorf("RoutingStageLabel(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
