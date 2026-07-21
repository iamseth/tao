package verifydetect

import (
	"slices"
	"testing"
	"testing/fstest"
)

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
