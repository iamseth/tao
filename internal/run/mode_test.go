package run

import (
	"testing"

	"github.com/iamseth/tao/internal/runtimeconfig"
)

func TestModeAliasesMatchRuntimeConfigTypes(t *testing.T) {
	var mode = runtimeconfig.ModeStep
	request := Request{Input: "plan-a", ResolvedRunOptions: runtimeconfig.ResolvedRunOptions{Mode: runtimeconfig.ModeRun, Agent: runtimeconfig.AgentPi}}
	if mode != ModeStep || request.Agent != AgentPi || request.Mode != ModeRun {
		t.Fatalf("unexpected alias values: mode=%q request=%#v", mode, request)
	}
}
