package workspace

// Metadata describes Tao's view of one plan workspace.
type Metadata struct {
	PlanID            string
	Path              string
	Branch            string
	RecordedBranch    string
	RecordedHeadSHA   string
	BaseBranch        string
	BaseSHA           string
	BaseCurrentSHA    string
	BaseStatus        string
	HeadSHA           string
	RefreshStatus     string
	RebaseStatus      string
	Created           bool
	Reused            bool
	Rebased           bool
	Dirty             bool
	Missing           bool
	DependencyStatus  string
	DependencyCommand string
}

// CleanPlan describes a non-destructive cleanup decision.
type CleanPlan struct {
	PlanID          string
	Path            string
	Branch          string
	Status          string
	CanRemove       bool
	Dirty           bool
	Missing         bool
	ProtectedBranch bool
	BranchExists    bool
	Reason          string
	Actions         []string
}

// CleanOptions controls workspace and managed branch removal once callers have accepted a plan.
type CleanOptions struct {
	Force                   bool
	ForceDirty              bool
	AllowNonAncestralBranch bool
}
