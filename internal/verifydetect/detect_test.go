package verifydetect

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"testing/fstest"
)

func TestGoPackageDir(t *testing.T) {
	modules := fstest.MapFS{
		"go.mod":           {Data: []byte("// root\nmodule example.com/project // comment\n")},
		"tools/go.mod":     {Data: []byte("module \"example.com/project/tools\"\n")},
		"nested/go.mod":    {Data: []byte("module example.org/other\n")},
		"relocated/go.mod": {Data: []byte("module example.com/project/sub\n")},
	}
	for _, tt := range []struct {
		name, importPath, want string
		empty                  bool
		ok                     bool
	}{
		{name: "root package", importPath: "example.com/project/internal/x", want: "internal/x", ok: true},
		{name: "root module", importPath: "example.com/project", want: ".", ok: true},
		{name: "nested package", importPath: "example.com/project/tools/check", want: "tools/check", ok: true},
		{name: "nested root", importPath: "example.com/project/tools", want: "tools", ok: true},
		{name: "independent nested module", importPath: "example.org/other/pkg", want: "nested/pkg", ok: true},
		{name: "longest module prefix", importPath: "example.com/project/sub/pkg", want: "relocated/pkg", ok: true},
		{name: "foreign", importPath: "example.org/foreign"},
		{name: "prefix boundary", importPath: "example.com/project-other"},
		{name: "traversal", importPath: "example.com/project/../escape"},
		{name: "no module", importPath: "example.com/project", empty: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			files := modules
			if tt.empty {
				files = fstest.MapFS{}
			}
			got, ok := (Detector{FS: files}).GoPackageDir(tt.importPath)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("GoPackageDir(%q) = %q, %v; want %q, %v", tt.importPath, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestOpenRootUsesAccessibleDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/project\n"), 0o600); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	detector, ok := OpenRoot(root)
	if !ok {
		t.Fatal("OpenRoot rejected accessible directory")
	}
	assertCommands(t, detector.DetectCommands(), []string{"go build ./...", "go test ./..."})
}

func TestOpenRootRejectsUnavailableRoot(t *testing.T) {
	if _, ok := OpenRoot(filepath.Join(t.TempDir(), "missing")); ok {
		t.Fatal("OpenRoot accepted missing directory")
	}
}

func TestDetectCommandsPrefersMakeVerifyTarget(t *testing.T) {
	detector := Detector{FS: fstest.MapFS{
		"Makefile": {Data: []byte(".PHONY: verify build test\nverify: build test\n\t@echo verified\nbuild:\n\tgo build ./...\ntest:\n\tgo test ./...\n")},
	}}

	assertCommands(t, detector.DetectCommands(), []string{"make verify"})
}

func TestDetectCommandsUsesMakeBuildAndTestTargets(t *testing.T) {
	detector := Detector{FS: fstest.MapFS{
		"Makefile": {Data: []byte(".PHONY: build test\nbuild:\n\tgo build ./...\ntest: deps\n\tgo test ./...\n")},
	}}

	assertCommands(t, detector.DetectCommands(), []string{"make build", "make test"})
}

func TestDetectCommandsOmitsMissingMakeTargets(t *testing.T) {
	detector := Detector{FS: fstest.MapFS{
		"Makefile": {Data: []byte("test:\n\tgo test ./...\n")},
	}}

	assertCommands(t, detector.DetectCommands(), []string{"make test"})
}

func TestDetectCommandsUsesGoModule(t *testing.T) {
	detector := Detector{FS: fstest.MapFS{
		"go.mod": {Data: []byte("module example.com/project\n")},
	}}

	assertCommands(t, detector.DetectCommands(), []string{"go build ./...", "go test ./..."})
}

func TestDetectCommandsFallsBackToGoWhenMakefileHasNoTargets(t *testing.T) {
	detector := Detector{FS: fstest.MapFS{
		"Makefile": {Data: []byte("lint:\n\tgo vet ./...\n")},
		"go.mod":   {Data: []byte("module example.com/project\n")},
	}}

	assertCommands(t, detector.DetectCommands(), []string{"go build ./...", "go test ./..."})
}

func TestDetectorGoModuleForPath(t *testing.T) {
	detector := Detector{FS: fstest.MapFS{
		"go.mod":                      {Data: []byte("module example.com/root\n")},
		"services/api/go.mod":         {Data: []byte("module example.com/api\n")},
		"services/api/server/main.go": {Data: []byte("package server\n")},
	}}

	tests := []struct {
		name       string
		file       string
		wantModule string
		wantOK     bool
	}{
		{name: "root module", file: "internal/task/task.go", wantModule: ".", wantOK: true},
		{name: "nearest nested module", file: "services/api/server/main.go", wantModule: "services/api", wantOK: true},
		{name: "invalid parent path", file: "../outside.go"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			module, ok := detector.GoModuleForPath(test.file)
			if module != test.wantModule || ok != test.wantOK {
				t.Fatalf("GoModuleForPath(%q) = (%q, %t), want (%q, %t)", test.file, module, ok, test.wantModule, test.wantOK)
			}
		})
	}
}

func TestDetectorGoModuleForPathReturnsFalseWithoutModule(t *testing.T) {
	detector := Detector{FS: fstest.MapFS{
		"src/project.go": {Data: []byte("package project\n")},
	}}
	if module, ok := detector.GoModuleForPath("src/project.go"); ok || module != "" {
		t.Fatalf("GoModuleForPath() = (%q, %t), want no module", module, ok)
	}
}

func TestDetectCommandJoinsDetectedCommands(t *testing.T) {
	root := t.TempDir()
	if got := DetectCommand(root); got != "" {
		t.Fatalf("DetectCommand() = %q, want empty", got)
	}
}

func TestDetectCommandsReturnsEmptyForUnrecognizedRoot(t *testing.T) {
	detector := Detector{FS: fstest.MapFS{
		"README.md": {Data: []byte("# project\n")},
	}}

	assertCommands(t, detector.DetectCommands(), []string{})
}

func assertCommands(t *testing.T, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("DetectCommands() = %v, want %v", got, want)
	}
}
