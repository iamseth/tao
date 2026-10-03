package agentsession

import (
	"context"

	"github.com/iamseth/tao/internal/promptcapture"
)

// TextGenerator adapts a bounded session to provider-neutral text generation.
type TextGenerator struct {
	capture *promptcapture.Target
	run     func(context.Context, Request) (Result, error)
	observe func(Result, error)
}

// NewTextGenerator constructs a text adapter with optional session observation.
func NewTextGenerator(config Config, observe func(Result, error)) TextGenerator {
	return TextGenerator{run: New(config).Run, observe: observe}
}

// WithCapture returns a copy that captures each generated prompt at target.
func (g TextGenerator) WithCapture(target promptcapture.Target) TextGenerator {
	g.capture = &target
	return g
}

// GenerateText runs one session, preferring final text over raw output.
func (g TextGenerator) GenerateText(ctx context.Context, repoRoot, prompt string) (string, error) {
	result, err := g.run(ctx, Request{RepoRoot: repoRoot, Prompt: prompt, CollectMetrics: true, Capture: g.capture})
	// Observe actual sessions before provider errors or downstream validation
	// can discard their measurements.
	if result.Invoked && g.observe != nil {
		g.observe(result, err)
	}
	if err != nil {
		return "", err
	}
	return ResultText(result), nil
}
