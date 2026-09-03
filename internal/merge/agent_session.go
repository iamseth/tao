package merge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/agent/logrecord"
	"github.com/iamseth/tao/internal/agentsession"
	"github.com/iamseth/tao/internal/commandrunner"
	commitcontract "github.com/iamseth/tao/internal/commit"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/runtimeconfig"
)

// BatchAgentSessionConfig configures a merge-owned agent operation. Zero-value
// provider, permission, and timeout settings use the ordinary TAO_* runtime
// environment defaults.
type BatchAgentSessionConfig struct {
	Agent           runtimeconfig.AgentKind
	ProcessStarter  agent.ProcessStarter
	SkipPermissions *bool
	Timeout         *time.Duration
	Log             io.Writer
	ControlRoot     string
	CommandRunner   commandrunner.Runner
	Metrics         func(agent.Metrics, string)
	Observe         func(BatchAgentSessionRequest, BatchAgentSessionResult, error)
	EventAppender   BatchAgentEventAppender
	Now             func() time.Time

	// ProviderLookPath and ConfinementProbe override preflight capability
	// checks. They are intended for tests; production callers leave them nil.
	ProviderLookPath agent.LookPath
	ConfinementProbe func() error
}

// BatchAgentEventAppender owns repository-scoped batch telemetry persistence.
type BatchAgentEventAppender interface {
	AppendAgentEvent(BatchAgentEvent) error
}

// BatchAgentOperation identifies the merge-batch operation that owns a provider call.
type BatchAgentOperation string

const (
	BatchAgentOperationCandidateResolution  BatchAgentOperation = "candidate_resolution"
	BatchAgentOperationSinglePlanResolution BatchAgentOperation = "single_plan_resolution"
	BatchAgentOperationSinglePlanReview     BatchAgentOperation = "single_plan_review"
	BatchAgentOperationAggregateReview      BatchAgentOperation = "aggregate_review"
	BatchAgentOperationAggregateRework      BatchAgentOperation = "aggregate_rework"
	BatchAgentOperationProposalGeneration   BatchAgentOperation = "proposal_generation"
)

// BatchAgentSessionRequest carries trusted call-site attribution and provider input.
type BatchAgentSessionRequest struct {
	BatchID         string
	Operation       BatchAgentOperation
	Attempt         int
	IntegrationRoot string
	Prompt          string
	CandidatePlanID string

	// ProtectedGitObjectRoot and ProtectedGitWritePaths are set only for
	// single-plan resolver and reviewer sessions. They activate the provider's
	// read-only host filesystem view and preserve stricter read-only submounts
	// for Git metadata beneath the resolver's writable integration worktree.
	ProtectedGitObjectRoot string
	ProtectedGitWritePaths []string
}

// BatchAgentSessionResult preserves the neutral provider result while exposing
// the final text selected for merge orchestration.
type BatchAgentSessionResult struct {
	Output   string
	Provider agentsession.Result
}

// BatchAgentSession is the provider-neutral session seam used by merge batches.
type BatchAgentSession struct {
	runner             agentsession.Runner
	run                func(context.Context, agentsession.Request) (agentsession.Result, error)
	confinesFilesystem bool
	log                io.Writer
	controlRoot        string
	metrics            func(agent.Metrics, string)
	observe            func(BatchAgentSessionRequest, BatchAgentSessionResult, error)
	eventAppender      BatchAgentEventAppender
	now                func() time.Time
	providerToolName   string
	providerLookPath   agent.LookPath
	confinementProbe   func() error
}

// SingleMergeAgentSessionConfig configures a plan-scoped merge session. The
// caller receives provider results directly and owns best-effort plan metrics;
// repository-scoped batch events are never written.
type SingleMergeAgentSessionConfig = BatchAgentSessionConfig

// NewSingleMergeAgentSession constructs a provider-neutral session for one
// single-plan operation. Each Resolve call starts a fresh provider process.
func NewSingleMergeAgentSession(config SingleMergeAgentSessionConfig) (BatchAgentSession, error) {
	config.EventAppender = nil
	return newBatchAgentSession(config, true)
}

// FreshSingleMergeAgentSession defers runtime configuration until an ordinary
// squash conflict actually needs an agent. Every operation gets a newly
// configured session and therefore a fresh provider process, with no retry.
type FreshSingleMergeAgentSession struct {
	config SingleMergeAgentSessionConfig
}

// NewFreshSingleMergeAgentSession constructs the deferred single-plan agent.
// Invalid runtime configuration cannot block a non-conflicting merge because it
// is resolved only by Resolve.
func NewFreshSingleMergeAgentSession(config SingleMergeAgentSessionConfig) FreshSingleMergeAgentSession {
	config.EventAppender = nil
	return FreshSingleMergeAgentSession{config: config}
}

// Preflight validates the fresh session configuration and proves the selected
// provider can start inside the OS confinement boundary without opening an
// interactive provider session.
func (s FreshSingleMergeAgentSession) Preflight(ctx context.Context, request BatchAgentSessionRequest) error {
	session, err := NewSingleMergeAgentSession(s.config)
	if err != nil {
		return fmt.Errorf("configure %s session: %w", request.Operation, err)
	}
	return session.Preflight(ctx, request)
}

// Resolve runs one fresh, attributed provider session.
func (s FreshSingleMergeAgentSession) Resolve(ctx context.Context, request BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
	if s.config.Log != nil {
		if message := singleMergeOperationStart(request.Operation); message != "" {
			_, _ = fmt.Fprintln(s.config.Log, message)
		}
	}
	session, err := NewSingleMergeAgentSession(s.config)
	if err != nil {
		return BatchAgentSessionResult{}, fmt.Errorf("configure %s session: %w", request.Operation, err)
	}
	return session.Resolve(ctx, request)
}

func singleMergeOperationStart(operation BatchAgentOperation) string {
	switch operation {
	case BatchAgentOperationSinglePlanResolution:
		return "Automatic squash conflict resolution started (attempt 1 of 1)."
	case BatchAgentOperationSinglePlanReview:
		return "Independent exact-integration review started in a fresh session (attempt 1 of 1)."
	default:
		return ""
	}
}

// SingleMergeAgentMetricsEvent projects usable provider metrics into the
// generic plan event format. The event is telemetry only; callers persist it
// best-effort and must never use it as merge or recovery authority.
func SingleMergeAgentMetricsEvent(request BatchAgentSessionRequest, result BatchAgentSessionResult, sessionErr error, timestamp time.Time) *plan.Event {
	if !result.Provider.MetricsUsable {
		return nil
	}
	metrics := result.Provider.Metrics
	projected := plan.AgentMetrics{Agent: result.Provider.AgentLabel, Status: plan.StatusCompleted, Result: plan.StatusCompleted}
	if metrics != nil {
		projected.SessionID = metrics.SessionID
		projected.ProviderID = metrics.ProviderID
		projected.ModelID = metrics.ModelID
		projected.InputTokens = metrics.InputTokens
		projected.OutputTokens = metrics.OutputTokens
		projected.ReasoningTokens = metrics.ReasoningTokens
		projected.CacheReadTokens = metrics.CacheReadTokens
		projected.CacheWriteTokens = metrics.CacheWriteTokens
		projected.TotalTokens = metrics.TotalTokens
		projected.Cost = metrics.Cost
		projected.TotalMessages = metrics.TotalMessages
		projected.UserMessages = metrics.UserMessages
		projected.AssistantMessages = metrics.AssistantMessages
		projected.ErroredMessages = metrics.ErroredMessages
		projected.ToolCalls = metrics.ToolCalls
	}
	if sessionErr != nil {
		projected.Status = "failed"
		projected.Result = "failed"
	}
	message := "Captured single-plan merge agent metrics"
	switch request.Operation {
	case BatchAgentOperationSinglePlanResolution:
		message = "Captured single-plan conflict resolver agent metrics"
	case BatchAgentOperationSinglePlanReview:
		message = "Captured independent integration reviewer agent metrics"
	}
	return &plan.Event{
		Type: plan.EventTypeAgentMetrics, Timestamp: timestamp.UTC(), PlanID: request.CandidatePlanID,
		Agent: projected.Agent, Metrics: &projected, Message: message,
	}
}

// MergeProposalGeneratorConfig configures the exceptional single-merge
// proposal session.
type MergeProposalGeneratorConfig = BatchAgentSessionConfig

// NewMergeProposalGenerator constructs a strict central proposal generator.
// Runtime configuration remains deferred until a proposal is requested, so
// review-backed merges do not configure or invoke a provider.
func NewMergeProposalGenerator(config MergeProposalGeneratorConfig) (commitcontract.Generator, error) {
	return commitcontract.Generator{Text: mergeProposalTextSession{config: config}}, nil
}

type mergeProposalTextSession struct {
	config MergeProposalGeneratorConfig
}

func (s mergeProposalTextSession) GenerateText(ctx context.Context, repoRoot, prompt string) (string, error) {
	session, err := NewBatchAgentSession(s.config)
	if err != nil {
		return "", fmt.Errorf("configure exceptional merge proposal session: %w", err)
	}
	identity, _ := ctx.Value(batchProposalSessionIdentityKey{}).(batchProposalSessionIdentity)
	if identity.Attempt == 0 {
		identity.Attempt = 1
	}
	result, err := session.Resolve(ctx, BatchAgentSessionRequest{
		BatchID: identity.BatchID, Operation: BatchAgentOperationProposalGeneration, Attempt: identity.Attempt,
		IntegrationRoot: repoRoot, Prompt: prompt, CandidatePlanID: identity.CandidatePlanID,
	})
	return result.Output, err
}

type batchProposalSessionIdentityKey struct{}

type batchProposalSessionIdentity struct {
	BatchID         string
	Attempt         int
	CandidatePlanID string
}

func withBatchProposalSessionIdentity(ctx context.Context, batchID string, attempt int, candidatePlanID string) context.Context {
	return context.WithValue(ctx, batchProposalSessionIdentityKey{}, batchProposalSessionIdentity{
		BatchID: batchID, Attempt: attempt, CandidatePlanID: candidatePlanID,
	})
}

// NewBatchAgentSession resolves merge runtime policy and constructs one bounded
// provider-neutral session adapter.
func NewBatchAgentSession(config BatchAgentSessionConfig) (BatchAgentSession, error) {
	return newBatchAgentSession(config, false)
}

func newBatchAgentSession(config BatchAgentSessionConfig, confineFilesystem bool) (BatchAgentSession, error) {
	defaults, err := runtimeconfig.RuntimeEnvDefaults()
	if err != nil {
		return BatchAgentSession{}, err
	}
	kind := config.Agent
	if kind == "" {
		kind = defaults.Agent
	}
	skip := defaults.SkipPermissions
	if config.SkipPermissions != nil {
		skip = *config.SkipPermissions
	}
	timeout := defaults.SessionTimeoutValue()
	if config.Timeout != nil {
		timeout = *config.Timeout
	}
	descriptor, ok := agent.Lookup(kind)
	if !ok {
		return BatchAgentSession{}, fmt.Errorf("unsupported agent %q", kind)
	}
	providerLookPath := config.ProviderLookPath
	if providerLookPath == nil {
		providerLookPath = exec.LookPath
	}
	starter := config.ProcessStarter
	if starter == nil {
		starter = agent.DefaultProcessStarter
	}
	if confineFilesystem {
		starter = singleMergeFilesystemConfiningProcessStarter(starter, providerLookPath)
	}
	runner := agentsession.New(agentsession.Config{
		Descriptor:      descriptor,
		Deps:            agent.RuntimeDeps{ProcessStarter: starter},
		SkipPermissions: skip,
		Timeout:         timeout,
		Progress:        config.Log,
		CommandRunner:   config.CommandRunner,
	})
	clock := config.Now
	if clock == nil {
		clock = time.Now
	}
	return BatchAgentSession{
		runner: runner, run: runner.Run, confinesFilesystem: confineFilesystem,
		log: config.Log, controlRoot: config.ControlRoot, metrics: config.Metrics,
		observe: config.Observe, eventAppender: config.EventAppender, now: clock,
		providerToolName: descriptor.ToolName, providerLookPath: providerLookPath,
		confinementProbe: config.ConfinementProbe,
	}, nil
}

type singleMergeFilesystemConfinementContextKey struct{}

type singleMergeFilesystemConfinement struct {
	protectedPaths  []string
	integrationRoot string
	allowEdits      bool
}

// singleMergeFilesystemConfiningProcessStarter gives the provider a read-only
// view of the host filesystem. A resolver may write only beneath the exact
// integration root; a reviewer cannot write there either. Provider scratch
// writes are redirected into one Tao-owned temporary directory that is removed
// with the process.
func singleMergeFilesystemConfiningProcessStarter(next agent.ProcessStarter, lookPath agent.LookPath) agent.ProcessStarter {
	return func(ctx context.Context, cwd, name string, args []string) (agent.Process, error) {
		resolvedName, err := resolveSingleMergeProviderExecutable(lookPath, name)
		if err != nil {
			return nil, err
		}
		policy, ok := ctx.Value(singleMergeFilesystemConfinementContextKey{}).(singleMergeFilesystemConfinement)
		if !ok {
			return next(ctx, cwd, name, args)
		}
		runtimeRoot, err := os.MkdirTemp("/tmp", "tao-merge-agent-runtime-*")
		if err != nil {
			return nil, fmt.Errorf("create provider confinement runtime: %w", err)
		}
		for _, dir := range []string{"cache", "state", "sessions"} {
			if err := os.Mkdir(filepath.Join(runtimeRoot, dir), 0o700); err != nil {
				_ = os.RemoveAll(runtimeRoot)
				return nil, fmt.Errorf("create provider confinement runtime: %w", err)
			}
		}
		confiner, confinedArgs, err := singleMergeFilesystemConfinementCommand(policy, runtimeRoot, resolvedName, args)
		if err != nil {
			_ = os.RemoveAll(runtimeRoot)
			return nil, err
		}
		process, err := next(ctx, cwd, confiner, confinedArgs)
		if err != nil {
			_ = os.RemoveAll(runtimeRoot)
			return nil, err
		}
		return &confinementCleanupProcess{Process: process, runtimeRoot: runtimeRoot}, nil
	}
}

type confinementCleanupProcess struct {
	agent.Process
	runtimeRoot string
	cleanupOnce sync.Once
}

func (p *confinementCleanupProcess) Wait() error {
	err := p.Process.Wait()
	p.cleanupOnce.Do(func() { _ = os.RemoveAll(p.runtimeRoot) })
	return err
}

func singleMergeFilesystemConfinementCommand(policy singleMergeFilesystemConfinement, runtimeRoot, name string, args []string) (string, []string, error) {
	protected, err := canonicalGitWritePaths(policy.protectedPaths)
	if err != nil {
		return "", nil, err
	}
	integrationRoot, err := canonicalConfinementDirectory(policy.integrationRoot, "integration worktree")
	if err != nil {
		return "", nil, err
	}
	if policy.allowEdits {
		if err := rejectMultiplyLinkedWorktreeFiles(context.Background(), integrationRoot, protected); err != nil {
			return "", nil, err
		}
	}
	runtimeRoot, err = canonicalConfinementDirectory(runtimeRoot, "provider runtime")
	if err != nil {
		return "", nil, err
	}
	if !policy.allowEdits && pathWithinConfinementRoot(runtimeRoot, integrationRoot) {
		return "", nil, errors.New("protect provider filesystem boundary: reviewer runtime overlaps integration worktree")
	}
	writable := []string{runtimeRoot}
	if policy.allowEdits {
		writable = append(writable, integrationRoot)
	}
	command := append([]string{
		"/usr/bin/env",
		"TMPDIR=" + runtimeRoot,
		"TMP=" + runtimeRoot,
		"TEMP=" + runtimeRoot,
		"XDG_CACHE_HOME=" + filepath.Join(runtimeRoot, "cache"),
		"XDG_STATE_HOME=" + filepath.Join(runtimeRoot, "state"),
		"PI_CODING_AGENT_SESSION_DIR=" + filepath.Join(runtimeRoot, "sessions"),
		name,
	}, args...)
	confiner, err := singleMergeFilesystemConfinementExecutable()
	if err != nil {
		return "", nil, err
	}
	switch runtime.GOOS {
	case "darwin":
		profile := darwinFilesystemConfinementProfile(protected, writable)
		return confiner, append([]string{"-p", profile, "--"}, command...), nil
	case "linux":
		// Keep the provider out of Tao's host PID namespace. Mounting /proc only
		// after entering the private namespace prevents access to Tao's process,
		// including unlinked rollback files that remain reachable through its FDs.
		confined := []string{"--die-with-parent", "--new-session", "--unshare-pid", "--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc"}
		for _, path := range writable {
			// Each writable bind is a distinct mount. Linux rejects a new hard
			// link between it and the read-only host mount with EXDEV; the
			// pre-launch link-count check above rejects aliases that predate it.
			confined = append(confined, "--bind", path, path)
		}
		// Apply protected submounts after the writable integration mount so its
		// .git indirection and every shared Git path remain read-only.
		for _, path := range protected {
			confined = append(confined, "--ro-bind", path, path)
		}
		confined = append(confined, "--")
		return confiner, append(confined, command...), nil
	default:
		return "", nil, fmt.Errorf("protect provider filesystem boundary: confinement is unsupported on %s", runtime.GOOS)
	}
}

func singleMergeFilesystemConfinementExecutable() (string, error) {
	switch runtime.GOOS {
	case "darwin":
		const sandboxExec = "/usr/bin/sandbox-exec"
		if err := requireConfinementExecutable(sandboxExec); err != nil {
			return "", err
		}
		return sandboxExec, nil
	case "linux":
		for _, candidate := range []string{"/usr/bin/bwrap", "/bin/bwrap"} {
			if requireConfinementExecutable(candidate) == nil {
				return candidate, nil
			}
		}
		return "", errors.New("protect provider filesystem boundary: bubblewrap is unavailable; install bwrap and run tao doctor")
	default:
		return "", fmt.Errorf("protect provider filesystem boundary: confinement is unsupported on %s", runtime.GOOS)
	}
}

func pathWithinConfinementRoot(path, root string) bool {
	if path == root {
		return true
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// rejectMultiplyLinkedWorktreeFiles closes the existing-hard-link side of the
// writable-bind boundary. Protected submounts are excluded because the provider
// cannot write through them. Symlinks are not followed.
func rejectMultiplyLinkedWorktreeFiles(ctx context.Context, root string, protected []string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return fmt.Errorf("protect provider filesystem boundary: inspect writable integration worktree: %w", walkErr)
		}
		for _, protectedPath := range protected {
			if pathWithinConfinementRoot(path, protectedPath) {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("protect provider filesystem boundary: inspect writable integration worktree: %w", err)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		links, err := regularFileLinkCount(info)
		if err != nil {
			return fmt.Errorf("protect provider filesystem boundary: inspect writable integration worktree link count: %w", err)
		}
		if links <= 1 {
			return nil
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			relative = path
		}
		return fmt.Errorf("protect provider filesystem boundary: writable integration worktree contains multiply linked regular file %q", filepath.ToSlash(relative))
	})
}

func canonicalConfinementDirectory(path, label string) (string, error) {
	canonical, err := canonicalGitProtectedPath(path)
	if err != nil {
		return "", fmt.Errorf("protect provider filesystem boundary: resolve %s: %w", label, err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", fmt.Errorf("protect provider filesystem boundary: inspect %s: %w", label, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("protect provider filesystem boundary: %s is not a directory", label)
	}
	return canonical, nil
}

func canonicalGitWritePaths(paths []string) ([]string, error) {
	canonical := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		resolved, err := canonicalGitProtectedPath(path)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[resolved]; ok {
			continue
		}
		seen[resolved] = struct{}{}
		canonical = append(canonical, resolved)
	}
	if len(canonical) == 0 {
		return nil, errors.New("protect Git write boundary: no protected paths")
	}
	slices.Sort(canonical)
	return canonical, nil
}

func canonicalGitObjectRoot(root string) (string, error) {
	canonical, err := canonicalGitProtectedPath(root)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", fmt.Errorf("protect Git object database: inspect path: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("protect Git object database: object root is not a directory")
	}
	return canonical, nil
}

func canonicalGitProtectedPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("protect Git write boundary: resolve path: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("protect Git write boundary: resolve symlinks: %w", err)
	}
	if _, err := os.Lstat(canonical); err != nil {
		return "", fmt.Errorf("protect Git write boundary: inspect path: %w", err)
	}
	return filepath.Clean(canonical), nil
}

func resolveSingleMergeProviderExecutable(lookPath agent.LookPath, name string) (string, error) {
	resolved, err := lookPath(name)
	if err != nil {
		return "", fmt.Errorf("resolve provider executable %q: %w", name, err)
	}
	if strings.TrimSpace(resolved) == "" {
		return "", fmt.Errorf("resolve provider executable %q: empty path", name)
	}
	absolute, err := filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("resolve provider executable %q path: %w", name, err)
	}
	return absolute, nil
}

const maxSingleMergeConfinementProbeOutputBytes = 1024

type singleMergeConfinementProbeOutput struct {
	mu       sync.Mutex
	retained []byte
}

// Write drains provider output while retaining only the bounded diagnostic
// prefix. Reporting the full write prevents an oversized version response from
// blocking the child after the diagnostic limit is reached.
func (w *singleMergeConfinementProbeOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	written := len(p)
	remaining := maxSingleMergeConfinementProbeOutputBytes - len(w.retained)
	if remaining > len(p) {
		remaining = len(p)
	}
	if remaining > 0 {
		w.retained = append(w.retained, p[:remaining]...)
	}
	return written, nil
}

func (w *singleMergeConfinementProbeOutput) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.retained)
}

func probeSingleMergeFilesystemConfinement(ctx context.Context, policy singleMergeFilesystemConfinement, providerExecutable string) error {
	runtimeRoot, err := os.MkdirTemp("/tmp", "tao-merge-confinement-probe-*")
	if err != nil {
		return fmt.Errorf("probe provider filesystem confinement: create runtime: %w", err)
	}
	defer func() { _ = os.RemoveAll(runtimeRoot) }()
	for _, dir := range []string{"cache", "state", "sessions"} {
		if err := os.Mkdir(filepath.Join(runtimeRoot, dir), 0o700); err != nil {
			return fmt.Errorf("probe provider filesystem confinement: create runtime: %w", err)
		}
	}
	// Preflight proves only that the selected executable can launch. It must not
	// inherit resolver edit authority before Tao persists requested evidence.
	probePolicy := policy
	probePolicy.allowEdits = false
	name, args, err := singleMergeFilesystemConfinementCommand(probePolicy, runtimeRoot, providerExecutable, []string{"--version"})
	if err != nil {
		return err
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	command := exec.CommandContext(probeCtx, name, args...) // #nosec G204,G702 -- Tao resolves the configured provider and runs only its fixed version probe through Tao's platform confiner.
	command.Dir = policy.integrationRoot
	var output singleMergeConfinementProbeOutput
	command.Stdout = &output
	command.Stderr = &output
	err = command.Run()
	if err == nil {
		return nil
	}
	detail := strings.TrimSpace(output.String())
	if detail != "" {
		return fmt.Errorf("probe provider filesystem confinement: %w: %s", err, detail)
	}
	return fmt.Errorf("probe provider filesystem confinement: %w", err)
}

func requireConfinementExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("protect Git object database: inspect confinement executable %s: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("protect Git object database: confinement executable %s is not executable", path)
	}
	return nil
}

func darwinFilesystemConfinementProfile(protected, writable []string) string {
	var profile strings.Builder
	profile.WriteString(`(version 1)(allow default)(deny file-write*)`)
	for _, path := range writable {
		profile.WriteString(`(allow file-write* (literal "` + sandboxProfileEscape(path) + `"))`)
		profile.WriteString(`(allow file-write* (subpath "` + sandboxProfileEscape(path) + `"))`)
	}
	profile.WriteString(darwinGitWriteDenyRules(protected))
	return profile.String()
}

func darwinGitWriteDenyRules(paths []string) string {
	var profile strings.Builder
	ancestors := make(map[string]struct{})
	for _, protected := range paths {
		profile.WriteString(`(deny file-write* (literal "` + sandboxProfileEscape(protected) + `"))`)
		profile.WriteString(`(deny file-write* (subpath "` + sandboxProfileEscape(protected) + `"))`)
		// A path-only rule can be evaded by renaming one of its ancestors. Deny
		// writes to each exact ancestor while allowing sibling worktree content.
		for path := filepath.Dir(protected); ; path = filepath.Dir(path) {
			ancestors[path] = struct{}{}
			if path == filepath.Dir(path) {
				break
			}
		}
	}
	ordered := make([]string, 0, len(ancestors))
	for path := range ancestors {
		ordered = append(ordered, path)
	}
	slices.Sort(ordered)
	for _, path := range ordered {
		profile.WriteString(`(deny file-write* (literal "` + sandboxProfileEscape(path) + `"))`)
	}
	return profile.String()
}

func sandboxProfileEscape(path string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(path)
}

// Preflight validates the single-plan filesystem boundary and its platform
// prerequisite without starting a provider process.
func (s BatchAgentSession) Preflight(ctx context.Context, request BatchAgentSessionRequest) error {
	_, err := s.confinementPolicy(ctx, request, true)
	return err
}

func (s BatchAgentSession) confinementPolicy(ctx context.Context, request BatchAgentSessionRequest, probe bool) (*singleMergeFilesystemConfinement, error) {
	protectedPaths := append([]string(nil), request.ProtectedGitWritePaths...)
	if request.ProtectedGitObjectRoot != "" {
		protectedPaths = append(protectedPaths, request.ProtectedGitObjectRoot)
	}
	if !s.confinesFilesystem {
		if len(protectedPaths) > 0 {
			return nil, errors.New("provider filesystem protection is unavailable for this agent session")
		}
		return nil, nil
	}
	allowEdits := request.Operation == BatchAgentOperationSinglePlanResolution
	if !allowEdits && request.Operation != BatchAgentOperationSinglePlanReview {
		return nil, fmt.Errorf("provider filesystem confinement is unsupported for operation %s", request.Operation)
	}
	if len(protectedPaths) == 0 {
		return nil, errors.New("protect provider filesystem boundary: single-plan session has no protected Git paths")
	}
	paths, err := canonicalGitWritePaths(protectedPaths)
	if err != nil {
		return nil, err
	}
	integrationRoot, err := canonicalConfinementDirectory(request.IntegrationRoot, "integration worktree")
	if err != nil {
		return nil, err
	}
	if allowEdits {
		if err := rejectMultiplyLinkedWorktreeFiles(ctx, integrationRoot, paths); err != nil {
			return nil, err
		}
	}
	policy := &singleMergeFilesystemConfinement{protectedPaths: paths, integrationRoot: integrationRoot, allowEdits: allowEdits}
	if probe {
		providerExecutable, err := resolveSingleMergeProviderExecutable(s.providerLookPath, s.providerToolName)
		if err != nil {
			return nil, err
		}
		if s.confinementProbe != nil {
			if err := s.confinementProbe(); err != nil {
				return nil, err
			}
		} else if err := probeSingleMergeFilesystemConfinement(ctx, *policy, providerExecutable); err != nil {
			return nil, err
		}
	}
	return policy, nil
}

// Resolve runs exactly one attributed session. Metrics parse failures are
// warnings and never replace the provider result or error.
func (s BatchAgentSession) Resolve(ctx context.Context, request BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
	policy, err := s.confinementPolicy(ctx, request, true)
	if err != nil {
		return BatchAgentSessionResult{}, err
	}
	if policy != nil {
		ctx = context.WithValue(ctx, singleMergeFilesystemConfinementContextKey{}, *policy)
	}
	run := s.run
	if run == nil {
		run = s.runner.Run
	}
	result, err := run(ctx, agentsession.Request{
		RepoRoot: request.IntegrationRoot, ControlRoot: s.controlRoot, Prompt: request.Prompt, CollectMetrics: true,
	})
	if s.metrics != nil {
		metrics := agent.Metrics{}
		if result.Metrics != nil {
			metrics = *result.Metrics
		}
		s.metrics(metrics, result.MetricsWarning)
	} else if result.ReportMetricsWarning && s.log != nil {
		_ = logrecord.Render(s.log, logrecord.Record{Type: logrecord.TypeDiagnostic, Content: "tao telemetry warning: " + result.MetricsWarningMessage})
	}
	text := strings.TrimSpace(result.FinalText)
	if text == "" {
		text = strings.TrimSpace(result.Output)
	}
	sessionResult := BatchAgentSessionResult{Output: text, Provider: result}
	s.recordTelemetry(request, result, err)
	if s.observe != nil {
		s.observe(request, sessionResult, err)
	}
	return sessionResult, err
}

func (s BatchAgentSession) recordTelemetry(request BatchAgentSessionRequest, result agentsession.Result, sessionErr error) {
	if s.eventAppender == nil || request.BatchID == "" {
		return
	}
	outcome := BatchAgentOutcomeCompleted
	if sessionErr != nil {
		outcome = BatchAgentOutcomeFailed
	}
	var timeoutErr *agent.SessionTimeoutError
	if errors.As(sessionErr, &timeoutErr) {
		outcome = BatchAgentOutcomeTimedOut
		durationSeconds := int64(timeoutErr.Timeout / time.Second)
		if durationSeconds < 1 {
			durationSeconds = 1
		}
		event := BatchAgentEvent{
			Schema: BatchAgentEventSchema, Type: BatchAgentEventTypeTimeout, BatchID: request.BatchID,
			Timestamp: s.now().UTC(), Operation: request.Operation, Attempt: request.Attempt,
			Agent: result.AgentLabel, PlanID: request.CandidatePlanID, Outcome: outcome,
			TimeoutDurationSeconds: &durationSeconds,
		}
		s.appendTelemetry(event, "session timeout")
	}
	if result.MetricsUsable {
		event := BatchAgentEvent{
			Schema: BatchAgentEventSchema, Type: BatchAgentEventTypeMetrics, BatchID: request.BatchID,
			Timestamp: s.now().UTC(), Operation: request.Operation, Attempt: request.Attempt,
			Agent: result.AgentLabel, PlanID: request.CandidatePlanID, Outcome: outcome,
			Metrics: newBatchAgentMetrics(result.Metrics),
		}
		s.appendTelemetry(event, "metrics")
	}
}

func (s BatchAgentSession) appendTelemetry(event BatchAgentEvent, label string) {
	if err := s.eventAppender.AppendAgentEvent(event); err != nil && s.log != nil {
		_ = logrecord.Render(s.log, logrecord.Record{Type: logrecord.TypeDiagnostic, Content: fmt.Sprintf("tao telemetry warning: append merge-batch %s event: %v", label, err)})
	}
}
