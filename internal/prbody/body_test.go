package prbody

import (
	"fmt"
	"strings"
	"testing"
)

func TestBuildPreservesDeterministicBodyContent(t *testing.T) {
	input := testInput()
	input.DiffStat = " internal/prbody/body.go | 10 +++++-----\n"
	body := Build(input)

	want := "## Problem\n\nKeep  workflow state metadata out of reviewer-facing context.\n\n" +
		"## Fix\n\nCreate repository-native pull requests without the workflow changes details.\n\n" +
		"## Tests\n\n- `go test ./internal/prbody`: passed\n\n" +
		"## Deploy\n\nNo special deployment steps are required.\n\n" +
		"## Scope\n\n<details>\n<summary>Changed files</summary>\n\n" +
		"```text\n internal/prbody/body.go | 10 +++++-----\n```\n\n</details>\n"
	if body != want {
		t.Fatalf("body mismatch\n--- got ---\n%s\n--- want ---\n%s", body, want)
	}
	if err := Validate(body, validation(input, body)); err != nil {
		t.Fatalf("deterministic body rejected: %v", err)
	}
}

func TestBuildUsesFallbacksAndEmptyScope(t *testing.T) {
	body := Build(Input{})
	want := "## Problem\n\nSee the commit description for change context.\n\n" +
		"## Fix\n\nSee the commit description for change context.\n\n" +
		"## Tests\n\nNo automated test results were recorded.\n\n" +
		"## Deploy\n\nNo special deployment steps are required.\n\n" +
		"## Scope\n\n<details>\n<summary>Changed files</summary>\n\n" +
		"No changed-file summary is available.\n\n</details>\n"
	if body != want {
		t.Fatalf("body mismatch\n--- got ---\n%s\n--- want ---\n%s", body, want)
	}
}

func TestBuildEscapesCommitProposalHeadings(t *testing.T) {
	input := testInput()
	input.CommitMessageBody = "What:\nCreate native pull requests.\n\n## Notes\n\nPreserve reviewer context.\n\nRelease notes\n-------------\n\nWhy:\nMake repository changes familiar.\n\n  ## Rationale\n\nKeep the fallback structurally valid."
	body := Build(input)
	if !strings.Contains(body, `\## Notes`) || !strings.Contains(body, `  \## Rationale`) || !strings.Contains(body, "Release notes\n\\-------------") {
		t.Fatalf("deterministic body did not escape proposal headings: %q", body)
	}
	if err := Validate(body, validation(input, body)); err != nil {
		t.Fatalf("deterministic body rejected: %v", err)
	}
}

func TestBuildSanitizesForbiddenReviewerNarrative(t *testing.T) {
	for _, phrase := range []string{"Tao", "slice", "lifecycle", "squash and merge", "merge guidance", "cleanup --dry-run"} {
		t.Run(phrase, func(t *testing.T) {
			input := testInput()
			input.CommitMessageBody = fmt.Sprintf("What:\nDocument %s behavior.\n\nWhy:\nClarify %s behavior for reviewers.", phrase, phrase)
			body := Build(input)
			if err := Validate(body, validation(input, body)); err != nil {
				t.Fatalf("sanitized deterministic body containing %q rejected: %v", phrase, err)
			}
		})
	}
}

func TestBuildAllowsTaoPathsAndOmitsPrefixedTaoCommands(t *testing.T) {
	input := testInput()
	input.VerificationResults = append(input.VerificationResults,
		VerificationResult{Command: "tao validate plan-a", Result: "passed"},
		VerificationResult{Command: "cd subdir && tao validate", Result: "passed"},
		VerificationResult{Command: "env X=1 tao validate", Result: "passed"},
		VerificationResult{Command: "X=1 tao validate", Result: "passed"},
		VerificationResult{Command: "go test ./cmd/tao", Result: "passed"},
	)
	input.DiffStat = " cmd/tao/main.go | 2 +-\n"
	body := Build(input)

	if !strings.Contains(body, input.DiffStat) {
		t.Fatalf("pull request body missing exact diff stat %q: %q", input.DiffStat, body)
	}
	for _, noise := range []string{"tao validate", "cd subdir", "env X=1", "X=1 tao"} {
		if strings.Contains(body, noise) {
			t.Fatalf("pull request body includes lifecycle-only verification command %q: %q", noise, body)
		}
	}
	for _, command := range []string{"go test ./internal/prbody", "go test ./cmd/tao"} {
		if !strings.Contains(body, command) {
			t.Fatalf("pull request body omitted repository test command %q: %q", command, body)
		}
	}
	if err := Validate(body, validation(input, body)); err != nil {
		t.Fatalf("deterministic body rejected: %v", err)
	}
}

func TestIsTaoLifecycleCommandFindsExecutablesOnly(t *testing.T) {
	for _, tt := range []struct {
		command string
		want    bool
	}{
		{command: "tao validate", want: true},
		{command: "/usr/local/bin/tao validate", want: true},
		{command: "cd subdir && tao validate", want: true},
		{command: "prepare || /usr/local/bin/tao validate", want: true},
		{command: "prepare; env X=1 tao validate", want: true},
		{command: "prepare | X=1 ./bin/tao validate", want: true},
		{command: "prepare & tao validate", want: true},
		{command: "prepare\ntao validate", want: true},
		{command: "env X=1 tao validate", want: true},
		{command: "/usr/bin/env -i X=1 ./bin/tao validate", want: true},
		{command: "X=1 tao validate", want: true},
		{command: "go test ./cmd/tao", want: false},
		{command: "go test ./internal/tao/...", want: false},
		{command: "env X=tao go test ./cmd/tao", want: false},
		{command: "echo tao validate", want: false},
		{command: `echo 'ready; tao validate'`, want: false},
		{command: `echo ready\; tao validate`, want: false},
		{command: `go test "./cmd/tao|helper"`, want: false},
		{command: "go test ./cmd/tao && echo done", want: false},
	} {
		t.Run(tt.command, func(t *testing.T) {
			if got := isTaoLifecycleCommand(tt.command); got != tt.want {
				t.Fatalf("isTaoLifecycleCommand(%q) = %t, want %t", tt.command, got, tt.want)
			}
		})
	}
}

func TestValidateRejectsLifecycleNoiseReintroducedInTests(t *testing.T) {
	input := testInput()
	draft := Build(input)
	if err := Validate(draft, validation(input, draft)); err != nil {
		t.Fatalf("legitimate repository path containing tao was rejected: %v", err)
	}

	for _, tt := range []struct {
		name     string
		addition string
		want     string
	}{
		{name: "plan ID", addition: "- Verified plan-a.\n", want: "plan ID"},
		{name: "slice lifecycle", addition: "- Tao slice lifecycle completed.\n", want: "preserve Tests exactly as drafted"},
		{name: "direct Tao command", addition: "- `tao validate`: passed\n", want: "direct Tao lifecycle command"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := strings.Replace(draft, "\n## Deploy", "\n"+tt.addition+"\n## Deploy", 1)
			err := Validate(body, validation(input, draft))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want rejection containing %q", err, tt.want)
			}
		})
	}
}

func TestValidateRequiresTestsToMatchDeterministicDraft(t *testing.T) {
	input := Input{}
	draft := Build(input)
	body := strings.Replace(draft, "No automated test results were recorded.", "- `go test ./...`: passed", 1)
	if err := Validate(body, validation(input, draft)); err == nil || !strings.Contains(err.Error(), "preserve Tests exactly as drafted") {
		t.Fatalf("error = %v, want deterministic Tests mismatch", err)
	}
}

func TestValidateRequiresExactHeadingSequenceAndScope(t *testing.T) {
	input := Input{DiffStat: " internal/prbody/body.go | 1 +\n"}
	valid := Build(input)
	if err := Validate(valid, validation(input, valid)); err != nil {
		t.Fatalf("valid deterministic body rejected: %v", err)
	}
	fencedSetext := strings.Replace(valid, narrativeFallback, "```text\nNotes\n-----\n```", 1)
	if err := Validate(fencedSetext, validation(input, valid)); err != nil {
		t.Fatalf("Setext-like text inside a code fence was treated as a heading: %v", err)
	}

	scopeText := scope(input.DiffStat)
	tests := []struct {
		name string
		body string
	}{
		{
			name: "extra ATX level-two heading",
			body: strings.Replace(valid, "## Scope", "## Notes\n\nReviewer context.\n\n## Scope", 1),
		},
		{
			name: "extra Setext level-two heading",
			body: strings.Replace(valid, "## Scope", "Notes\n-----\n\nReviewer context.\n\n## Scope", 1),
		},
		{
			name: "changed files block outside scope",
			body: strings.Replace(valid, "## Scope\n\n"+scopeText, scopeText+"\n## Scope\n\nChanged files are listed above.\n", 1),
		},
		{
			name: "diff stat outside scope block",
			body: strings.Replace(valid, "## Scope\n\n"+scopeText, "```text\n"+input.DiffStat+"```\n\n## Scope\n\n<details>\n<summary>Changed files</summary>\n\nNo changed-file summary is available.\n\n</details>\n", 1),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := Validate(tt.body, validation(input, valid)); err == nil {
				t.Fatal("expected invalid agent body to be rejected")
			}
		})
	}
}

func testInput() Input {
	return Input{
		PlanID:            "plan-a",
		CommitMessageBody: "What:\nCreate repository-native pull requests without Tao slice details.\n\nWhy:\nKeep plan-a lifecycle metadata out of reviewer-facing context.",
		VerificationResults: []VerificationResult{
			{Command: "go test ./internal/prbody", Result: "passed"},
		},
	}
}

func validation(input Input, draft string) ValidationInput {
	return ValidationInput{PlanID: input.PlanID, DiffStat: input.DiffStat, DeterministicDraft: draft}
}
