package plannerroute

import (
	"crypto/sha256"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/iamseth/tao/internal/runtimeconfig"
)

type Policy struct {
	Version string
	Mode    Mode
	Arms    []WeightedArm
	Floor   float64
}

// ParseArms accepts either all weighted selectors or all bare selectors.
// Prompt and permission facts are supplied by the caller before NewPolicy.
func ParseArms(value string) ([]WeightedArm, error) {
	parts := strings.Split(value, ",")
	weighted := strings.Contains(parts[0], "=")
	arms := make([]WeightedArm, 0, len(parts))
	for _, part := range parts {
		selector, weight, hasWeight := strings.Cut(strings.TrimSpace(part), "=")
		if hasWeight != weighted {
			return nil, fmt.Errorf("planner routing arms must be all weighted or all bare selectors")
		}
		selector = strings.TrimSpace(selector)
		if selector == "" {
			return nil, fmt.Errorf("planner routing arm selector is required")
		}
		kind, err := runtimeconfig.ParseAgentKind(selector)
		if err != nil {
			return nil, err
		}
		probability := 1 / float64(len(parts))
		if weighted {
			probability, err = strconv.ParseFloat(strings.TrimSpace(weight), 64)
			if err != nil {
				return nil, fmt.Errorf("planner routing arm %q probability: %w", selector, err)
			}
		}
		arms = append(arms, WeightedArm{
			Arm:         Arm{Runtime: kind, Provider: Inherited, Model: Inherited, ReasoningEffort: Inherited},
			Probability: probability,
		})
	}
	if err := validateArms(arms, true); err != nil {
		return nil, err
	}
	return arms, nil
}

func ParseFloor(value string) (float64, error) {
	if value == "" {
		return 0.1, nil
	}
	floor, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("planner routing floor: %w", err)
	}
	if err := validateFloor(floor); err != nil {
		return 0, err
	}
	return floor, nil
}

func (p Policy) Validate() error {
	switch p.Mode {
	case ModeOff, ModeShadow, ModeRandomized:
	default:
		return fmt.Errorf("unsupported planner routing mode %q", p.Mode)
	}
	if err := validateFloor(p.Floor); err != nil {
		return err
	}
	if len(p.Arms) == 0 && p.Mode != ModeOff {
		return fmt.Errorf("planner routing %s mode requires at least one arm", p.Mode)
	}
	if err := validateArms(p.Arms, true); err != nil {
		return err
	}
	if p.Mode == ModeRandomized {
		for _, weighted := range p.Arms {
			arm := weighted.Arm
			if weighted.Probability < p.Floor {
				return fmt.Errorf("planner routing arm %q probability is below floor %g", arm.Key(), p.Floor)
			}
			if arm.Provider != Inherited || arm.Model != Inherited || arm.ReasoningEffort != Inherited {
				return fmt.Errorf("randomized planner routing arm %q requires inherited provider, model, and reasoning effort", arm.Key())
			}
		}
	}
	return nil
}

func NewPolicy(mode Mode, arms []WeightedArm, floor float64) (Policy, error) {
	mode, err := ParseMode(string(mode))
	if err != nil {
		return Policy{}, err
	}
	p := Policy{Version: PolicyVersion(mode, arms, floor), Mode: mode, Arms: slices.Clone(arms), Floor: floor}
	if err := p.Validate(); err != nil {
		return Policy{}, err
	}
	return p, nil
}

// PolicyVersion uses the same canonical arm order as assignment without
// modifying the caller's input. Quoted keys frame fields; fixed precision makes
// weights reproducible.
func PolicyVersion(mode Mode, arms []WeightedArm, floor float64) string {
	if mode == "" {
		mode = ModeOff
	}
	keys := make([]string, len(arms))
	for i, arm := range canonicalArms(arms) {
		keys[i] = policyArmKey(arm)
	}
	canonical := fmt.Sprintf("%q\n%s\n%s", mode, strings.Join(keys, "\n"), policyNumber(floor))
	digest := sha256.Sum256([]byte(canonical))
	return fmt.Sprintf("%x", digest[:8])
}

// canonicalArms fixes cumulative sampling intervals (including the fallback
// arm) independently of configuration or eligibility enumeration order.
func canonicalArms(arms []WeightedArm) []WeightedArm {
	ordered := slices.Clone(arms)
	slices.SortFunc(ordered, func(a, b WeightedArm) int {
		return strings.Compare(policyArmKey(a), policyArmKey(b))
	})
	return ordered
}

func policyArmKey(arm WeightedArm) string {
	return fmt.Sprintf("%q:%s", arm.Arm.Key(), policyNumber(arm.Probability))
}

func policyNumber(value float64) string {
	if value == 0 {
		value = 0 // Canonicalize negative zero.
	}
	return strconv.FormatFloat(value, 'f', 17, 64)
}

// Eligible preserves configured probabilities and policy order. Filtering is
// not renormalization; assignment canonicalizes the eligible order, uses its
// last arm as the fallback, and records effective propensities in Assignment.Eligible.
func Eligible(policy Policy, installed []runtimeconfig.AgentKind) ([]WeightedArm, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	var eligible []WeightedArm
	for _, arm := range policy.Arms {
		if slices.Contains(installed, arm.Arm.Runtime) {
			eligible = append(eligible, arm)
		}
	}
	if len(eligible) == 0 && policy.Mode == ModeRandomized {
		return nil, fmt.Errorf("randomized planner routing has no installed eligible runtimes")
	}
	return eligible, nil
}

func validateFloor(floor float64) error {
	if math.IsNaN(floor) || math.IsInf(floor, 0) || floor < 0 || floor > 0.5 {
		return fmt.Errorf("planner routing floor must be finite and within [0, 0.5]")
	}
	return nil
}

func validateArms(arms []WeightedArm, normalized bool) error {
	seen := make(map[string]bool, len(arms))
	var total float64
	for _, weighted := range arms {
		arm := weighted.Arm
		if arm.Runtime == "" {
			return fmt.Errorf("planner routing arm runtime is required")
		}
		if _, err := runtimeconfig.ParseAgentKind(string(arm.Runtime)); err != nil {
			return err
		}
		key := arm.Key()
		if seen[key] {
			return fmt.Errorf("duplicate planner routing arm %q", key)
		}
		seen[key] = true
		probability := weighted.Probability
		if math.IsNaN(probability) || math.IsInf(probability, 0) || probability < 0 {
			return fmt.Errorf("planner routing arm %q probability must be finite and non-negative", key)
		}
		total += probability
	}
	if normalized && len(arms) > 0 && math.Abs(total-1) > 1e-9 {
		return fmt.Errorf("planner routing probabilities must sum to 1 (got %g)", total)
	}
	return nil
}
