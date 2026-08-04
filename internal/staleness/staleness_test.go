package staleness

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/commandrunner"
	"github.com/iamseth/tao/internal/plan"
)

func fakeGitRunner(outputs map[string]string, failures map[string]error) commandrunner.Runner {
	return func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		if len(args) >= 2 && args[0] == "-C" {
			args = args[2:]
		}
		key := strings.Join(args, " ")
		if err := failures[key]; err != nil {
			return err
		}
		if out, ok := outputs[key]; ok {
			_, _ = io.WriteString(stdout, out)
		}
		return nil
	}
}

func findingMessages(findings []Finding) string {
	messages := make([]string, 0, len(findings))
	for _, finding := range findings {
		messages = append(messages, finding.Severity+": "+finding.Message)
	}
	return strings.Join(messages, "\n")
}

func TestFindingsWarnsOnMissingMetadata(t *testing.T) {
	noRoot := &plan.PlanDetail{State: plan.State{Plan: plan.PlanState{ID: "plan-a"}}}
	findings := Findings(context.Background(), noRoot, fakeGitRunner(nil, nil))
	if len(findings) != 1 || !strings.Contains(findings[0].Message, "no recorded repository root") {
		t.Fatalf("missing-root findings = %q", findingMessages(findings))
	}

	noBase := &plan.PlanDetail{State: plan.State{Repo: plan.Repo{Root: "/repo"}, Plan: plan.PlanState{ID: "plan-a"}}}
	findings = Findings(context.Background(), noBase, fakeGitRunner(nil, nil))
	if len(findings) != 1 || !strings.Contains(findings[0].Message, "no recorded repo.base_commit") {
		t.Fatalf("missing-base findings = %q", findingMessages(findings))
	}
}

func TestFindingsCleanWhenHeadMatchesBase(t *testing.T) {
	detail := &plan.PlanDetail{State: plan.State{
		Repo: plan.Repo{Root: "/repo", BaseCommit: "aaaaaaaaaaaa1111"},
		Plan: plan.PlanState{ID: "plan-a"},
	}}
	findings := Findings(context.Background(), detail, fakeGitRunner(map[string]string{
		"rev-parse HEAD": "aaaaaaaaaaaa1111\n",
	}, nil))
	if len(findings) != 0 {
		t.Fatalf("expected no findings at base, got %q", findingMessages(findings))
	}
}

func TestFindingsReportsDriftAndPendingSliceOverlap(t *testing.T) {
	detail := &plan.PlanDetail{
		State: plan.State{
			Repo: plan.Repo{Root: "/repo", BaseCommit: "aaaaaaaaaaaa1111"},
			Plan: plan.PlanState{ID: "plan-a", PendingSlices: []string{"001-a", "002-b"}},
		},
		Slices: plan.SlicesFile{Slices: []plan.Slice{
			{ID: "001-a", Status: plan.StatusPending, ExpectedFiles: []string{"internal/run/run.go", "README.md"}},
			{ID: "002-b", Status: plan.StatusPending, ExpectedFiles: []string{"docs"}},
			{ID: "003-c", Status: plan.StatusCompleted, ExpectedFiles: []string{"internal/run/run.go"}},
		}},
	}
	findings := Findings(context.Background(), detail, fakeGitRunner(map[string]string{
		"rev-parse HEAD": "bbbbbbbbbbbb2222\n",
		"merge-base --is-ancestor aaaaaaaaaaaa1111 HEAD": "",
		"diff --name-only aaaaaaaaaaaa1111..HEAD":        "internal/run/run.go\ndocs/plan-format.md\n",
	}, nil))
	text := findingMessages(findings)
	for _, want := range []string{
		"repository HEAD changed since planning: aaaaaaaaaaaa -> bbbbbbbbbbbb",
		"2 file(s) changed",
		"pending slice 001-a expects file(s) changed since planning: internal/run/run.go",
		"pending slice 002-b expects file(s) changed since planning: docs",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("findings missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "003-c") {
		t.Fatalf("completed slice should not overlap:\n%s", text)
	}
}

func TestFindingsWarnsWhenBaseNotAncestorOrGitFails(t *testing.T) {
	detail := &plan.PlanDetail{State: plan.State{
		Repo: plan.Repo{Root: "/repo", BaseCommit: "aaaaaaaaaaaa1111"},
		Plan: plan.PlanState{ID: "plan-a"},
	}}
	findings := Findings(context.Background(), detail, fakeGitRunner(map[string]string{
		"rev-parse HEAD": "bbbbbbbbbbbb2222\n",
		"diff --name-only aaaaaaaaaaaa1111..HEAD": "",
	}, map[string]error{
		"merge-base --is-ancestor aaaaaaaaaaaa1111 HEAD": errors.New("exit status 1"),
	}))
	if text := findingMessages(findings); !strings.Contains(text, "not an ancestor of current HEAD") {
		t.Fatalf("expected non-ancestor warning:\n%s", text)
	}

	findings = Findings(context.Background(), detail, fakeGitRunner(nil, map[string]error{
		"rev-parse HEAD": errors.New("not a repository"),
	}))
	if text := findingMessages(findings); !strings.Contains(text, "could not read current HEAD") {
		t.Fatalf("expected HEAD warning:\n%s", text)
	}
}
