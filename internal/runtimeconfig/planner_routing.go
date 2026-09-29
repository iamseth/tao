package runtimeconfig

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// PlannerRoutingMode selects routing behavior; policy and assignment remain in
// plannerroute, which consumes this package's syntax-only configuration.
type PlannerRoutingMode string

type PlannerRoutingArm struct {
	Runtime     AgentKind
	Probability float64
}

type PlannerRoutingConfig struct {
	Mode                       PlannerRoutingMode
	Arms                       []PlannerRoutingArm
	Floor                      float64
	ModeSet, ArmsSet, FloorSet bool
}

func ParsePlannerRoutingMode(value string) (PlannerRoutingMode, error) {
	if value == "" {
		return "off", nil
	}
	switch value {
	case "off", "shadow", "randomized":
		return PlannerRoutingMode(value), nil
	default:
		return "", fmt.Errorf("unsupported planner routing mode %q (want off, shadow, or randomized)", value)
	}
}

// ParsePlannerRoutingArms accepts either all weighted selectors or all bare
// selectors. Configured probabilities must be finite, non-negative, and sum to
// one; floor compatibility and treatment metadata are policy concerns.
func ParsePlannerRoutingArms(value string) ([]PlannerRoutingArm, error) {
	parts := strings.Split(value, ",")
	weighted := strings.Contains(parts[0], "=")
	arms := make([]PlannerRoutingArm, 0, len(parts))
	seen := make(map[AgentKind]bool, len(parts))
	var total float64
	for _, part := range parts {
		selector, weight, hasWeight := strings.Cut(strings.TrimSpace(part), "=")
		if hasWeight != weighted {
			return nil, fmt.Errorf("planner routing arms must be all weighted or all bare selectors")
		}
		selector = strings.TrimSpace(selector)
		if selector == "" {
			return nil, fmt.Errorf("planner routing arm selector is required")
		}
		kind, err := ParseAgentKind(selector)
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
		if seen[kind] {
			return nil, fmt.Errorf("duplicate planner routing arm %q", selector)
		}
		seen[kind] = true
		if math.IsNaN(probability) || math.IsInf(probability, 0) || probability < 0 {
			return nil, fmt.Errorf("planner routing arm %q probability must be finite and non-negative", selector)
		}
		total += probability
		arms = append(arms, PlannerRoutingArm{Runtime: kind, Probability: probability})
	}
	if math.Abs(total-1) > 1e-9 {
		return nil, fmt.Errorf("planner routing probabilities must sum to 1 (got %g)", total)
	}
	return arms, nil
}

func ParsePlannerRoutingFloor(value string) (float64, error) {
	if value == "" {
		return 0.1, nil
	}
	floor, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("planner routing floor: %w", err)
	}
	if math.IsNaN(floor) || math.IsInf(floor, 0) || floor < 0 || floor > 0.5 {
		return 0, fmt.Errorf("planner routing floor must be finite and within [0, 0.5]")
	}
	return floor, nil
}
