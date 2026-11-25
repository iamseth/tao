package cli

import (
	"bytes"
	"testing"

	"github.com/iamseth/tao/internal/plan"
)

func TestRenderFinalContractHistoryVerification(t *testing.T) {
	for _, tt := range []struct {
		name         string
		verification plan.FinalVerification
		want         string
	}{
		{
			name:         "passed",
			verification: plan.FinalVerification{Result: "passed"},
			want:         "Final verification: \"passed\"\n",
		},
		{
			name:         "failed with diagnostics",
			verification: plan.FinalVerification{Result: "failed", FailureKind: plan.FinalVerificationFailureKindCode, Details: "gate failed"},
			want:         "Final verification: \"failed\"; failure kind: \"code\"\n  Reason: \"gate failed\"\n",
		},
		{
			name:         "legacy failure",
			verification: plan.FinalVerification{Result: "failed"},
			want:         "Final verification: \"failed\"\n",
		},
		{
			name:         "passed with retained diagnostics",
			verification: plan.FinalVerification{Result: "passed", Details: "same-head retry passed"},
			want:         "Final verification: \"passed\"\n  Reason: \"same-head retry passed\"\n",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var detail plan.PlanDetail
			detail.State.Plan.FinalVerification = &tt.verification
			var out bytes.Buffer
			if err := renderFinalContractHistory(&out, &detail); err != nil {
				t.Fatal(err)
			}
			if got := out.String(); got != tt.want {
				t.Fatalf("renderFinalContractHistory() = %q, want %q", got, tt.want)
			}
		})
	}
}
