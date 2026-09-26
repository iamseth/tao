package steal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const MaxCloneBytes int64 = 200 << 20
const PromptMaxFiles = 400
const PromptMaxFileBytes = 64 << 10
const PromptMaxTotalBytes = 2 << 20

// Omission identifies content the scouting prompt must not read.
type Omission struct {
	Path, Reason string
}

func generatedDirectory(name string) bool {
	switch name {
	case "node_modules", "vendor", "dist", "build", "target", ".venv", "__pycache__":
		return true
	}
	return false
}

func requireDirectory(root string) error {
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", root)
	}
	return nil
}

// HardenSnapshot removes special files without following links, makes regular
// files read-only, and inventories prompt exclusions. Generated and binary
// content stays on disk and counts toward the cap; Git metadata does not.
func HardenSnapshot(root string, maxBytes int64) ([]Omission, int64, error) {
	if err := requireDirectory(root); err != nil {
		return nil, 0, err
	}
	scope, err := os.OpenRoot(root)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = scope.Close() }()
	var omitted []Omission
	var total int64
	err = fs.WalkDir(scope.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel := path
		info, err := entry.Info()
		if err != nil {
			return err
		}
		omit := func(reason string) { omitted = append(omitted, Omission{Path: rel, Reason: reason}) }
		if info.Mode()&os.ModeSymlink != 0 {
			omit("symlink")
			return scope.Remove(path)
		}
		if info.IsDir() {
			if rel != "." && generatedDirectory(entry.Name()) {
				omit("generated")
			}
			return scope.Chmod(path, 0755) // #nosec G302 -- snapshot directories intentionally remain traversable.
		}
		if !info.Mode().IsRegular() {
			omit("non-regular")
			return scope.Remove(path)
		}
		if rel != ".git" && !strings.HasPrefix(rel, ".git/") {
			total += info.Size()
			if total > maxBytes {
				return fmt.Errorf("snapshot exceeds cap of %d bytes", maxBytes)
			}
			prefix, err := readPrefix(scope, path, 8<<10)
			if err != nil {
				return err
			}
			if bytes.IndexByte(prefix, 0) >= 0 {
				omit("binary")
			}
		}
		return scope.Chmod(path, 0444) // #nosec G302 -- public source files are deliberately read-only.
	})
	return omitted, total, err
}

// ObjectStoreBytes counts all regular bytes under .git without following links.
func ObjectStoreBytes(root string) (int64, error) {
	gitDir := filepath.Join(root, ".git")
	if err := requireDirectory(gitDir); err != nil {
		return 0, err
	}
	scope, err := os.OpenRoot(root)
	if err != nil {
		return 0, err
	}
	defer func() { _ = scope.Close() }()
	var total int64
	err = fs.WalkDir(scope.FS(), ".git", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("non-regular Git metadata at %s", path)
		}
		total += info.Size()
		return nil
	})
	return total, err
}

func readPrefix(root *os.Root, path string, limit int64) ([]byte, error) {
	info, err := root.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	file, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	return io.ReadAll(io.LimitReader(file, limit))
}

var declaredVersion = regexp.MustCompile(`^v?[0-9]+(?:\.[0-9]+)*(?:[-+][a-zA-Z0-9.+-]+)?$`)
var tomlVersion = regexp.MustCompile(`^version\s*=\s*["']([^"']+)["']\s*(?:#.*)?$`)
var goVersion = regexp.MustCompile(`(?m)^go\s+([0-9]+\.[0-9]+(?:\.[0-9]+)?)\s*(?://[^\n]*)?$`)

// DeclaredVersion is a bounded, best-effort textual probe, not a manifest parser
// or an execution of packaging tools. For Go modules it reports the go directive.
func DeclaredVersion(root string) string {
	scope, err := os.OpenRoot(root)
	if err != nil {
		return ""
	}
	defer func() { _ = scope.Close() }()
	for _, name := range []string{"VERSION", "package.json", "Cargo.toml", "pyproject.toml", "go.mod"} {
		data, err := readPrefix(scope, name, 4<<10)
		if err != nil {
			continue
		}
		var version string
		switch name {
		case "VERSION":
			version = strings.TrimSpace(string(data))
		case "package.json":
			var manifest struct {
				Version string `json:"version"`
			}
			if json.Unmarshal(data, &manifest) == nil {
				version = manifest.Version
			}
		case "Cargo.toml", "pyproject.toml":
			section := ""
			for line := range strings.SplitSeq(string(data), "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "[") {
					section, _, _ = strings.Cut(line, "#")
					section = strings.TrimSpace(section)
				}
				if (name == "Cargo.toml" && section == "[package]") || (name == "pyproject.toml" && (section == "[project]" || section == "[tool.poetry]")) {
					if match := tomlVersion.FindStringSubmatch(line); match != nil {
						version = match[1]
						break
					}
				}
			}
		case "go.mod":
			if match := goVersion.FindSubmatch(data); match != nil {
				version = string(match[1])
			}
		}
		if declaredVersion.MatchString(version) {
			return version
		}
	}
	return ""
}
