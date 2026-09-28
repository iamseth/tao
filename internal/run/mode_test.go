package run

import (
	"testing"

	"github.com/iamseth/tao/internal/runtimeconfig"
)

func TestRunOptionAliasesMatchRuntimeConfigTypes(t *testing.T) {
	request := Request{Input: "plan-a", ResolvedRunOptions: runtimeconfig.ResolvedRunOptions{
		CommitPolicy:  runtimeconfig.CommitPolicySlice,
		ExecutionMode: runtimeconfig.ExecutionModeIsolated,
		Agent:         runtimeconfig.AgentPi,
	}}
	if request.CommitPolicy != CommitPolicySlice || request.ExecutionMode != ExecutionModeIsolated || request.Agent != AgentPi {
		t.Fatalf("unexpected alias values: request=%#v", request)
	}
}
