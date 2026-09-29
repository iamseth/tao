package runtimeconfig

import (
	"reflect"
	"testing"
)

func TestParsePlannerRoutingMode(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want PlannerRoutingMode
	}{{"", "off"}, {"off", "off"}, {"shadow", "shadow"}, {"randomized", "randomized"}} {
		got, err := ParsePlannerRoutingMode(tc.raw)
		if err != nil || got != tc.want {
			t.Errorf("ParsePlannerRoutingMode(%q) = %q, %v", tc.raw, got, err)
		}
	}
	for _, raw := range []string{"unknown", "SHADOW", " shadow", "randomized "} {
		if _, err := ParsePlannerRoutingMode(raw); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
}

func TestParsePlannerRoutingArms(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want []PlannerRoutingArm
	}{
		{"pi,claude", []PlannerRoutingArm{{AgentPi, 0.5}, {AgentClaude, 0.5}}},
		{"pi=0.5,claude=0.5", []PlannerRoutingArm{{AgentPi, 0.5}, {AgentClaude, 0.5}}},
		{"claude", []PlannerRoutingArm{{AgentClaude, 1}}},
		{" pi = 0.25 , claude = 0.75 ", []PlannerRoutingArm{{AgentPi, 0.25}, {AgentClaude, 0.75}}},
		{"pi=0,claude=1", []PlannerRoutingArm{{AgentPi, 0}, {AgentClaude, 1}}},
		{"pi=0.5,claude=0.5000000005", []PlannerRoutingArm{{AgentPi, 0.5}, {AgentClaude, 0.5000000005}}},
	} {
		got, err := ParsePlannerRoutingArms(tc.raw)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ParsePlannerRoutingArms(%q) = %+v, %v", tc.raw, got, err)
		}
	}
	for _, raw := range []string{
		"", " ", "=1", "pi,", ",pi", "pi,,claude", "unknown=1", "PI=1",
		"pi,pi", "pi=0.5,pi=0.5", "pi=NaN", "pi=Inf", "pi=-Inf", "pi=1e999",
		"pi=-0.1,claude=1.1", "pi=0.4,claude=0.4", "pi=0.5,claude=0.500000002",
		"pi=0,claude=0", "pi=", "pi=oops", "pi=0.5=0.5", "pi=0.5,claude", "pi,claude=0.5",
	} {
		if _, err := ParsePlannerRoutingArms(raw); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
}

func TestParsePlannerRoutingFloor(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want float64
	}{{"", 0.1}, {"0", 0}, {"0.5", 0.5}, {"0.25", 0.25}} {
		got, err := ParsePlannerRoutingFloor(tc.raw)
		if err != nil || got != tc.want {
			t.Errorf("ParsePlannerRoutingFloor(%q) = %v, %v", tc.raw, got, err)
		}
	}
	for _, raw := range []string{"-0.1", "0.500000001", "1", "NaN", "Inf", "-Inf", "1e999", "oops", " 0.1 "} {
		if _, err := ParsePlannerRoutingFloor(raw); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
}
