package verification

import (
	"os"
	"path/filepath"
	"strings"
)

// MechanicalCorrection returns a narrowly validated command/cwd candidate, not
// an execution result. It accepts only the grammar documented by this package;
// neither diagnostic prose nor an advisory suggested command proves equivalence.
func MechanicalCorrection(repoRoot string, failed Run) (Run, bool) {
	root, ok := correctionDirectory(repoRoot)
	if !ok {
		return Run{}, false
	}
	cwd := root
	if failed.CWD != "" {
		cwd, ok = correctionCWD(root, failed.CWD, root)
		if !ok {
			return Run{}, false
		}
	}
	tokens := strings.Split(failed.Command, " ")
	if len(tokens) >= 4 && tokens[0] == "cd" && tokens[2] == "&&" {
		if !correctionWord(tokens[1]) || strings.HasPrefix(tokens[1], "-") {
			return Run{}, false
		}
		cwd, ok = correctionCWD(cwd, tokens[1], root)
		if !ok {
			return Run{}, false
		}
		tokens = tokens[3:]
	}
	if cwd == root || len(tokens) < 3 || tokens[0] != "go" || tokens[1] != "test" {
		return Run{}, false
	}
	for _, token := range tokens {
		if !correctionWord(token) {
			return Run{}, false
		}
	}
	// Classification is a prerequisite, not authorization. In particular, a
	// missing executable/config or ordinary code failure cannot be repaired here.
	classification := ClassifyRun(root, Run{Command: failed.Command, CWD: cwd, Details: failed.Details})
	if classification.Code != "verification_no_test_files" && classification.Code != "verification_path_cwd_mismatch" {
		return Run{}, false
	}
	targets, ok := correctionTargets(tokens)
	if !ok {
		return Run{}, false
	}
	prefix, err := filepath.Rel(root, cwd)
	if err != nil {
		return Run{}, false
	}
	prefix = filepath.ToSlash(prefix) + "/"
	changed := -1
	var replacement string
	for _, i := range targets {
		arg := strings.TrimPrefix(tokens[i], "./")
		if !strings.HasPrefix(arg, prefix) {
			continue
		}
		if changed != -1 {
			return Run{}, false
		}
		replacement, ok = correctedTarget(root, cwd, tokens[i], prefix)
		if !ok {
			return Run{}, false
		}
		changed = i
	}
	if changed == -1 {
		return Run{}, false
	}
	tokens[changed] = replacement
	return Run{Command: strings.Join(tokens, " "), CWD: cwd}, true
}

// Deliberately not a shell lexer: reject quoting, expansion, control characters,
// Unicode whitespace, and every metacharacter rather than trying to emulate sh.
func correctionWord(word string) bool {
	if word == "" {
		return false
	}
	for _, r := range word {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_./-=:,+@%^", r) {
			continue
		}
		return false
	}
	return true
}

// Only known Go flags can be skipped without mistaking a flag value (especially
// a test filter) for a target. Unknown flags and test-binary passthrough fail closed.
func correctionTargets(tokens []string) ([]int, bool) {
	var targets []int
	for i := 2; i < len(tokens); i++ {
		arg := tokens[i]
		if !strings.HasPrefix(arg, "-") {
			targets = append(targets, i)
			continue
		}
		name, value, assigned := strings.Cut(arg, "=")
		switch name {
		case "-race", "-short", "-v", "-failfast", "-cover", "-fullpath", "-json", "-x", "-work":
			if assigned && value != "true" && value != "false" {
				return nil, false
			}
		case "-run", "-skip", "-bench", "-benchtime", "-count", "-cpu", "-parallel", "-timeout", "-shuffle", "-tags":
			if assigned {
				if value == "" {
					return nil, false
				}
			} else {
				i++
				if i >= len(tokens) || strings.HasPrefix(tokens[i], "-") {
					return nil, false
				}
			}
		default:
			return nil, false
		}
	}
	return targets, true
}

func correctedTarget(root, cwd, target, prefix string) (string, bool) {
	explicit := strings.HasPrefix(target, "./")
	path := strings.TrimPrefix(target, "./")
	recursive := strings.HasSuffix(path, "/...")
	path = strings.TrimSuffix(path, "/...")
	if !cleanCorrectionPath(path) || filepath.IsAbs(path) || !correctionPathMissing(cwd, path) {
		return "", false
	}
	corrected := strings.TrimPrefix(path, prefix)
	if corrected == path || corrected == "" || strings.HasPrefix(corrected, "-") {
		return "", false
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(cwd, corrected))
	if err != nil || !correctionInside(root, resolved) {
		return "", false
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", false
	}
	if info.IsDir() {
		// Bare Go import paths are not necessarily repository-relative paths.
		if !explicit {
			return "", false
		}
	} else if !info.Mode().IsRegular() || !strings.HasSuffix(path, ".go") || recursive {
		return "", false
	}
	if explicit {
		corrected = "./" + corrected
	}
	if recursive {
		corrected += "/..."
	}
	return corrected, true
}

func correctionDirectory(path string) (string, bool) {
	if path == "" {
		return "", false
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", false
	}
	info, err := os.Stat(canonical)
	return canonical, err == nil && info.IsDir()
}

func correctionCWD(base, path, root string) (string, bool) {
	if !cleanCorrectionPath(strings.TrimPrefix(path, "./")) {
		return "", false
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	canonical, ok := correctionDirectory(path)
	return canonical, ok && correctionInside(root, canonical)
}

func cleanCorrectionPath(path string) bool {
	if path == "" || filepath.Clean(path) != path || strings.ContainsAny(path, "\\\x00\r\n") {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == ".." || part == "." {
			return false
		}
	}
	return true
}

func correctionInside(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// Prove absence without following an existing symlink (including dangling or
// escaping links) along the incorrect cwd-relative path.
func correctionPathMissing(cwd, path string) bool {
	for _, part := range strings.Split(path, "/") {
		cwd = filepath.Join(cwd, part)
		info, err := os.Lstat(cwd)
		if err != nil {
			return os.IsNotExist(err)
		}
		if !info.IsDir() {
			return false
		}
	}
	return false
}
