// Package verifydetect detects repository verification commands from build-system files.
package verifydetect

import (
	"io/fs"
	"os"
	"strings"
)

// Detector detects ordered verification commands against an injectable filesystem.
type Detector struct {
	// FS is the repository-rooted filesystem to probe. When nil, the current
	// working directory is used.
	FS fs.FS
}

// DetectCommands probes root and returns ordered verification commands for the
// first recognized build system.
func DetectCommands(root string) []string {
	return Detector{FS: os.DirFS(root)}.DetectCommands()
}

// DetectCommand returns the detected repository verification as one shell
// command. Callers that need repository-owned broad verification share this
// resolution rather than duplicating build-system precedence.
func DetectCommand(root string) string {
	return strings.Join(DetectCommands(root), " && ")
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
