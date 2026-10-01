package runtimeconfig

import "fmt"

// ReworkOptionsPatch is one configuration layer. Nil fields inherit from earlier
// layers; AutoRework is compatibility input only and yields to MaxAttempts.
type ReworkOptionsPatch struct {
	MaxAttempts           *int
	EscalationFromAttempt *int
	AutoRework            *bool
}

// ResolvedReworkOptions is the numeric policy after layering and normalization.
type ResolvedReworkOptions struct {
	MaxAttempts           int
	EscalationFromAttempt int
}

// ResolveReworkOptions applies stages in increasing precedence order and validates
// the consumed values before disabling attempts for review-disabled or reverify
// runs. Presentation warnings belong to callers.
func ResolveReworkOptions(reviewEnabled, reverify bool, stages ...ReworkOptionsPatch) (ResolvedReworkOptions, error) {
	options := ResolvedReworkOptions{
		MaxAttempts:           DefaultMaxReworkAttempts,
		EscalationFromAttempt: DefaultReworkEscalationFromAttempt,
	}
	for _, stage := range stages {
		if stage.MaxAttempts != nil {
			options.MaxAttempts = *stage.MaxAttempts
		} else if stage.AutoRework != nil {
			options.MaxAttempts = 0
			if *stage.AutoRework {
				options.MaxAttempts = DefaultMaxReworkAttempts
			}
		}
		if stage.EscalationFromAttempt != nil {
			options.EscalationFromAttempt = *stage.EscalationFromAttempt
		}
	}
	if options.MaxAttempts < 0 {
		return ResolvedReworkOptions{}, fmt.Errorf("--max-rework-attempts must be 0 or greater")
	}
	if options.EscalationFromAttempt < 1 {
		return ResolvedReworkOptions{}, fmt.Errorf("--rework-escalation-from-attempt must be 1 or greater")
	}
	if !reviewEnabled || reverify {
		options.MaxAttempts = 0
	}
	return options, nil
}
