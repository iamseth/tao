package run

import (
	"strings"
	"testing"
)

func TestRenderPullRequestBodyPromptRequiresNativeStructureAndNoTaoNoise(t *testing.T) {
	prompt := renderPullRequestBodyPrompt(pullRequestBodyPromptData{
		PlanDir:    "/plans/plan-a",
		PlanID:     "plan-a",
		Title:      "feat(pr): create native pull requests",
		Branch:     "feature/native-pr-format",
		BaseBranch: "main",
		HeadSHA:    "head123",
		DraftBody:  "## Problem\n\nContext.\n\n## Fix\n\nChange.\n\n## Tests\n\n- `go test ./...`: passed\n\n## Deploy\n\nNo special deployment steps are required.\n\n## Scope\n\n<details>\n<summary>Changed files</summary>\n\n```text\ncmd/tao/main.go | 1 +\n```\n\n</details>\n",
	})

	if strings.Contains(prompt, "plan-a") || strings.Contains(prompt, "/plans/") || strings.Contains(prompt, "head123") {
		t.Fatalf("pull request body prompt exposed lifecycle metadata:\n%s", prompt)
	}
	for _, want := range []string{
		"Keep exactly these level-two headings in this order: Problem, Fix, Tests, Deploy, Scope",
		"Use only ## ATX syntax for level-two headings; do not add Setext headings",
		"Keep Tests exactly as drafted, including legitimate repository paths that contain the word Tao",
		"draft omits Tao lifecycle verification commands, so do not reintroduce them",
		"Keep Scope exactly as drafted: a complete collapsed Changed files details block containing the exact diff stat",
		"including paths that happen to contain the word Tao",
		"Do not include plan IDs, slice or lifecycle details, merge guidance, or Tao-specific prose in Problem, Fix, Tests, or Deploy",
		"Do not add claims",
		"## Problem",
		"<summary>Changed files</summary>",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("pull request body prompt missing %q:\n%s", want, prompt)
		}
	}
}
