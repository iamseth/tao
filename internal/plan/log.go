package plan

import (
	"path/filepath"
	"strings"
)

const logFileName = "agent-run.log"

func LogPath(planDir string) string {
	return filepath.Join(planDir, logFileName)
}

func lastLines(value string, count int) string {
	if count <= 0 || value == "" {
		return value
	}
	trimmed := strings.TrimSuffix(value, "\n")
	lines := strings.Split(trimmed, "\n")
	if len(lines) <= count {
		return value
	}
	return strings.Join(lines[len(lines)-count:], "\n") + "\n"
}
