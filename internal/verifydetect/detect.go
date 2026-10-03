// Package verifydetect detects repository verification commands from build-system files.
package verifydetect

import (
	"io/fs"
	"os"
	"path"
	"strconv"
	"strings"
)

// Detector detects ordered verification commands against an injectable filesystem.
type Detector struct {
	// FS is the repository-rooted filesystem to probe. When nil, the current
	// working directory is used.
	FS fs.FS
}

// OpenRoot returns a detector when root names an accessible directory.
func OpenRoot(root string) (Detector, bool) {
	root = strings.TrimSpace(root)
	info, err := os.Stat(root)
	if root == "" || err != nil || !info.IsDir() {
		return Detector{}, false
	}
	return Detector{FS: os.DirFS(root)}, true
}

// DetectCommands probes root and returns ordered verification commands for the
// first recognized build system.
func DetectCommands(root string) []string {
	detector, ok := OpenRoot(root)
	if !ok {
		return []string{}
	}
	return detector.DetectCommands()
}

// DetectCommand returns the detected repository verification as one shell
// command. Callers that need repository-owned broad verification share this
// resolution rather than duplicating build-system precedence.
func DetectCommand(root string) string {
	return strings.Join(DetectCommands(root), " && ")
}

// GoModuleForPath returns the repository-relative Go module containing file.
func (d Detector) GoModuleForPath(file string) (string, bool) {
	file = path.Clean(strings.TrimSpace(strings.ReplaceAll(file, "\\", "/")))
	if !fs.ValidPath(file) || file == "." {
		return "", false
	}

	fileSystem := d.FS
	if fileSystem == nil {
		fileSystem = os.DirFS(".")
	}
	for dir := path.Dir(file); ; dir = path.Dir(dir) {
		manifest := path.Join(dir, "go.mod")
		info, err := fs.Stat(fileSystem, manifest)
		if err == nil && !info.IsDir() {
			return dir, true
		}
		if dir == "." {
			return "", false
		}
	}
}

// GoPackageDir maps an import path to a repository-relative directory using the
// most specific discovered module directive. It does not execute Go commands or
// assert that the package directory exists.
func (d Detector) GoPackageDir(importPath string) (string, bool) {
	if !fs.ValidPath(importPath) || strings.ContainsAny(importPath, "\\\\ \t\r\n") {
		return "", false
	}
	fileSystem := d.FS
	if fileSystem == nil {
		fileSystem = os.DirFS(".")
	}
	bestModule, result := "", ""
	err := fs.WalkDir(fileSystem, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == "vendor" {
				return fs.SkipDir
			}
			return nil
		}
		if entry.Name() != "go.mod" {
			return nil
		}
		content, err := fs.ReadFile(fileSystem, name)
		if err != nil {
			return err
		}
		module := goModulePath(string(content))
		if module == "" || len(module) <= len(bestModule) {
			return nil
		}
		if importPath != module && !strings.HasPrefix(importPath, module+"/") {
			return nil
		}
		bestModule = module
		suffix := strings.TrimPrefix(strings.TrimPrefix(importPath, module), "/")
		result = path.Join(path.Dir(name), suffix)
		return nil
	})
	if err != nil || bestModule == "" {
		return "", false
	}
	return result, true
}

func goModulePath(content string) string {
	for line := range strings.SplitSeq(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "module" {
			continue
		}
		module := fields[1]
		if strings.HasPrefix(module, `"`) || strings.HasPrefix(module, "`") {
			unquoted, err := strconv.Unquote(module)
			if err != nil {
				return ""
			}
			module = unquoted
		}
		if !fs.ValidPath(module) || module == "." {
			return ""
		}
		return module
	}
	return ""
}

// DetectCommands returns ordered verification commands for the first recognized
// build system in the detector filesystem.
func (d Detector) DetectCommands() []string {
	fileSystem := d.FS
	if fileSystem == nil {
		fileSystem = os.DirFS(".")
	}

	probes := []func(fs.FS) []string{
		detectMakeCommands,
		detectGoCommands,
	}
	for _, probe := range probes {
		commands := probe(fileSystem)
		if len(commands) > 0 {
			return commands
		}
	}
	return []string{}
}

func detectMakeCommands(fileSystem fs.FS) []string {
	for _, name := range []string{"Makefile", "makefile"} {
		content, err := fs.ReadFile(fileSystem, name)
		if err != nil {
			continue
		}
		commands := makeCommands(content)
		if len(commands) > 0 {
			return commands
		}
	}
	return nil
}

func makeCommands(content []byte) []string {
	var commands []string
	targets := makeTargets(content)
	if targets["verify"] {
		return []string{"make verify"}
	}
	if targets["build"] {
		commands = append(commands, "make build")
	}
	if targets["test"] {
		commands = append(commands, "make test")
	}
	return commands
}

func makeTargets(content []byte) map[string]bool {
	targets := map[string]bool{}
	for line := range strings.SplitSeq(string(content), "\n") {
		line = strings.TrimSuffix(line, "\r")
		switch {
		case strings.HasPrefix(line, "verify:"):
			targets["verify"] = true
		case strings.HasPrefix(line, "build:"):
			targets["build"] = true
		case strings.HasPrefix(line, "test:"):
			targets["test"] = true
		}
	}
	return targets
}

func detectGoCommands(fileSystem fs.FS) []string {
	info, err := fs.Stat(fileSystem, "go.mod")
	if err != nil || info.IsDir() {
		return nil
	}
	return []string{"go build ./...", "go test ./..."}
}
