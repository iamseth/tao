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

Tao plan: %s
Title: %s
Plan directory: %s
Branch: %s
Base branch: %s
Head SHA: %s

Start from this deterministic draft, preserving its factual content while improving clarity and concision:

%s
`, data.PlanID, data.Title, data.PlanDir, data.Branch, data.BaseBranch, data.HeadSHA, data.DraftBody)
}
