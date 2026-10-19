package workspace

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/plan"
)

func TestExecutionPreparerSetup(t *testing.T) {
	for _, tc := range []struct {
		name, command              string
		reused, fail, skip, jsFail bool
	}{
		{name: "create", command: `echo "$TAO_PLAN_ID"`},
		{name: "reuse", command: `echo "$TAO_PLAN_ID"`, reused: true},
		{name: "failure", command: "fail", fail: true},
		{name: "reused failure", command: "fail", reused: true, fail: true, skip: true},
		{name: "clear prior failure", command: "ok", reused: true, skip: true},
		{name: "preserve tolerated JS failure", command: "ok", reused: true, jsFail: true},
		{name: "empty"}, {name: "blank", command: " \t "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			detail := executionPreparerPlanDetail(t.TempDir())
			detail.State.Workspace = &plan.Workspace{DependencyFingerprint: "prior", DependencyFailure: "old failure"}
			if tc.skip || tc.jsFail {
				if err := os.WriteFile(filepath.Join(root, "package-lock.json"), []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
				if tc.skip {
					fp, err := dependencyLockfileFingerprint(root)
					if err != nil {
						t.Fatal(err)
					}
					detail.State.Workspace.DependencyFingerprint = fp
				}
			}
			calls := 0
			p := ExecutionPreparer{SetupCommand: tc.command, PlanRecordFactory: memoryWorkspacePlanRecordFactory, managerFactory: executionPreparerManagerFactory(Metadata{Path: root, Branch: "tao/plan-a", Created: !tc.reused, Reused: tc.reused}, nil), Runner: func(_ context.Context, cwd, command string, args []string, _, stderr io.Writer) error {
				if command == "npm" && tc.jsFail {
					_, _ = io.WriteString(stderr, "JS failure")
					return errors.New("install")
				}
				if command != "env" {
					t.Fatalf("unexpected command %s", command)
				}
				calls++
				want := []string{"TAO_PLAN_ID=plan-a", "TAO_WORKSPACE_PATH=" + root, "TAO_REPO_ROOT=" + detail.State.Repo.Root, "sh", "-c", tc.command}
				if cwd != root || !reflect.DeepEqual(args, want) {
					t.Fatalf("cwd=%q args=%q", cwd, args)
				}
				if tc.fail {
					_, _ = io.WriteString(stderr, strings.Repeat("x", 5000))
					return errors.New("exit")
				}
				return nil
			}}
			_, err := p.Prepare(context.Background(), detail, ExecutionPrepareOptions{})
			w := detail.State.Workspace
			if tc.fail {
				if err == nil || err.Error() != "workspace setup command failed: "+strings.Repeat("x", 4096) {
					t.Fatalf("error=%v", err)
				}
				if w.LifecycleStatus != plan.WorkspaceStatusFailed || w.DependencyCommand != tc.command || len(w.DependencyFailure) != 4096 || w.DependencyStartedAt == nil || w.DependencyCompletedAt == nil {
					t.Fatalf("failure=%+v", w)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if w.LifecycleStatus != plan.WorkspaceStatusReady {
					t.Fatalf("workspace=%+v", w)
				}
				if strings.TrimSpace(tc.command) != "" && !tc.jsFail && w.DependencyFailure != "" {
					t.Fatalf("stale failure: %q", w.DependencyFailure)
				}
				if tc.jsFail && w.DependencyFailure != "JS failure" {
					t.Fatalf("lost JS failure: %+v", w)
				}
			}
			wantCalls := 1
			if strings.TrimSpace(tc.command) == "" {
				wantCalls = 0
			}
			if calls != wantCalls {
				t.Fatalf("calls=%d", calls)
			}
		})
	}
}

func TestWorkspaceSetupRealShell(t *testing.T) {
	root := t.TempDir()
	command := `printf '%s\n' "$PWD" "$TAO_PLAN_ID" "$TAO_WORKSPACE_PATH" "$TAO_REPO_ROOT" > inputs`
	_, err := runWorkspaceSetup(context.Background(), root, "plan-a", "/recorded/repo", command, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, "inputs")) //nolint:gosec // fixed test output under t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	want := realRoot + "\nplan-a\n" + root + "\n/recorded/repo\n"
	if string(got) != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestWorkspaceSetupFailureFallback(t *testing.T) {
	metadata, err := runWorkspaceSetup(context.Background(), t.TempDir(), "p", "r", "exit", func(context.Context, string, string, []string, io.Writer, io.Writer) error {
		return errors.New("fallback")
	}, nil)
	if err == nil || metadata.Command != "exit" || metadata.FailureReason != "fallback" {
		t.Fatalf("metadata=%+v error=%v", metadata, err)
	}
}
