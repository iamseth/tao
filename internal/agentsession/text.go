package agentsession

import (
	"context"
	"strings"
)

// TextGenerator adapts a bounded session to provider-neutral text generation.
type TextGenerator struct {
	run     func(context.Context, Request) (Result, error)
	observe func(Result, error)
}

// NewTextGenerator constructs a text adapter with optional session observation.
func NewTextGenerator(config Config, observe func(Result, error)) TextGenerator {
	return TextGenerator{run: New(config).Run, observe: observe}
}

// GenerateText runs one session, preferring final text over raw output.
func (g TextGenerator) GenerateText(ctx context.Context, repoRoot, prompt string) (string, error) {
	result, err := g.run(ctx, Request{RepoRoot: repoRoot, Prompt: prompt, CollectMetrics: true})
	// Observe actual sessions before provider errors or downstream validation
	// can discard their measurements.
	if result.Invoked && g.observe != nil {
		g.observe(result, err)
	}
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(result.FinalText)
	if text == "" {
		text = strings.TrimSpace(result.Output)
	}
	return text, nil
}
