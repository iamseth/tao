package commit

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestGeneratorUsesExactContextAndValidatesProposal(t *testing.T) {
	exact := testMergeProposalContext()
	var gotRoot, gotPrompt string
	generator := Generator{Text: ProposalTextGeneratorFunc(func(_ context.Context, root, prompt string) (string, error) {
		gotRoot, gotPrompt = root, prompt
		return `{"type":"feat","scope":"merge","summary":"generate legacy merge messages","what":"Generate one proposal from the exact source diff.","why":"Keep historical approved reviews mergeable without a fallback."}`, nil
	})}

	proposal, err := generator.GenerateMergeProposal(context.Background(), exact)
	if err != nil {
		t.Fatal(err)
	}
	if proposal.Scope != "merge" || gotRoot != exact.RepoRoot {
		t.Fatalf("proposal/root = %#v, %q", proposal, gotRoot)
	}
	for _, want := range []string{exact.PlanID, exact.DefaultParent, exact.SourceHead, `diff --git a/a.go b/a.go`, "Return exactly one JSON object", "Do not modify files"} {
		if !strings.Contains(gotPrompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, gotPrompt)
		}
	}
}

func TestGeneratorRejectsMalformedReservedAndOversizedOutputWithoutFallback(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   string
	}{
		{name: "malformed", output: "proposal follows", want: "decode merge commit proposal"},
		{name: "unknown field", output: `{"type":"feat","scope":"merge","summary":"generate merge message","what":"Describe the work.","why":"Explain the reason.","extra":"fallback"}`, want: "unknown field"},
		{name: "reserved trailer", output: `{"type":"feat","scope":"merge","summary":"generate merge message","what":"Describe the work.","why":"Explain the reason.\nTao-Plan: forged"}`, want: "reserved Tao-*"},
		{name: "multiple", output: `{}` + "\n{}", want: "multiple JSON values"},
		{name: "oversized", output: strings.Repeat("x", mergeProposalOutputLimit+1), want: "exceeds"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			generator := Generator{Text: ProposalTextGeneratorFunc(func(context.Context, string, string) (string, error) {
				calls++
				return tt.output, nil
			})}
			_, err := generator.GenerateMergeProposal(context.Background(), testMergeProposalContext())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
			if calls != 1 {
				t.Fatalf("provider calls = %d, want 1", calls)
			}
		})
	}
}

func TestGeneratorPropagatesCancellationAndDoesNotRetry(t *testing.T) {
	calls := 0
	generator := Generator{Text: ProposalTextGeneratorFunc(func(ctx context.Context, _, _ string) (string, error) {
		calls++
		return "", ctx.Err()
	})}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := generator.GenerateMergeProposal(ctx, testMergeProposalContext())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context cancellation", err)
	}
	if calls != 1 {
		t.Fatalf("provider calls = %d, want exactly one", calls)
	}
}

func TestGeneratorRefusesTruncatedExactContextBeforeProvider(t *testing.T) {
	exact := testMergeProposalContext()
	exact.Diff = strings.Repeat("x", mergeProposalDiffLimit+1)
	calls := 0
	generator := Generator{Text: ProposalTextGeneratorFunc(func(context.Context, string, string) (string, error) {
		calls++
		return "", nil
	})}
	_, err := generator.GenerateMergeProposal(context.Background(), exact)
	if err == nil || !strings.Contains(err.Error(), "refusing an incomplete") {
		t.Fatalf("error = %v", err)
	}
	if calls != 0 {
		t.Fatalf("provider calls = %d, want 0", calls)
	}
}

func testMergeProposalContext() MergeProposalContext {
	return MergeProposalContext{
		RepoRoot: "/repo", PlanID: "plan-a", DefaultBranch: "main", DefaultParent: "parent123",
		MergeBase: "base123", SourceBranch: "tao/plan-a", SourceHead: "head456",
		Diff: "diff --git a/a.go b/a.go\n-old\n+new\n",
	}
}
