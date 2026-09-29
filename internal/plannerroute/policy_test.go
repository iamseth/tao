package plannerroute

import (
	"math"
	"reflect"
	"testing"

	"github.com/iamseth/tao/internal/runtimeconfig"
)

func TestParseArms(t *testing.T) {
	for _, tt := range []struct {
		value string
		want  []float64
	}{
		{"pi=0.5,claude=0.5", []float64{0.5, 0.5}},
		{"pi,claude", []float64{0.5, 0.5}},
		{"pi", []float64{1}},
		{" pi = 0.25 , claude = 0.75 ", []float64{0.25, 0.75}},
		{"pi=0,claude=1", []float64{0, 1}},
		{"pi=0.5,claude=0.5000000005", []float64{0.5, 0.5000000005}},
	} {
		t.Run(tt.value, func(t *testing.T) {
			arms, err := ParseArms(tt.value)
			if err != nil {
				t.Fatal(err)
			}
			if len(arms) != len(tt.want) {
				t.Fatalf("arms = %+v", arms)
			}
			for i, arm := range arms {
				wantKind := runtimeconfig.AgentPi
				if i == 1 {
					wantKind = runtimeconfig.AgentClaude
				}
				if arm.Probability != tt.want[i] || arm.Arm.Runtime != wantKind ||
					arm.Arm.Provider != Inherited || arm.Arm.Model != Inherited || arm.Arm.ReasoningEffort != Inherited {
					t.Errorf("arm %d = %+v", i, arm)
				}
			}
		})
	}
	for _, value := range []string{
		"", " ", "=1", "pi,", ",pi", "pi,,claude", "unknown=1", "PI=1",
		"pi,pi", "pi=0.5,pi=0.5", "pi=NaN", "pi=Inf", "pi=-Inf", "pi=1e999",
		"pi=-0.1,claude=1.1", "pi=0.4,claude=0.4", "pi=0.5,claude=0.500000002",
		"pi=0,claude=0", "pi=", "pi=oops", "pi=0.5=0.5", "pi=0.5,claude", "pi,claude=0.5",
	} {
		t.Run("invalid/"+value, func(t *testing.T) {
			if _, err := ParseArms(value); err == nil {
				t.Fatalf("ParseArms(%q) accepted invalid arms", value)
			}
		})
	}
}

func TestParseFloor(t *testing.T) {
	for _, tt := range []struct {
		value string
		want  float64
	}{{"", 0.1}, {"0", 0}, {"0.5", 0.5}, {"0.25", 0.25}} {
		got, err := ParseFloor(tt.value)
		if err != nil || got != tt.want {
			t.Errorf("ParseFloor(%q) = %v, %v; want %v", tt.value, got, err, tt.want)
		}
	}
	for _, value := range []string{"-0.1", "0.500000001", "1", "NaN", "Inf", "-Inf", "oops"} {
		if _, err := ParseFloor(value); err == nil {
			t.Errorf("ParseFloor(%q) accepted invalid floor", value)
		}
	}
}

func TestPolicyFromTypedRoutingConfig(t *testing.T) {
	values := map[string]string{
		runtimeconfig.EnvPlannerRouting:      "randomized",
		runtimeconfig.EnvPlannerRoutingArms:  "pi=0.25,claude=0.75",
		runtimeconfig.EnvPlannerRoutingFloor: "0.1",
	}
	snapshot := runtimeconfig.LoadEnv(func(key string) (string, bool) { value, ok := values[key]; return value, ok })
	if err := snapshot.Require(runtimeconfig.EnvPlannerRouting, runtimeconfig.EnvPlannerRoutingArms, runtimeconfig.EnvPlannerRoutingFloor); err != nil {
		t.Fatal(err)
	}
	config := snapshot.Defaults().PlannerRouting
	arms := ArmsFromConfig(config.Arms)
	p, err := NewPolicy(Mode(config.Mode), arms, config.Floor)
	if err != nil {
		t.Fatal(err)
	}
	legacyArms, err := ParseArms(values[runtimeconfig.EnvPlannerRoutingArms])
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := NewPolicy(ModeRandomized, legacyArms, 0.1)
	if err != nil || !reflect.DeepEqual(p, legacy) {
		t.Fatalf("typed policy changed: %+v, want %+v; %v", p, legacy, err)
	}
	if _, err := NewPolicy(Mode(config.Mode), arms, 0.5); err == nil {
		t.Fatal("typed conversion bypassed floor policy")
	}
	arms[0].Probability = 0.5
	if _, err := NewPolicy(Mode(config.Mode), arms, config.Floor); err == nil {
		t.Fatal("typed conversion bypassed weight policy")
	}
}

func TestPolicyValidate(t *testing.T) {
	for _, mode := range []Mode{ModeOff, ModeShadow, ModeRandomized} {
		p := Policy{Mode: mode, Arms: policyTestArms(), Floor: 0.1}
		if err := p.Validate(); err != nil {
			t.Fatalf("valid %s policy: %v", mode, err)
		}
		p.Arms = nil
		if err := p.Validate(); (err == nil) != (mode == ModeOff) {
			t.Errorf("empty %s policy: %v", mode, err)
		}
	}
	if err := (Policy{Mode: ModeRandomized, Arms: policyTestArms(), Floor: 0.5}).Validate(); err != nil {
		t.Fatalf("probabilities exactly at floor rejected: %v", err)
	}
	for _, change := range []func(*Policy){
		func(p *Policy) { p.Arms[0].Arm.Provider = "custom" },
		func(p *Policy) { p.Arms[0].Arm.Model = "custom" },
		func(p *Policy) { p.Arms[0].Arm.ReasoningEffort = "custom" },
		func(p *Policy) { p.Arms[0].Arm.Provider = "" },
		func(p *Policy) { p.Arms[0].Probability, p.Arms[1].Probability = 0.05, 0.95 },
	} {
		for _, mode := range []Mode{ModeOff, ModeShadow, ModeRandomized} {
			p := Policy{Mode: mode, Arms: policyTestArms(), Floor: 0.1}
			change(&p)
			if err := p.Validate(); (err != nil) != (mode == ModeRandomized) {
				t.Errorf("mode %s: unexpected validation %v for %+v", mode, err, p)
			}
		}
	}
	for _, change := range []func(*Policy){
		func(p *Policy) { p.Mode = "invalid" },
		func(p *Policy) { p.Floor = math.NaN() },
		func(p *Policy) { p.Floor = math.Inf(1) },
		func(p *Policy) { p.Floor = -0.1 },
		func(p *Policy) { p.Floor = 0.6 },
		func(p *Policy) { p.Arms[0].Arm.Runtime = "unknown" },
		func(p *Policy) { p.Arms[0].Arm.Runtime = "" },
		func(p *Policy) { p.Arms[1] = p.Arms[0] },
		func(p *Policy) { p.Arms[0].Probability = math.NaN() },
		func(p *Policy) { p.Arms[0].Probability = math.Inf(1) },
		func(p *Policy) { p.Arms[0].Probability = -0.1 },
		func(p *Policy) { p.Arms[0].Probability = 0.2 },
	} {
		p := Policy{Mode: ModeShadow, Arms: policyTestArms(), Floor: 0.1}
		change(&p)
		if err := p.Validate(); err == nil {
			t.Errorf("accepted invalid policy %+v", p)
		}
	}
}

func TestNewPolicyAndVersion(t *testing.T) {
	arms := policyTestArms()
	original := append([]WeightedArm(nil), arms...)
	p, err := NewPolicy(ModeRandomized, arms, 0.1)
	if err != nil || p.Version == "" || p.Version != PolicyVersion(p.Mode, arms, p.Floor) {
		t.Fatalf("NewPolicy = %+v, %v", p, err)
	}
	reversed := []WeightedArm{arms[1], arms[0]}
	if got := PolicyVersion(p.Mode, reversed, p.Floor); got != p.Version {
		t.Errorf("version depends on arm order: %s != %s", got, p.Version)
	}
	if !reflect.DeepEqual(arms, original) || reversed[0] != arms[1] {
		t.Fatal("version calculation mutated input")
	}
	for _, change := range []func(*Policy){
		func(p *Policy) { p.Mode = ModeShadow },
		func(p *Policy) { p.Floor = 0.2 },
		func(p *Policy) { p.Arms = p.Arms[:1] },
		func(p *Policy) { p.Arms[0].Probability, p.Arms[1].Probability = 0.4, 0.6 },
		func(p *Policy) { p.Arms[0].Arm.Runtime = runtimeconfig.AgentClaude },
		func(p *Policy) { p.Arms[0].Arm.Provider = "other" },
		func(p *Policy) { p.Arms[0].Arm.Model = "other" },
		func(p *Policy) { p.Arms[0].Arm.ReasoningEffort = "other" },
		func(p *Policy) { p.Arms[0].Arm.PromptVersion = "other" },
		func(p *Policy) { p.Arms[0].Arm.PermissionMode = "other" },
	} {
		other := p
		other.Arms = append([]WeightedArm(nil), p.Arms...)
		change(&other)
		if got := PolicyVersion(other.Mode, other.Arms, other.Floor); got == p.Version {
			t.Errorf("changed policy has same version: %+v", other)
		}
	}
	if _, err := NewPolicy(ModeRandomized, nil, 0.1); err == nil {
		t.Fatal("NewPolicy accepted invalid policy")
	}
	if got, err := NewPolicy("", nil, 0.1); err != nil || got.Mode != ModeOff || got.Version != PolicyVersion(ModeOff, nil, 0.1) {
		t.Fatalf("default mode not normalized: %+v, %v", got, err)
	}
	arms[0].Probability = 0
	if p.Arms[0].Probability != original[0].Probability {
		t.Fatal("policy aliases input arms")
	}
}

func TestCanonicalArms(t *testing.T) {
	arms := policyTestArms()
	// Unequal weights must stay attached to their exact treatment tuples.
	arms[0].Probability, arms[1].Probability = 0.25, 0.75
	original := append([]WeightedArm(nil), arms...)
	want := []WeightedArm{arms[1], arms[0]}
	got := canonicalArms(arms)
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(arms, original) {
		t.Fatalf("canonical order = %+v; input = %+v", got, arms)
	}
	if !reflect.DeepEqual(canonicalArms(want), got) {
		t.Fatal("canonical order depends on input order")
	}
	got[0].Probability = 0
	if !reflect.DeepEqual(arms, original) {
		t.Fatal("canonical arms alias input")
	}
}

func TestEligible(t *testing.T) {
	p, err := NewPolicy(ModeRandomized, policyTestArms(), 0.1)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Eligible(p, []runtimeconfig.AgentKind{runtimeconfig.AgentClaude})
	if err != nil || !reflect.DeepEqual(got, p.Arms[1:]) || got[0].Probability != 0.5 {
		t.Fatalf("filtered eligibility = %+v, %v; must not renormalize", got, err)
	}
	got, err = Eligible(p, []runtimeconfig.AgentKind{runtimeconfig.AgentClaude, runtimeconfig.AgentPi, runtimeconfig.AgentPi, "unknown"})
	if err != nil || !reflect.DeepEqual(got, p.Arms) {
		t.Fatalf("policy order not preserved: %+v, %v", got, err)
	}
	got[0].Probability = 0
	if p.Arms[0].Probability != 0.5 {
		t.Fatal("eligibility aliases policy arms")
	}
	if _, err := Eligible(p, nil); err == nil {
		t.Fatal("randomized policy accepted no installed arms")
	}
	p.Mode = ModeShadow
	if got, err := Eligible(p, nil); err != nil || len(got) != 0 {
		t.Fatalf("shadow with no installed arms = %+v, %v", got, err)
	}
	p.Arms[0].Probability, p.Arms[1].Probability = 0.05, 0.95
	if got, err := Eligible(p, []runtimeconfig.AgentKind{runtimeconfig.AgentPi}); err != nil || !reflect.DeepEqual(got, p.Arms[:1]) {
		t.Fatalf("shadow sub-floor eligibility = %+v, %v", got, err)
	}
	p.Mode = ModeRandomized
	if _, err := Eligible(p, []runtimeconfig.AgentKind{runtimeconfig.AgentPi}); err == nil {
		t.Fatal("sub-floor probability accepted after filtering")
	}
}

func policyTestArms() []WeightedArm {
	pi := testArm()
	claude := pi
	claude.Runtime = runtimeconfig.AgentClaude
	return []WeightedArm{{Arm: pi, Probability: 0.5}, {Arm: claude, Probability: 0.5}}
}
