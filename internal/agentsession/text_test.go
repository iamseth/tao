package agentsession

import (
	"context"
	"errors"
	"testing"

	"github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/promptcapture"
)

func TestTextGeneratorWithCapture(t *testing.T) {
	target := promptcapture.Target{Dir: t.TempDir(), Role: "text", Template: "test"}
	var results []Result
	original := NewTextGenerator(Config{Runtime: runtimeFunc(func(context.Context, agent.Session) (agent.SessionResult, error) {
		return agent.SessionResult{FinalText: "done"}, nil
	})}, func(result Result, _ error) { results = append(results, result) })
	captured := original.WithCapture(target)
	for _, g := range []TextGenerator{captured, original} {
		if text, err := g.GenerateText(context.Background(), "", "prompt"); text != "done" || err != nil {
			t.Fatalf("text=%q err=%v", text, err)
		}
	}
	entries, err := promptcapture.List(target.Dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries=%v err=%v", entries, err)
	}
	if len(results) != 2 || results[0].PromptPath != entries[0].Path || results[1].PromptPath != "" || results[1].PromptTemplate != "" {
		t.Fatalf("results=%+v", results)
	}
}

func TestTextGenerator(t *testing.T) {
	providerErr := errors.New("provider failed")
	for _, tt := range []struct {
		name      string
		finalText string
		output    string
		err       error
		want      string
	}{
		{name: "final text wins", finalText: " \nfinal\t", output: " raw ", want: "final"},
		{name: "blank final text falls back", finalText: " \n\t", output: " \nraw\t", want: "raw"},
		{name: "blank output", finalText: " ", output: "\n"},
		{name: "error discards text after observation", finalText: "final", output: "partial", err: providerErr},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls, observations := 0, 0
			ctx := context.Background()
			metrics := &agent.Metrics{OutputTokens: 7}
			generator := NewTextGenerator(Config{
				Descriptor: agent.Descriptor{Label: "test"},
				Runtime: runtimeFunc(func(gotCtx context.Context, session agent.Session) (agent.SessionResult, error) {
					calls++
					if gotCtx != ctx || session.RepoRoot != "/repo" || session.Prompt != "prompt" || !session.CollectMetrics {
						t.Fatalf("unexpected session: %+v", session)
					}
					return agent.SessionResult{FinalText: tt.finalText, Output: tt.output, Metrics: metrics}, tt.err
				}),
			}, func(result Result, err error) {
				observations++
				if calls != 1 || !result.Invoked || result.FinalText != tt.finalText || result.Output != tt.output || result.Metrics != metrics || result.AgentLabel != "test" || err != tt.err { //nolint:errorlint // The adapter must pass the original error unchanged.
					t.Fatalf("unexpected observation: %+v, %v", result, err)
				}
			})
			text, err := generator.GenerateText(ctx, "/repo", "prompt")
			if text != tt.want || err != tt.err { //nolint:errorlint // Error identity is part of the adapter contract.
				t.Fatalf("GenerateText = %q, %v; want %q, %v", text, err, tt.want, tt.err)
			}
			if calls != 1 || observations != 1 {
				t.Fatalf("calls=%d observations=%d; want one each", calls, observations)
			}
		})
	}
}

func TestTextGeneratorWithoutObserver(t *testing.T) {
	generator := NewTextGenerator(Config{Runtime: runtimeFunc(func(context.Context, agent.Session) (agent.SessionResult, error) {
		return agent.SessionResult{FinalText: "done"}, nil
	})}, nil)
	if text, err := generator.GenerateText(context.Background(), "/repo", "prompt"); text != "done" || err != nil {
		t.Fatalf("GenerateText = %q, %v", text, err)
	}
}

func TestTextGeneratorDoesNotObserveUninvokedSession(t *testing.T) {
	guardErr := errors.New("pre-session guard failed")
	// Text requests currently have no pre-session guard. Inject the runner
	// outcome to exercise the Invoked contract independently of guard policy.
	generator := TextGenerator{
		run: func(context.Context, Request) (Result, error) {
			return Result{}, guardErr
		},
		observe: func(Result, error) {
			t.Fatal("observed a session that was not invoked")
		},
	}
	if text, err := generator.GenerateText(context.Background(), "/repo", "prompt"); text != "" || err != guardErr { //nolint:errorlint // Error identity is part of the adapter contract.
		t.Fatalf("GenerateText = %q, %v; want empty text and original error", text, err)
	}
}
