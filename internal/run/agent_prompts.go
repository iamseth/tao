package run

import (
	"bytes"
	"fmt"
	"text/template"

	"github.com/iamseth/tao/prompts"
)

type workPromptData struct {
	PlanDir       string
	RunPacket     string
	CommitPolicy  string
	ExecutionMode string
	Resuming      bool
	ResumeAttempt int
}

type pullRequestPromptData struct {
	PlanDir string
	PlanID  string
}

type pullRequestBodyPromptData struct {
	PlanDir    string
	PlanID     string
	Title      string
	Branch     string
	BaseBranch string
	HeadSHA    string
	DraftBody  string
}

func renderWorkPrompt(data workPromptData) (string, error) {
	if data.CommitPolicy == "" {
		data.CommitPolicy = CommitPolicySlice.String()
	}
	if data.CommitPolicy == CommitPolicyPlan.String() {
		return "", fmt.Errorf("commit policy plan was removed; use slice or none")
	}
	if data.ExecutionMode == "" {
		data.ExecutionMode = ExecutionModeIsolated.String()
	}
	tmpl, err := template.New("run.md").Parse(prompts.RunPromptTemplate)
	if err != nil {
		return "", err
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, data); err != nil {
		return "", err
	}
	return out.String(), nil
}

func renderPullRequestPrompt(data pullRequestPromptData) (string, error) {
	arguments := fmt.Sprintf("Create a GitHub pull request for completed Tao plan `%s`. Plan directory: `%s`. Print the created PR URL in the output.", data.PlanID, data.PlanDir)
	return prompts.Render(prompts.PromptPR, prompts.Data{Arguments: arguments})
}

func renderPullRequestBodyPrompt(data pullRequestBodyPromptData) string {
	return fmt.Sprintf(`Draft only the Markdown body for a GitHub pull request.

Do not run commands. Do not push. Do not create or edit any pull request. Return only the Markdown body with no surrounding code fence.

Pull request title: %s

Polish the deterministic draft below for a repository reviewer while preserving every fact. Keep exactly these level-two headings in this order: Problem, Fix, Tests, Deploy, Scope. Use only ## ATX syntax for level-two headings; do not add Setext headings. Keep Tests exactly as drafted, including legitimate repository paths that contain the word Tao; the draft omits Tao lifecycle verification commands, so do not reintroduce them. Keep Scope exactly as drafted: a complete collapsed Changed files details block containing the exact diff stat, including paths that happen to contain the word Tao. Do not include plan IDs, slice or lifecycle details, merge guidance, or Tao-specific prose in Problem, Fix, Tests, or Deploy. Do not add claims about tests, deployment, behavior, or files that are not present in the draft.

%s
`, data.Title, data.DraftBody)
}
