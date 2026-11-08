package configtypes

type ModelRole string

const (
	ModelRoleDefault     ModelRole = "default"
	ModelRoleRun         ModelRole = "run"
	ModelRoleReview      ModelRole = "review"
	ModelRoleMergeReview ModelRole = "merge_review"
	ModelRoleResolver    ModelRole = "resolver"
)

// ModelSelection keeps role overrides separate from their shared fallback.
// Empty names leave model selection to the agent runtime.
type ModelSelection struct {
	Base              string `json:"model,omitempty"`
	Run               string `json:"run_model,omitempty"`
	Review            string `json:"review_model,omitempty"`
	MergeReview       string `json:"merge_review_model,omitempty"`
	Resolver          string `json:"resolver_model,omitempty"`
	Effort            string `json:"effort,omitempty"`
	RunEffort         string `json:"run_effort,omitempty"`
	ReviewEffort      string `json:"review_effort,omitempty"`
	MergeReviewEffort string `json:"merge_review_effort,omitempty"`
	ResolverEffort    string `json:"resolver_effort,omitempty"`
	// ReworkEscalation is opt-in rework policy, not a role fallback for For.
	// It has no effort counterpart; escalated rounds retain run-role effort.
	ReworkEscalation string `json:"rework_escalation_model,omitempty"`
}

// EffortFor resolves a role override before the shared effort fallback.
func (m ModelSelection) EffortFor(role ModelRole) string {
	return m.forKind(role, "effort")
}

func (m ModelSelection) For(role ModelRole) string {
	return m.forKind(role, "model")
}
