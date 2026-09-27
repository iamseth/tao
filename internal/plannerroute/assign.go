package plannerroute

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"slices"
	"strings"
)

func Assign(policy Policy, eligible []WeightedArm, unit UnitKey, override *Arm) (Assignment, error) {
	if err := policy.Validate(); err != nil {
		return Assignment{}, err
	}
	if policy.Mode == ModeOff {
		return Assignment{}, fmt.Errorf("cannot assign with planner routing off")
	}
	if strings.TrimSpace(policy.Version) == "" {
		return Assignment{}, fmt.Errorf("planner routing policy version is required")
	}
	if err := validateArms(eligible, false); err != nil {
		return Assignment{}, err
	}
	for _, arm := range eligible {
		if !slices.Contains(policy.Arms, arm) {
			return Assignment{}, fmt.Errorf("eligible planner routing arm %q is not in policy", arm.Arm.Key())
		}
	}
	if len(eligible) == 0 && override == nil {
		return Assignment{}, fmt.Errorf("planner routing assignment requires an eligible arm")
	}
	eligible = canonicalArms(eligible)
	// Quoting frames the fields so distinct tuples cannot concatenate to the
	// same input. The high 53 bits fit exactly in float64, keeping Draw < 1.
	input := fmt.Sprintf("%q\n%q\n%q\n%q", policy.Version, unit.RepoID, unit.Kind, unit.ID)
	digest := sha256.Sum256([]byte(input))
	draw := float64(binary.BigEndian.Uint64(digest[:8])>>11) / (1 << 53)
	assignment := Assignment{
		PolicyVersion: policy.Version,
		Mode:          policy.Mode,
		UnitKey:       unit,
		Eligible:      effectivePropensities(eligible),
		Draw:          draw,
	}
	if override != nil {
		arm := *override
		assignment.Selected = arm
		assignment.ManualOverride = true
		assignment.OverrideArm = &arm
	} else {
		assignment.Selected = selectArm(eligible, draw)
	}
	return assignment, nil
}

// effectivePropensities records the interval each arm receives from selectArm,
// not its configured weight. The last eligible arm absorbs the remaining draw
// range, including filtered-out weights and rounding slack. Configured weights
// remain in Policy.Arms and the input to Assign.
func effectivePropensities(eligible []WeightedArm) []WeightedArm {
	effective := slices.Clone(eligible)
	var cumulative float64
	for i, arm := range eligible {
		next := min(1, cumulative+arm.Probability)
		if i == len(eligible)-1 {
			next = 1
		}
		effective[i].Probability = next - cumulative
		cumulative = next
	}
	return effective
}

// selectArm requires a nonempty eligible set and a draw in [0, 1). Shadow mode
// uses the same recommendation; actual treatment is recorded separately.
func selectArm(eligible []WeightedArm, draw float64) Arm {
	var cumulative float64
	for _, arm := range eligible {
		cumulative += arm.Probability
		if draw < cumulative {
			return arm.Arm
		}
	}
	return eligible[len(eligible)-1].Arm
}
