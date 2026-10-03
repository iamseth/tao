package plan

import (
	"path/filepath"
	"strings"
)

const logFileName = "agent-run.log"
const uiLaunchLogFileName = "ui-launch.log"

// UILaunchLogPath returns the local diagnostic log for detached dashboard actions.
func UILaunchLogPath(planDir string) string {
	return filepath.Join(planDir, uiLaunchLogFileName)
}

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
