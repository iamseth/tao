package verification

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

type lookup struct {
	root        string
	dirCache    map[string]bool
	pathCache   map[string]bool
	packageDirs map[string]packageDirResult
}

type packageDirResult struct {
	dir   string
	found bool
}

func newLookup(repoRoot string) *lookup {
	return &lookup{
		root:        repoRoot,
		dirCache:    make(map[string]bool),
		pathCache:   make(map[string]bool),
		packageDirs: make(map[string]packageDirResult),
	}
}

func (l *lookup) repoRoot() string {
	return l.root
}

func (l *lookup) resolve(cwd string, value string) string {
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Clean(filepath.Join(cwd, value))
}

func (l *lookup) dirExists(path string) bool {
	if exists, ok := l.dirCache[path]; ok {
		return exists
	}
	info, err := os.Stat(path)
	exists := err == nil && info.IsDir()
	l.dirCache[path] = exists
	return exists
}

func (l *lookup) pathExists(path string) bool {
	if exists, ok := l.pathCache[path]; ok {
		return exists
	}
	_, err := os.Stat(path)
	exists := err == nil || !errors.Is(err, os.ErrNotExist)
	l.pathCache[path] = exists
	return exists
}

func (l *lookup) findPackageDir(name string) (string, bool) {
	name = strings.Trim(name, ".")
	if cached, ok := l.packageDirs[name]; ok {
		return cached.dir, cached.found
	}
	var found string
	_ = filepath.WalkDir(l.root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || found != "" {
			return nil //nolint:nilerr // walk errors are skipped to continue traversal
		}
		if entry.IsDir() {
			base := entry.Name()
			if base == ".git" || base == "node_modules" || base == ".tao" || base == "bin" {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() != "package.json" {
			return nil
		}
		content, readErr := os.ReadFile(path) //nolint:gosec // G304: path comes from filesystem walk of repo
		if readErr != nil {
			return nil //nolint:nilerr // unreadable package.json is skipped, not fatal
		}
		var pkg struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(content, &pkg) == nil && pkg.Name == name {
			found = filepath.Dir(path)
		}
		return nil
	})
	l.packageDirs[name] = packageDirResult{dir: found, found: found != ""}
	return found, found != ""
}

func (l *lookup) packageRelativeSuggestion(cwd string, arg string) (string, string, bool) {
	if filepath.IsAbs(arg) {
		return "", "", false
	}
	rootRelative := l.resolve(l.root, arg)
	if !l.pathExists(rootRelative) {
		return "", "", false
	}
	rel, err := filepath.Rel(cwd, rootRelative)
	if err != nil || strings.HasPrefix(rel, "..") || rel == "." {
		return "", "", false
	}
	return filepath.ToSlash(rel), rootRelative, true
}

func obviousPathArgs(tokens []string) []string {
	paths := make([]string, 0)
	for _, token := range tokens[1:] {
		if token == "--" || strings.HasPrefix(token, "-") || strings.Contains(token, "=") {
			continue
		}
		if isGoTestPackage(token) {
			continue
		}
		if looksLikeFilePath(token) {
			paths = append(paths, token)
		}
	}
	return paths
}

func isGoTestPackage(token string) bool {
	return token == "." || token == "./..." || strings.HasSuffix(token, "/...")
}

func looksLikeFilePath(token string) bool {
	ext := strings.ToLower(filepath.Ext(token))
	if ext == "" {
		return false
	}
	switch ext {
	case ".go", ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs", ".mts", ".cts", ".json":
		return strings.Contains(token, "/") || strings.Contains(token, "test") || strings.Contains(token, "spec")
	default:
		return false
	}
}

func replaceCommandToken(command string, old string, replacement string) string {
	if old == replacement {
		return command
	}
	start := strings.Index(command, old)
	if start < 0 {
		return command
	}
	end := start + len(old)
	return command[:start] + replacement + command[end:]
}
