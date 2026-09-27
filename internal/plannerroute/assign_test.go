package plannerroute

import (
	"math"
	"reflect"
	"testing"

	"github.com/iamseth/tao/internal/runtimeconfig"
)

func TestAssignDeterministic(t *testing.T) {
	p, err := NewPolicy(ModeRandomized, policyTestArms(), 0.1)
	if err != nil {
		t.Fatal(err)
	}
	unit := UnitKey{RepoID: "repo-1", Kind: "note", ID: "note-1"}
	got, err := Assign(p, p.Arms, unit, nil)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Assign(p, p.Arms, unit, nil)
	if err != nil || !reflect.DeepEqual(got, again) {
		t.Fatalf("assignment not sticky: %+v, %+v, %v", got, again, err)
	}
	if got.PolicyVersion != p.Version || got.Mode != p.Mode || got.UnitKey != unit ||
		got.ManualOverride || got.OverrideArm != nil || !reflect.DeepEqual(got.Eligible, []WeightedArm{p.Arms[1], p.Arms[0]}) || got.Draw < 0 || got.Draw >= 1 {
		t.Fatalf("invalid assignment: %+v", got)
	}
	want := p.Arms[1].Arm // Claude sorts before Pi.
	if got.Draw >= 0.5 {
		want = p.Arms[0].Arm
	}
	if got.Selected != want {
		t.Fatalf("selected %+v at draw %v; want %+v", got.Selected, got.Draw, want)
	}
	for _, other := range []UnitKey{
		{RepoID: "repo-1", Kind: "note", ID: "note-2"},
		{RepoID: "repo-2", Kind: "note", ID: "note-1"},
		{RepoID: "repo-1", Kind: "other", ID: "note-1"},
	} {
		a, err := Assign(p, p.Arms, other, nil)
		if err != nil || a.Draw == got.Draw {
			t.Errorf("changed unit draw = %v, %v", a.Draw, err)
		}
	}
	otherPolicy := p
	otherPolicy.Version += "-other"
	a, err := Assign(otherPolicy, p.Arms, unit, nil)
	if err != nil || a.Draw == got.Draw {
		t.Errorf("changed version draw = %v, %v", a.Draw, err)
	}
	got.Eligible[0].Probability = 0
	if p.Arms[0].Probability != 0.5 {
		t.Fatal("assignment aliases eligible arms")
	}
}

func TestAssignIgnoresArmOrder(t *testing.T) {
	for _, mode := range []Mode{ModeRandomized, ModeShadow} {
		t.Run(string(mode), func(t *testing.T) {
			arms := policyTestArms()
			reversed := []WeightedArm{arms[1], arms[0]}
			unit := UnitKey{RepoID: "repo-1", Kind: "note", ID: "note-1"}
			var first Assignment
			for i, configured := range [][]WeightedArm{arms, reversed} {
				p, err := NewPolicy(mode, configured, 0.1)
				if err != nil {
					t.Fatal(err)
				}
				for j, eligible := range [][]WeightedArm{arms, reversed} {
					before := append([]WeightedArm(nil), eligible...)
					got, err := Assign(p, eligible, unit, nil)
					if err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(eligible, before) {
						t.Fatal("assignment reordered caller-owned eligible arms")
					}
					if i == 0 && j == 0 {
						first = got
					} else if got.PolicyVersion != first.PolicyVersion || got.Draw != first.Draw || got.Selected != first.Selected || !reflect.DeepEqual(got.Eligible, first.Eligible) {
						t.Fatalf("arm ordering changed sticky assignment: first=%+v, got=%+v", first, got)
					}
				}
			}
		})
	}
}

func TestSelectArmBoundaries(t *testing.T) {
	arms := policyTestArms()
	for _, tt := range []struct {
		draw float64
		want int
	}{
		{0, 0}, {math.Nextafter(0.5, 0), 0}, {0.5, 1}, {math.Nextafter(1, 0), 1},
	} {
		if got := selectArm(arms, tt.draw); got != arms[tt.want].Arm {
			t.Errorf("draw %v selected %+v; want arm %d", tt.draw, got, tt.want)
		}
	}
	arms[0].Probability, arms[1].Probability = 0, 1
	if got := selectArm(arms, 0); got != arms[1].Arm {
		t.Fatal("zero-probability arm selected at zero")
	}
	arms[0].Probability, arms[1].Probability = 0.5, 0.4999999995
	if got := selectArm(arms, math.Nextafter(1, 0)); got != arms[1].Arm {
		t.Fatal("last arm did not absorb rounding")
	}
	if got := selectArm(arms[:1], math.Nextafter(1, 0)); got != arms[0].Arm {
		t.Fatal("filtered final arm did not absorb remaining draw")
	}
}

func TestAssignEffectivePropensities(t *testing.T) {
	for _, installed := range [][]runtimeconfig.AgentKind{
		{runtimeconfig.AgentPi}, {runtimeconfig.AgentClaude},
		{runtimeconfig.AgentPi, runtimeconfig.AgentClaude},
	} {
		p, err := NewPolicy(ModeRandomized, policyTestArms(), 0.1)
		if err != nil {
			t.Fatal(err)
		}
		eligible, err := Eligible(p, installed)
		if err != nil {
			t.Fatal(err)
		}
		a, err := Assign(p, eligible, UnitKey{ID: "note"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, arm := range a.Eligible {
			if want := 1 / float64(len(installed)); arm.Probability != want {
				t.Errorf("installed %v: probability = %g, want %g", installed, arm.Probability, want)
			}
		}
		if a.Selected != selectArm(canonicalArms(eligible), a.Draw) {
			t.Fatal("effective propensity changed selection")
		}
		if eligible[0].Probability != 0.5 || p.Arms[0].Probability != 0.5 {
			t.Fatal("assignment mutated configured weights")
		}
	}
}

func TestAssignEffectiveFallbackProbability(t *testing.T) {
	for _, weights := range [][]float64{
		{0.25, 0.5, 0.25},
		{0.5, 0.4999999995},
		{0.5, 0.5000000005},
		{1.0000000005, 0},
	} {
		arms := policyTestArms()
		if len(weights) == 3 {
			other := arms[0]
			other.Arm.PermissionMode = "other"
			arms = append(arms, other)
		}
		for i, weight := range weights {
			arms[i].Probability = weight
		}
		p, err := NewPolicy(ModeRandomized, arms, 0)
		if err != nil {
			t.Fatal(err)
		}
		eligible := arms
		if len(weights) == 3 {
			eligible = []WeightedArm{arms[0], arms[2]}
		}
		a, err := Assign(p, eligible, UnitKey{ID: "note"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		eligible = canonicalArms(eligible)
		first := min(eligible[0].Probability, 1)
		if a.Eligible[0].Probability != first || a.Eligible[1].Probability != 1-first {
			t.Errorf("weights %v: effective probabilities = %+v", weights, a.Eligible)
		}
		for _, draw := range []float64{0, 0.25, 0.5, 0.75, math.Nextafter(1, 0)} {
			if selectArm(a.Eligible, draw) != selectArm(eligible, draw) {
				t.Errorf("weights %v: selection changed at %g", weights, draw)
			}
		}
	}
}

func TestAssignOverrideAndShadow(t *testing.T) {
	unit := UnitKey{RepoID: "repo", Kind: "note", ID: "id"}
	for _, mode := range []Mode{ModeRandomized, ModeShadow} {
		p, err := NewPolicy(mode, policyTestArms(), 0.1)
		if err != nil {
			t.Fatal(err)
		}
		recommendation, err := Assign(p, p.Arms, unit, nil)
		if err != nil || recommendation.Selected != selectArm(canonicalArms(p.Arms), recommendation.Draw) || recommendation.ManualOverride {
			t.Fatalf("%s recommendation = %+v, %v", mode, recommendation, err)
		}
		override := testArm()
		override.PermissionMode = "manual"
		a, err := Assign(p, p.Arms, unit, &override)
		if err != nil || a.Selected != override || !a.ManualOverride || a.OverrideArm == nil ||
			*a.OverrideArm != override || a.Draw != recommendation.Draw || a.Mode != mode {
			t.Fatalf("%s override = %+v, %v", mode, a, err)
		}
		override.PermissionMode = "changed"
		if a.OverrideArm.PermissionMode != "manual" {
			t.Fatal("assignment aliases override")
		}
	}
}

func TestAssignErrors(t *testing.T) {
	p, err := NewPolicy(ModeRandomized, policyTestArms(), 0.1)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []Mode{ModeOff, "invalid"} {
		other := p
		other.Mode = mode
		if _, err := Assign(other, p.Arms, UnitKey{}, nil); err == nil {
			t.Errorf("Assign accepted mode %q", mode)
		}
	}
	for _, mode := range []Mode{ModeRandomized, ModeShadow} {
		p.Mode = mode
		if _, err := Assign(p, nil, UnitKey{}, nil); err == nil {
			t.Errorf("%s assignment accepted no eligible arms", mode)
		}
	}
	for _, change := range []func(*Policy, *[]WeightedArm){
		func(p *Policy, _ *[]WeightedArm) { p.Version = "" },
		func(_ *Policy, arms *[]WeightedArm) { (*arms)[0].Probability = math.NaN() },
		func(_ *Policy, arms *[]WeightedArm) { (*arms)[0].Probability = 0.4 },
		func(_ *Policy, arms *[]WeightedArm) { (*arms)[0].Arm.PromptVersion = "not-in-policy" },
		func(_ *Policy, arms *[]WeightedArm) { *arms = append(*arms, (*arms)[0]) },
	} {
		other := p
		eligible := append([]WeightedArm(nil), p.Arms...)
		change(&other, &eligible)
		if _, err := Assign(other, eligible, UnitKey{}, nil); err == nil {
			t.Errorf("accepted invalid policy/eligible input: %+v, %+v", other, eligible)
		}
	}
}

func TestAssignmentHashFramesFields(t *testing.T) {
	p, err := NewPolicy(ModeShadow, policyTestArms(), 0.1)
	if err != nil {
		t.Fatal(err)
	}
	first, err := Assign(p, p.Arms, UnitKey{RepoID: "repo", Kind: "note", ID: "id"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Assign(p, p.Arms, UnitKey{RepoID: "repon", Kind: "ote", ID: "id"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Draw == second.Draw {
		t.Fatal("distinct field tuples produced identical draws")
	}
}
