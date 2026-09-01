package prompts

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"
)

const (
	PromptRun                         = "run"
	PromptPlan                        = "plan"
	PromptSlice                       = "slice"
	PromptNoteSlice                   = "note-slice"
	PromptNote                        = "note"
	PromptCommit                      = "commit"
	PromptGrillMe                     = "grill-me"
	PromptImproveCodebaseArchitecture = "improve-codebase-architecture"
	PromptImproveDocumentation        = "improve-documentation"
	PromptRepoHealth                  = "repo-health"
	PromptTaoInsightsReview           = "insights-review"
	PromptPR                          = "pr"
	PromptReview                      = "review"
)

//go:embed run.md
var RunPromptTemplate string

//go:embed plan.md
var PlanPromptTemplate string

//go:embed slice.md
var SlicePromptTemplate string

//go:embed note_slice.md
var NoteSlicePromptTemplate string

//go:embed note.md
var NotePromptTemplate string

//go:embed commit.md
var CommitPromptTemplate string

//go:embed grill-me.md
var GrillMePromptTemplate string

//go:embed improve-codebase-architecture.md
var ImproveCodebaseArchitecturePromptTemplate string

//go:embed improve-documentation.md
var ImproveDocumentationPromptTemplate string

//go:embed repo-health.md
var RepoHealthPromptTemplate string

//go:embed tao-insights-review.md
var TaoInsightsReviewPromptTemplate string

//go:embed pr.md
var PRPromptTemplate string

//go:embed review.md
var ReviewPromptTemplate string

//go:embed rework_triage.md
var ReworkTriagePromptTemplate string

//go:embed merge_resolve.md
var MergeResolvePromptTemplate string

//go:embed merge_review.md
var MergeReviewPromptTemplate string

const mergeResolveFieldLimit = 8 * 1024

// MergeResolveData is the internal-only packet for a batch conflict or
// verification repair. It is deliberately not part of the installable prompt
// registry.
type MergeResolveData struct {
	BatchID, PlanID, SourceHead, IntegrationBase, VerifyCommand                  string
	PlanBrief, SourceReview, Diff, ConflictFiles, PriorPlans, VerificationOutput string
}

// MergeReviewData is the internal-only aggregate review packet.
type MergeReviewData struct {
	BatchID, DefaultStart, IntegrationHead, VerifyCommand string
	Candidates, ResolutionCommits, DiffStat, Verification string
}

// ReworkTriageData is the internal-only prompt input for pull-request thread
// classification. ThreadPackets must come from RenderPRThreadPackets.
type ReworkTriageData struct {
	ThreadCount   int
	ThreadPackets string
}

type Data struct {
	PlanDir            string
	PlanID             string
	Base               string
	Head               string
	ChangeType         string
	ProposalOnly       bool
	RunPacket          string
	Resuming           bool
	ResumeAttempt      int
	CommitEnable       bool
	CommitPolicy       string
	ExecutionMode      string
	Arguments          string
	SessionID          string
	Title              string
	RepoID             string
	RepoName           string
	RepoRoot           string
	RepoBranch         string
	Transcript         string
	UnsupervisedPolicy bool
}

type promptDefinition struct {
	name     string
	template string
	render   func(string, Data) (string, error)
}

type Definition struct {
	Name        string
	CommandName string
	Template    string
}

// agentCommandNames keeps the stable Tao prompt selectors separate from the
// slash-command names installed into provider-owned namespaces.
var agentCommandNames = map[string]string{
	PromptPlan:                        "tao-plan",
	PromptSlice:                       "tao-slice",
	PromptNoteSlice:                   "tao-note-slice",
	PromptNote:                        "tao-note",
	PromptRun:                         "tao-run",
	PromptCommit:                      "tao-commit",
	PromptGrillMe:                     "tao-grill-me",
	PromptImproveCodebaseArchitecture: "tao-improve-codebase-architecture",
	PromptImproveDocumentation:        "tao-improve-documentation",
	PromptRepoHealth:                  "tao-repo-health",
	PromptTaoInsightsReview:           "tao-insights-review",
	PromptPR:                          "tao-pr",
	PromptReview:                      "tao-review",
}

var promptRegistry = []promptDefinition{
	newTemplatedPrompt(PromptPlan, PlanPromptTemplate),
	newTemplatedPrompt(PromptSlice, SlicePromptTemplate),
	newPrompt(PromptNoteSlice, NoteSlicePromptTemplate, renderNoteSlicePrompt),
	newTemplatedPrompt(PromptNote, NotePromptTemplate),
	newPrompt(PromptRun, RunPromptTemplate, renderRunPrompt),
	newTemplatedPrompt(PromptCommit, CommitPromptTemplate),
	newTemplatedPrompt(PromptGrillMe, GrillMePromptTemplate),
	newTemplatedPrompt(PromptImproveCodebaseArchitecture, ImproveCodebaseArchitecturePromptTemplate),
	newTemplatedPrompt(PromptImproveDocumentation, ImproveDocumentationPromptTemplate),
	newTemplatedPrompt(PromptRepoHealth, RepoHealthPromptTemplate),
	newTemplatedPrompt(PromptTaoInsightsReview, TaoInsightsReviewPromptTemplate),
	newTemplatedPrompt(PromptPR, PRPromptTemplate),
	newPrompt(PromptReview, ReviewPromptTemplate, renderReviewPrompt),
}

func newTemplatedPrompt(name string, template string) promptDefinition {
	return newPrompt(name, template, renderPromptTemplate)
}

func newPrompt(name string, template string, render func(string, Data) (string, error)) promptDefinition {
	return promptDefinition{name: name, template: template, render: render}
}

func Render(name string, data Data) (string, error) {
	prompt, ok := promptByName(name)
	if !ok {
		return "", fmt.Errorf("unknown prompt %q", name)
	}
	return prompt.render(prompt.template, data)
}

func renderTemplate(source string, data Data) (string, error) {
	tmpl, err := template.New("prompt").Parse(source)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	if err := tmpl.Execute(&out, data); err != nil {
		return "", err
	}
	return out.String(), nil
}

func renderNoteSlicePrompt(source string, data Data) (string, error) {
	if data.UnsupervisedPolicy {
		lines := strings.Split(strings.ReplaceAll(data.Transcript, "\r\n", "\n"), "\n")
		for i, line := range lines {
			encoded, err := json.Marshal(line)
			if err != nil {
				return "", fmt.Errorf("encode untrusted transcript line: %w", err)
			}
			lines[i] = string(encoded)
		}
		data.Transcript = strings.Join(lines, "\n")
	}
	return renderTemplate(source, data)
}

func renderRunPrompt(source string, data Data) (string, error) {
	if data.PlanDir == "" {
		data.PlanDir = "<plan-directory>"
	}
	if data.CommitPolicy == "" {
		if data.CommitEnable {
			data.CommitPolicy = "slice"
		} else {
			data.CommitPolicy = "none"
		}
	}
	if data.ExecutionMode == "" {
		data.ExecutionMode = "isolated"
	}
	return renderTemplate(source, data)
}

func renderReviewPrompt(source string, data Data) (string, error) {
	if data.PlanDir == "" {
		data.PlanDir = "<plan-directory>"
	}
	if data.PlanID == "" {
		data.PlanID = "<plan-id>"
	}
	if data.Base == "" {
		data.Base = "<base>"
	}
	if data.Head == "" {
		data.Head = "<head>"
	}
	return renderTemplate(source, data)
}

func renderPromptTemplate(source string, data Data) (string, error) {
	return renderTemplate(source, data)
}

// RenderPRThreadPackets renders pull-request thread prose as size-bounded,
// JSON-encoded packets. Encoding keeps thread text from manufacturing packet
// delimiters; callers are responsible for composing one prose value per thread.
func RenderPRThreadPackets(threadProse []string) (string, error) {
	const packetName = "PULL REQUEST THREAD"

	var packets strings.Builder
	for _, prose := range threadProse {
		if len(prose) > mergeResolveFieldLimit {
			prose = prose[:mergeResolveFieldLimit] + "\n[TRUNCATED BY TAO]"
		}
		encoded, err := json.Marshal(prose)
		if err != nil {
			return "", fmt.Errorf("encode untrusted pull request thread packet: %w", err)
		}
		fmt.Fprintf(&packets, "BEGIN TAO UNTRUSTED %s\n%s\nEND TAO UNTRUSTED %s\n\n", packetName, encoded, packetName)
	}
	return packets.String(), nil
}

// RenderReworkTriage renders the internal pull-request thread classification
// prompt. It is deliberately not part of the installable prompt registry.
func RenderReworkTriage(data ReworkTriageData) (string, error) {
	tmpl, err := template.New("rework-triage").Parse(ReworkTriagePromptTemplate)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	if err := tmpl.Execute(&out, data); err != nil {
		return "", err
	}
	return out.String(), nil
}

// RenderMergeResolve renders a size-bounded internal prompt. JSON string
// encoding prevents untrusted content from manufacturing packet delimiters.
func RenderMergeResolve(data MergeResolveData) (string, error) {
	packets := []struct{ name, value string }{
		{"PLAN BRIEF", data.PlanBrief}, {"SOURCE REVIEW", data.SourceReview},
		{"DIFF", data.Diff}, {"CONFLICT FILES", data.ConflictFiles},
		{"PRIOR INTEGRATED PLANS", data.PriorPlans}, {"VERIFICATION OUTPUT", data.VerificationOutput},
	}
	var body strings.Builder
	for _, packet := range packets {
		value := packet.value
		if len(value) > mergeResolveFieldLimit {
			value = value[:mergeResolveFieldLimit] + "\n[TRUNCATED BY TAO]"
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return "", fmt.Errorf("encode untrusted %s packet: %w", strings.ToLower(packet.name), err)
		}
		fmt.Fprintf(&body, "BEGIN TAO UNTRUSTED %s\n%s\nEND TAO UNTRUSTED %s\n\n", packet.name, encoded, packet.name)
	}
	input := struct {
		BatchID, PlanID, SourceHead, IntegrationBase, VerifyCommand, Packets string
	}{data.BatchID, data.PlanID, data.SourceHead, data.IntegrationBase, data.VerifyCommand, body.String()}
	tmpl, err := template.New("merge-resolve").Parse(MergeResolvePromptTemplate)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	if err := tmpl.Execute(&out, input); err != nil {
		return "", err
	}
	return out.String(), nil
}

// RenderMergeReview renders size-bounded, JSON-encoded aggregate evidence.
func RenderMergeReview(data MergeReviewData) (string, error) {
	packets := []struct{ name, value string }{
		{"CANDIDATES AND SOURCE REVIEWS", data.Candidates},
		{"RESOLUTION COMMITS", data.ResolutionCommits},
		{"FINAL DIFF STAT", data.DiffStat},
		{"VERIFICATION EVIDENCE", data.Verification},
	}
	var body strings.Builder
	for _, packet := range packets {
		value := packet.value
		if len(value) > mergeResolveFieldLimit {
			value = value[:mergeResolveFieldLimit] + "\n[TRUNCATED BY TAO]"
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return "", fmt.Errorf("encode untrusted %s packet: %w", strings.ToLower(packet.name), err)
		}
		fmt.Fprintf(&body, "BEGIN TAO UNTRUSTED %s\n%s\nEND TAO UNTRUSTED %s\n\n", packet.name, encoded, packet.name)
	}
	input := struct {
		BatchID, DefaultStart, IntegrationHead, VerifyCommand, Packets string
	}{data.BatchID, data.DefaultStart, data.IntegrationHead, data.VerifyCommand, body.String()}
	tmpl, err := template.New("merge-review").Parse(MergeReviewPromptTemplate)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	if err := tmpl.Execute(&out, input); err != nil {
		return "", err
	}
	return out.String(), nil
}

func PromptNames() []string {
	names := make([]string, 0, len(promptRegistry))
	for _, prompt := range promptRegistry {
		names = append(names, prompt.name)
	}
	return names
}

func Definitions() []Definition {
	definitions := make([]Definition, 0, len(promptRegistry))
	for _, prompt := range promptRegistry {
		definitions = append(definitions, Definition{
			Name:        prompt.name,
			CommandName: agentCommandNames[prompt.name],
			Template:    prompt.template,
		})
	}
	return definitions
}

func promptByName(name string) (promptDefinition, bool) {
	for _, prompt := range promptRegistry {
		if prompt.name == name {
			return prompt, true
		}
	}
	return promptDefinition{}, false
}
