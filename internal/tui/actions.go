package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/plan"
)

const defaultActionStartupTimeout = 10 * time.Second

// CommandRequest describes one Tao subprocess launched by the dashboard.
type CommandRequest struct {
	CWD        string
	Executable string
	Args       []string
	Detached   bool
}

// CommandLauncher is the process boundary used by dashboard actions.
type CommandLauncher func(context.Context, CommandRequest) error

// ActionOptions supplies the process, observation, and clock seams for Actions.
type ActionOptions struct {
	Executable     string
	Launcher       CommandLauncher
	ReadRunLock    func(string) (plan.RunLock, error)
	Now            func() time.Time
	StartupTimeout time.Duration
}

type actionKind string

const (
	actionRun     actionKind = "run"
	actionApprove actionKind = "approve"
	actionMerge   actionKind = "merge"
	actionFailed  actionKind = "failed"
)

type actionFeedback struct {
	kind            actionKind
	label           string
	startedAt       time.Time
	approvalSliceID string
	initialStatus   string
}

// Actions launches Tao commands and tracks their best-effort startup feedback.
type Actions struct {
	executable     string
	launcher       CommandLauncher
	readRunLock    func(string) (plan.RunLock, error)
	now            func() time.Time
	startupTimeout time.Duration
	feedback       map[string]actionFeedback
	message        string
	messageKey     string
}

// NewActions constructs the action controller used by one dashboard loop.
func NewActions(options ActionOptions) (*Actions, error) {
	if strings.TrimSpace(options.Executable) == "" {
		return nil, errors.New("dashboard action executable is required")
	}
	if options.Launcher == nil {
		return nil, errors.New("dashboard command launcher is required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.ReadRunLock == nil {
		options.ReadRunLock = plan.ReadRunLock
	}
	if options.StartupTimeout <= 0 {
		options.StartupTimeout = defaultActionStartupTimeout
	}
	return &Actions{
		executable:     options.Executable,
		launcher:       options.Launcher,
		readRunLock:    options.ReadRunLock,
		now:            options.Now,
		startupTimeout: options.StartupTimeout,
		feedback:       make(map[string]actionFeedback),
	}, nil
}

// RunPlan starts the selected plan unless its fresh heartbeat already shows it live.
func (a *Actions) RunPlan(ctx context.Context, row monitor.Row) {
	if !actionableRow(row) || row.Liveness == monitor.LivenessLive {
		return
	}
	args := []string{"run", row.PlanID}
	switch row.Status {
	case plan.StatusBlocked:
		args = []string{"run", "--continue", row.PlanID}
	case plan.StatusVerificationFailed:
		var err error
		args, err = verificationRecoveryArgs(row)
		if err != nil {
			a.recordFailure(row, err)
			return
		}
	}
	if err := a.launch(ctx, row, args); err != nil {
		a.recordFailure(row, err)
		return
	}
	a.recordPending(row, actionRun, "starting…", "")
}

// ApprovalPrompt returns the confirmation text for an approval-gated row.
func (a *Actions) ApprovalPrompt(row monitor.Row) (string, bool) {
	if !actionableRow(row) || strings.TrimSpace(row.ApprovalSliceID) == "" {
		return "", false
	}
	reason := strings.TrimSpace(row.ApprovalReason)
	if reason == "" {
		reason = "approval required"
	}
	return fmt.Sprintf("Approve slice %s: %s?", row.ApprovalSliceID, reason), true
}

// ApproveSlice starts approval after the event loop's confirmation has resolved yes.
func (a *Actions) ApproveSlice(ctx context.Context, row monitor.Row) {
	if _, ok := a.ApprovalPrompt(row); !ok {
		return
	}
	args := []string{"approve", "--slice", row.ApprovalSliceID, row.PlanID}
	if err := a.launch(ctx, row, args); err != nil {
		a.recordFailure(row, err)
		return
	}
	a.recordPending(row, actionApprove, "starting…", row.ApprovalSliceID)
}

// MergePlanPrompt returns confirmation text only for a reviewed plan row.
// The merge command remains responsible for all detailed eligibility checks.
func (a *Actions) MergePlanPrompt(row monitor.Row) (string, bool) {
	if !actionableRow(row) || row.Status != plan.StatusReviewed {
		return "", false
	}
	return fmt.Sprintf("Merge reviewed plan %s in repository %s?", row.PlanID, repositoryLabel(row)), true
}

// MergePlan launches a confirmed single-plan merge.
func (a *Actions) MergePlan(ctx context.Context, row monitor.Row) {
	if _, ok := a.MergePlanPrompt(row); !ok {
		return
	}
	if err := a.launch(ctx, row, []string{"merge", row.PlanID}); err != nil {
		a.recordFailure(row, err)
		return
	}
	a.recordPending(row, actionMerge, "merging…", "")
}

// MergeAllPrompt returns confirmation text for a repository-scoped batch.
func (a *Actions) MergeAllPrompt(row monitor.Row) (string, bool) {
	if !actionableRow(row) {
		return "", false
	}
	return fmt.Sprintf("Merge all eligible plans in repository %s?", repositoryLabel(row)), true
}

// MergeAll launches a confirmed batch merge in exactly one repository root.
func (a *Actions) MergeAll(ctx context.Context, row monitor.Row) {
	if _, ok := a.MergeAllPrompt(row); !ok {
		return
	}
	if err := a.launch(ctx, row, []string{"merge", "--all"}); err != nil {
		a.recordFailure(row, err)
		return
	}
	a.recordPending(row, actionMerge, "merging…", "")
}

// Reconcile clears observed starts and turns unobserved pending actions into
// failed-to-start feedback after the bounded startup window. Merge feedback
// expires silently because a detached merge may legitimately run for longer.
func (a *Actions) Reconcile(snapshot monitor.Snapshot) {
	rows := make(map[string]monitor.Row, len(snapshot.Rows))
	for _, row := range snapshot.Rows {
		rows[actionRowKey(row)] = row
	}
	for key, feedback := range a.feedback {
		if feedback.kind == actionFailed {
			continue
		}
		row, found := rows[key]
		if found && a.observed(feedback, row) {
			delete(a.feedback, key)
			if a.messageKey == key {
				a.message = ""
				a.messageKey = ""
			}
			continue
		}
		if a.now().Sub(feedback.startedAt) < a.startupTimeout {
			continue
		}
		if feedback.kind == actionMerge {
			delete(a.feedback, key)
			continue
		}
		planID := row.PlanID
		if strings.TrimSpace(planID) == "" {
			planID = planIDFromActionKey(key)
		}
		a.feedback[key] = actionFeedback{kind: actionFailed, label: "failed to start", startedAt: feedback.startedAt}
		a.messageKey = key
		a.message = failedStartMessage(planID, nil)
	}
}

func (a *Actions) labels() map[string]string {
	if a == nil || len(a.feedback) == 0 {
		return nil
	}
	labels := make(map[string]string, len(a.feedback))
	for key, feedback := range a.feedback {
		labels[key] = feedback.label
	}
	return labels
}

func (a *Actions) statusMessage() string {
	if a == nil {
		return ""
	}
	return a.message
}

func (a *Actions) launch(ctx context.Context, row monitor.Row, args []string) error {
	request := CommandRequest{
		CWD:        row.RepositoryRoot,
		Executable: a.executable,
		Args:       append([]string(nil), args...),
		Detached:   true,
	}
	return a.launcher(ctx, request)
}

func (a *Actions) observed(feedback actionFeedback, row monitor.Row) bool {
	switch feedback.kind {
	case actionApprove:
		return row.ApprovalSliceID == "" || row.ApprovalSliceID != feedback.approvalSliceID
	case actionMerge:
		return row.Status != feedback.initialStatus
	default:
		return row.Liveness == monitor.LivenessLive || a.liveRunLock(row.PlanDir)
	}
}

func (a *Actions) liveRunLock(planDir string) bool {
	if strings.TrimSpace(planDir) == "" || a.readRunLock == nil {
		return false
	}
	lock, err := a.readRunLock(planDir)
	return err == nil && lock.ProcessAlive
}

func (a *Actions) recordPending(row monitor.Row, kind actionKind, label, approvalSliceID string) {
	key := actionRowKey(row)
	a.feedback[key] = actionFeedback{
		kind:            kind,
		label:           label,
		startedAt:       a.now(),
		approvalSliceID: approvalSliceID,
		initialStatus:   row.Status,
	}
	if a.messageKey == key {
		a.message = ""
		a.messageKey = ""
	}
}

func (a *Actions) recordFailure(row monitor.Row, err error) {
	key := actionRowKey(row)
	a.feedback[key] = actionFeedback{kind: actionFailed, label: "failed to start", startedAt: a.now()}
	a.messageKey = key
	a.message = failedStartMessage(row.PlanID, err)
}

func verificationRecoveryArgs(row monitor.Row) ([]string, error) {
	action := row.VerificationRecoveryAction
	command := strings.TrimSpace(action.Command)
	if command == "" {
		instruction := strings.TrimSpace(action.Instruction)
		if instruction == "" {
			instruction = "resolve the external verification failure before explicitly reverifying"
		}
		return nil, fmt.Errorf("verification recovery cannot be launched automatically: %s", instruction)
	}
	if action.Kind == plan.PlanActionRun && action.Command == "tao run "+row.PlanID {
		return []string{"run", row.PlanID}, nil
	}
	fields := strings.Fields(command)
	if len(fields) != 4 || fields[0] != "tao" || fields[1] != "run" || fields[3] != row.PlanID {
		return nil, fmt.Errorf("unsupported projected verification recovery command %q", command)
	}
	switch fields[2] {
	case "--repair-verification", "--reverify":
		return append([]string(nil), fields[1:]...), nil
	default:
		return nil, fmt.Errorf("unsupported projected verification recovery command %q", command)
	}
}

func repositoryLabel(row monitor.Row) string {
	if name := strings.TrimSpace(row.RepositoryName); name != "" {
		return name
	}
	return row.RepositoryID
}

func actionableRow(row monitor.Row) bool {
	return row.Kind != monitor.RowKindRepositoryWarning && row.Status != plan.StatusAbandoned && strings.TrimSpace(row.PlanID) != "" && strings.TrimSpace(row.RepositoryRoot) != ""
}

func actionRowKey(row monitor.Row) string {
	return row.RepositoryID + "\x00" + row.PlanID
}

func planIDFromActionKey(key string) string {
	_, planID, _ := strings.Cut(key, "\x00")
	return planID
}

func failedStartMessage(planID string, err error) string {
	message := fmt.Sprintf("Failed to start %s", planID)
	if err != nil {
		message += ": " + err.Error()
	} else {
		message += ": no activity observed"
	}
	return message + fmt.Sprintf("; inspect `tao log %s`.", planID)
}
