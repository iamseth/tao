package commit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	mergeProposalDiffLimit   = 1024 * 1024
	mergeProposalOutputLimit = 32 * 1024
)

// MergeProposalContext is the exact, read-only change context for an
// exceptional single-plan squash message. Current reviews carry their own
// proposal and do not use this path.
type MergeProposalContext struct {
	RepoRoot      string
	PlanID        string
	DefaultBranch string
	DefaultParent string
	MergeBase     string
	SourceBranch  string
	SourceHead    string
	Diff          string
}

// ProposalTextGenerator is the provider-neutral text-session boundary used by
// Generator. Implementations run exactly one bounded session; Generator owns
// prompt shaping, output bounds, strict decoding, and central validation.
type ProposalTextGenerator interface {
	GenerateText(context.Context, string, string) (string, error)
}

// ProposalTextGeneratorFunc adapts a function to ProposalTextGenerator.
type ProposalTextGeneratorFunc func(context.Context, string, string) (string, error)

func (f ProposalTextGeneratorFunc) GenerateText(ctx context.Context, repoRoot, prompt string) (string, error) {
	return f(ctx, repoRoot, prompt)
}

// MergeProposalGenerator is the exceptional merge-message generation contract.
type MergeProposalGenerator interface {
	GenerateMergeProposal(context.Context, MergeProposalContext) (Proposal, error)
}

// Generator validates one provider-neutral proposal session. It deliberately
// has no retry or fallback behavior.
type Generator struct {
	Text ProposalTextGenerator
}

func (g Generator) GenerateMergeProposal(ctx context.Context, exact MergeProposalContext) (Proposal, error) {
	if g.Text == nil {
		return Proposal{}, errors.New("merge commit proposal generator is not configured")
	}
	if err := validateMergeProposalContext(exact); err != nil {
		return Proposal{}, err
	}
	prompt, err := renderMergeProposalPrompt(exact)
	if err != nil {
		return Proposal{}, err
	}
	output, err := g.Text.GenerateText(ctx, exact.RepoRoot, prompt)
	if err != nil {
		return Proposal{}, fmt.Errorf("generate merge commit proposal: %w", err)
	}
	proposal, err := decodeGeneratedProposal(output)
	if err != nil {
		return Proposal{}, err
	}
	return proposal, nil
}

func validateMergeProposalContext(exact MergeProposalContext) error {
	for label, value := range map[string]string{
		"repo root": exact.RepoRoot, "plan id": exact.PlanID,
		"default branch": exact.DefaultBranch, "default parent": exact.DefaultParent,
		"merge base": exact.MergeBase, "source branch": exact.SourceBranch, "source head": exact.SourceHead,
	} {
		if value == "" || value != strings.TrimSpace(value) || strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("merge commit proposal context requires a valid %s", label)
		}
	}
	if len(exact.Diff) > mergeProposalDiffLimit {
		return fmt.Errorf("exact merge diff exceeds %d bytes; refusing an incomplete commit proposal", mergeProposalDiffLimit)
	}
	return nil
}

func renderMergeProposalPrompt(exact MergeProposalContext) (string, error) {
	payload := struct {
		PlanID        string `json:"plan_id"`
		DefaultBranch string `json:"default_branch"`
		DefaultParent string `json:"default_parent"`
		MergeBase     string `json:"merge_base"`
		SourceBranch  string `json:"source_branch"`
		SourceHead    string `json:"source_head"`
		ExactDiff     string `json:"exact_diff"`
	}{exact.PlanID, exact.DefaultBranch, exact.DefaultParent, exact.MergeBase, exact.SourceBranch, exact.SourceHead, exact.Diff}
	contextJSON, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode exact merge commit context: %w", err)
	}
	return "Generate a commit proposal for the exact single-plan squash context below. Do not modify files, Git refs, the index, or the worktree. Return exactly one JSON object and no markdown or commentary. The object must contain only the string fields type, scope, summary, what, and why. Use a supported scoped Conventional Commit type, a narrow lowercase scope, a lowercase imperative summary of at most 72 characters with no ending punctuation, and useful non-empty what/why text. Do not include verification output or any Tao-* field or trailer.\n\nExact context JSON:\n" + string(contextJSON), nil
}

func decodeGeneratedProposal(output string) (Proposal, error) {
	output = strings.TrimSpace(output)
	if output == "" {
		return Proposal{}, errors.New("merge commit proposal output is empty")
	}
	if len(output) > mergeProposalOutputLimit {
		return Proposal{}, fmt.Errorf("merge commit proposal output exceeds %d bytes", mergeProposalOutputLimit)
	}
	decoder := json.NewDecoder(bytes.NewBufferString(output))
	decoder.DisallowUnknownFields()
	var proposal Proposal
	if err := decoder.Decode(&proposal); err != nil {
		return Proposal{}, fmt.Errorf("decode merge commit proposal: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Proposal{}, errors.New("decode merge commit proposal: multiple JSON values")
		}
		return Proposal{}, fmt.Errorf("decode merge commit proposal: %w", err)
	}
	if err := ValidateProposal(proposal); err != nil {
		return Proposal{}, fmt.Errorf("validate merge commit proposal: %w", err)
	}
	return proposal, nil
}
