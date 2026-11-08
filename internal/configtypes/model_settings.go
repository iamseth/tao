package configtypes

// ModelSetting describes one existing configurable field. An empty Role denotes
// escalation policy, not a fallback role. Profiles are command-profile names.
type ModelSetting struct {
	Key       string
	EnvSuffix string
	FlagName  string
	Kind      string
	Role      ModelRole
	Profiles  []string
	Field     func(*ModelSelection) *string
}

// Preserve environment/schema order; consumers may filter by Kind when they
// need separate model and effort enumerations.
var modelSettings = [...]ModelSetting{
	{"models.model", "MODEL", "model", "model", ModelRoleDefault, []string{"run", "review", "merge"}, func(m *ModelSelection) *string { return &m.Base }},
	{"models.run_model", "RUN_MODEL", "run-model", "model", ModelRoleRun, []string{"run"}, func(m *ModelSelection) *string { return &m.Run }},
	{"models.review_model", "REVIEW_MODEL", "review-model", "model", ModelRoleReview, []string{"run", "review"}, func(m *ModelSelection) *string { return &m.Review }},
	{"models.merge_review_model", "MERGE_REVIEW_MODEL", "merge-review-model", "model", ModelRoleMergeReview, []string{"merge"}, func(m *ModelSelection) *string { return &m.MergeReview }},
	{"models.resolver_model", "RESOLVER_MODEL", "resolver-model", "model", ModelRoleResolver, []string{"merge"}, func(m *ModelSelection) *string { return &m.Resolver }},
	{"models.rework_escalation_model", "REWORK_ESCALATION_MODEL", "rework-escalation-model", "model", "", []string{"run"}, func(m *ModelSelection) *string { return &m.ReworkEscalation }},
	{"models.effort", "EFFORT", "effort", "effort", ModelRoleDefault, []string{"run", "review", "merge"}, func(m *ModelSelection) *string { return &m.Effort }},
	{"models.run_effort", "RUN_EFFORT", "run-effort", "effort", ModelRoleRun, []string{"run"}, func(m *ModelSelection) *string { return &m.RunEffort }},
	{"models.review_effort", "REVIEW_EFFORT", "review-effort", "effort", ModelRoleReview, []string{"run", "review"}, func(m *ModelSelection) *string { return &m.ReviewEffort }},
	{"models.merge_review_effort", "MERGE_REVIEW_EFFORT", "merge-review-effort", "effort", ModelRoleMergeReview, []string{"merge"}, func(m *ModelSelection) *string { return &m.MergeReviewEffort }},
	{"models.resolver_effort", "RESOLVER_EFFORT", "resolver-effort", "effort", ModelRoleResolver, []string{"merge"}, func(m *ModelSelection) *string { return &m.ResolverEffort }},
}

// ModelSettings returns independent descriptors, including their profile lists.
func ModelSettings() []ModelSetting {
	settings := append([]ModelSetting(nil), modelSettings[:]...)
	for i := range settings {
		settings[i].Profiles = append([]string(nil), settings[i].Profiles...)
	}
	return settings
}

func (m ModelSelection) forKind(role ModelRole, kind string) string {
	var fallback string
	for _, setting := range modelSettings {
		if setting.Kind != kind || setting.Role == "" {
			continue
		}
		value := *setting.Field(&m)
		if setting.Role == ModelRoleDefault {
			fallback = value
		}
		if setting.Role == role && value != "" {
			return value
		}
	}
	return fallback
}
