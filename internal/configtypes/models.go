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
	Base        string `json:"model,omitempty"`
	Run         string `json:"run_model,omitempty"`
	Review      string `json:"review_model,omitempty"`
	MergeReview string `json:"merge_review_model,omitempty"`
	Resolver    string `json:"resolver_model,omitempty"`
	// ReworkEscalation is opt-in rework policy, not a role fallback for For.
	ReworkEscalation string `json:"rework_escalation_model,omitempty"`
}

func (m ModelSelection) For(role ModelRole) string {
	var override string
	switch role {
	case ModelRoleRun:
		override = m.Run
	case ModelRoleReview:
		override = m.Review
	case ModelRoleMergeReview:
		override = m.MergeReview
	case ModelRoleResolver:
		override = m.Resolver
	}
	if override != "" {
		return override
	}
	return m.Base
}
