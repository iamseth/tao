package plan

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const logFileName = "agent-run.log"

func LogPath(planDir string) string {
	return filepath.Join(planDir, logFileName)
}

func OpenLogAppend(planDir string) (*os.File, error) {
	return fileArtifactStore{}.openLogAppend(planDir)
}

func ReadLog(planDir string) (string, error) {
	return fileArtifactStore{}.readLog(planDir)
}

func ReadLogTail(planDir string, tail int) (string, error) {
	return fileArtifactStore{}.readLogTail(planDir, tail)
}

func FollowLog(ctx context.Context, planDir string, out io.Writer) error {
	return fileArtifactStore{}.followLog(ctx, planDir, out)
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
