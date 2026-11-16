package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/cli"
)

func TestReportExitPreservesTypedNoteRunOutcomes(t *testing.T) {
	for code, assigned := range []int{cli.NoteRunCompleted, cli.NoteRunInfrastructure, cli.NoteRunUsage, cli.NoteRunBlocked, cli.NoteRunDecision, cli.NoteRunPlanningRefusal, cli.NoteRunNothingToDo, cli.NoteRunMergePending} {
		if code != assigned {
			t.Fatalf("numeric contract: index=%d value=%d", code, assigned)
		}
		var errOut bytes.Buffer
		err := fmt.Errorf("wrapped: %w", &cli.NoteRunOutcome{Code: code, Cause: errors.New("outcome")})
		if got := reportExit(err, &errOut); got != code || !strings.Contains(errOut.String(), "wrapped: outcome") {
			t.Fatalf("code=%d got=%d stderr=%s", code, got, &errOut)
		}
	}
	merge := &cli.NoteRunOutcome{Code: cli.NoteRunMergePending}
	for _, tc := range []struct {
		err  error
		code int
	}{
		{errors.Join(merge, merge), 7},
		{errors.Join(merge, errors.New("transport")), 1},
		{errors.Join(merge, &cli.NoteRunOutcome{Code: cli.NoteRunDecision}), 1},
	} {
		var output bytes.Buffer
		if got := reportExit(tc.err, &output); got != tc.code {
			t.Fatalf("joined outcome=%d want=%d", got, tc.code)
		}
	}
	var errOut bytes.Buffer
	if reportExit(nil, &errOut) != 0 || errOut.Len() != 0 {
		t.Fatal("nil result changed")
	}
	if reportExit(errors.New("ordinary error"), &errOut) != 1 {
		t.Fatal("untyped result changed")
	}
}

func TestNoteRunUsageExitCodes(t *testing.T) {
	t.Setenv("TAO_DATA_HOME", t.TempDir())
	for _, args := range [][]string{
		{"note", "run"}, {"n", "r"}, {"note", "run", "one", "two"},
		{"n", "r", "one", "--unknown"}, {"note", "run", "one", "--max-slices=bad"},
		{"note", "run", "one", "--agent=invalid"},
	} {
		var out, errOut bytes.Buffer
		if code := run(context.Background(), args, strings.NewReader(""), &out, &errOut); code != 2 || errOut.Len() == 0 {
			t.Fatalf("%v: exit=%d stderr=%s", args, code, &errOut)
		}
	}
	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"note", "run", "--help"}, 0}, {[]string{"n", "r", "--help"}, 0},
		{[]string{"run", "--unknown"}, 1}, {[]string{"note", "show"}, 1},
	} {
		var out, errOut bytes.Buffer
		if code := run(context.Background(), tc.args, strings.NewReader(""), &out, &errOut); code != tc.code {
			t.Fatalf("%v: exit=%d stderr=%s", tc.args, code, &errOut)
		}
	}
}

func TestRunReturnsExitCodes(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"help"}, strings.NewReader(""), &out, &errOut); code != 0 {
		t.Fatalf("help exit code = %d, stderr=%q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "Usage:") {
		t.Fatalf("expected help output, got %q", out.String())
	}
	out.Reset()
	errOut.Reset()
	if code := run(context.Background(), []string{"does-not-exist"}, strings.NewReader(""), &out, &errOut); code != 1 {
		t.Fatalf("bad command exit code = %d", code)
	}
	if !strings.Contains(errOut.String(), "unknown command") {
		t.Fatalf("expected error output, got %q", errOut.String())
	}
}
