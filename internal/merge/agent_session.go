package merge

import (
	"context"
	"encoding/json"
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
	piagent "github.com/iamseth/tao/internal/agent/pi"
	"github.com/iamseth/tao/internal/agentsession"
	"github.com/iamseth/tao/internal/agenttelemetry"
	"github.com/iamseth/tao/internal/commandrunner"
	commitcontract "github.com/iamseth/tao/internal/commit"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/internal/workspace"
)

// BatchAgentSessionConfig configures a merge-owned agent operation. Zero-value
// provider, permission, and timeout settings use RuntimeEnv (built-ins when
// omitted). Models must already be resolved by the caller unless ResolveOptions
// supplies the shared applicable projection.
type BatchAgentSessionConfig struct {
	// ResolveOptions optionally supplies the invocation's shared, lazily admitted
	// merge projection. Direct domain callers retain the explicit-field defaults.
	ResolveOptions  func() (runtimeconfig.CommandOptions, error)
	RuntimeEnv      *runtimeconfig.EnvSnapshot
	Agent           runtimeconfig.AgentKind
	Models          runtimeconfig.ModelSelection
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

	// FramedLog sends raw records to Log instead of pre-rendered progress.
	// Such writers own terminal presentation as well as transcript persistence.
	FramedLog bool

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

// Agent operations attribute merge provider calls to their trusted session context.
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
	// ReasoningEffort is the Tao-owned request value, not provider output.
	ReasoningEffort string
}

// BatchAgentSession is the provider-neutral session seam used by merge batches.
type BatchAgentSession struct {
	deferred           *BatchAgentSessionConfig
	runner             agentsession.Runner
	models             runtimeconfig.ModelSelection
	run                func(context.Context, agentsession.Request) (agentsession.Result, error)
	confinesFilesystem bool
	log                io.Writer
	framedLog          io.Writer
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

// SingleMergeAgentMetricsEvent projects invoked provider measurements into the
// generic plan event format. The event is telemetry only; callers persist it
// best-effort and must never use it as merge or recovery authority.
func SingleMergeAgentMetricsEvent(request BatchAgentSessionRequest, result BatchAgentSessionResult, sessionErr error, timestamp time.Time) *plan.Event {
	if !result.Provider.Invoked || request.BatchID != "" || request.CandidatePlanID == "" {
		return nil
	}
	role := plan.AgentRoleMerge
	var message string
	switch request.Operation {
	case BatchAgentOperationSinglePlanResolution:
		message = "Captured single-plan conflict resolver agent metrics"
	case BatchAgentOperationSinglePlanReview:
		role = plan.AgentRoleReview
		message = "Captured independent integration reviewer agent metrics"
	case BatchAgentOperationProposalGeneration:
		message = "Captured single-plan merge proposal agent metrics"
	default:
		return nil
	}
	event := agenttelemetry.Event(request.CandidatePlanID, "", timestamp, agenttelemetry.Project(result.Provider, role, result.ReasoningEffort, sessionErr))
	event.Message = message
	return &event
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

// NewDeferredBatchAgentSession keeps unused provider settings from blocking
// batch discovery or settlement. Resolve validates before starting a provider.
func NewDeferredBatchAgentSession(config BatchAgentSessionConfig) BatchAgentSession {
	return BatchAgentSession{deferred: &config}
}

func newBatchAgentSession(config BatchAgentSessionConfig, confineFilesystem bool) (BatchAgentSession, error) {
	if config.ResolveOptions != nil {
		options, err := config.ResolveOptions()
		if err != nil {
			return BatchAgentSession{}, err
		}
		config.Agent = options.RunOptions.Agent
		config.Models = options.RunOptions.Models
		config.SkipPermissions = &options.SkipPermissions
		config.Timeout = &options.RunOptions.SessionTimeout
	}
	var snapshot runtimeconfig.EnvSnapshot
	if config.RuntimeEnv != nil {
		snapshot = *config.RuntimeEnv
	}
	if config.ResolveOptions == nil {
		if err := snapshot.Require(runtimeconfig.EnvAgent, runtimeconfig.EnvSessionTimeout, runtimeconfig.EnvSkipPermissions); err != nil {
			return BatchAgentSession{}, err
		}
	}
	defaults := snapshot.Defaults()
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
	progress := config.Log
	var framedLog io.Writer
	if config.FramedLog {
		progress, framedLog = nil, config.Log
	}
	runner := agentsession.New(agentsession.Config{
		Descriptor:      descriptor,
		Deps:            agent.RuntimeDeps{ProcessStarter: starter},
		SkipPermissions: skip,
		Timeout:         timeout,
		Progress:        progress,
		CommandRunner:   config.CommandRunner,
	})
	clock := config.Now
	if clock == nil {
		clock = time.Now
	}
	return BatchAgentSession{
		runner: runner, run: runner.Run, confinesFilesystem: confineFilesystem, models: config.Models,
		log: config.Log, framedLog: framedLog, controlRoot: config.ControlRoot, metrics: config.Metrics,
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
// integration root; a reviewer cannot write there either. The launch builder
// owns executable resolution, the private runtime projection, sandbox command,
// and cleanup for both readiness and attributed processes.
func singleMergeFilesystemConfiningProcessStarter(next agent.ProcessStarter, lookPath agent.LookPath) agent.ProcessStarter {
	builder := singleMergeLaunchSpecBuilder{lookPath: lookPath}
	return func(ctx context.Context, cwd, name string, args []string) (agent.Process, error) {
		policy, ok := ctx.Value(singleMergeFilesystemConfinementContextKey{}).(singleMergeFilesystemConfinement)
		if !ok {
			return next(ctx, cwd, name, args)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		spec, err := builder.build(policy, cwd, name, args)
		if err != nil {
			return nil, err
		}
		process, err := next(ctx, spec.cwd, spec.name, spec.args)
		if err != nil {
			_ = spec.runtime.cleanup()
			return nil, err
		}
		return newConfinementCleanupProcess(process, spec.runtime), nil
	}
}

type singleMergeLaunchSpecBuilder struct {
	lookPath agent.LookPath
}

type singleMergeLaunchSpec struct {
	cwd     string
	name    string
	args    []string
	runtime *singleMergeInvocationRuntime
}

func (b singleMergeLaunchSpecBuilder) build(policy singleMergeFilesystemConfinement, cwd, providerName string, args []string) (*singleMergeLaunchSpec, error) {
	resolvedName, err := resolveSingleMergeProviderExecutable(b.lookPath, providerName)
	if err != nil {
		return nil, err
	}
	runtime, err := newSingleMergeInvocationRuntime(providerName)
	if err != nil {
		return nil, err
	}
	confiner, confinedArgs, err := singleMergeFilesystemConfinementCommandForProvider(policy, runtime.root, resolvedName, args, providerName == "pi")
	if err != nil {
		_ = runtime.cleanup()
		return nil, err
	}
	canonicalCWD, err := canonicalConfinementDirectory(cwd, "provider working directory")
	if err != nil {
		_ = runtime.cleanup()
		return nil, err
	}
	return &singleMergeLaunchSpec{cwd: canonicalCWD, name: confiner, args: confinedArgs, runtime: runtime}, nil
}

const maxSingleMergePiConfigBytes = 4 << 20

var singleMergePiMutableInputs = []string{
	"settings.json", "auth.json", "models.json", "models-store.json", "trust.json", "trusted-folders.json",
}

var singleMergePiResourceInputs = []string{"extensions", "npm", "prompts", "skills", "themes", "bin"}

type singleMergeInvocationRuntime struct {
	root        string
	cleanupOnce sync.Once
	cleanupErr  error
}

func newSingleMergeInvocationRuntime(providerName string) (*singleMergeInvocationRuntime, error) {
	root, err := os.MkdirTemp("/tmp", "tao-merge-agent-runtime-*")
	if err != nil {
		return nil, fmt.Errorf("create provider confinement runtime: %w", err)
	}
	if err := os.Chmod(root, 0o700); err != nil { //nolint:gosec // private directories require owner traversal as well as read/write.
		_ = os.RemoveAll(root)
		return nil, fmt.Errorf("create provider confinement runtime: set private mode: %w", err)
	}
	runtime := &singleMergeInvocationRuntime{root: root}
	for _, relative := range []string{"agent", "cache", "config", "data", "home", "sessions", "state", "tmp"} {
		if err := os.Mkdir(filepath.Join(root, relative), 0o700); err != nil {
			_ = runtime.cleanup()
			return nil, fmt.Errorf("create provider confinement runtime: %w", err)
		}
	}
	if providerName == "pi" {
		if err := materializeSingleMergePiView(filepath.Join(root, "agent")); err != nil {
			_ = runtime.cleanup()
			return nil, err
		}
	}
	return runtime, nil
}

func (r *singleMergeInvocationRuntime) path() string { return r.root }

func (r *singleMergeInvocationRuntime) cleanup() error {
	if r == nil {
		return nil
	}
	r.cleanupOnce.Do(func() { r.cleanupErr = os.RemoveAll(r.path()) })
	return r.cleanupErr
}

func materializeSingleMergePiView(destination string) error {
	hostRoot, err := hostPiAgentDirectory()
	if err != nil {
		return err
	}
	if hostRoot == "" {
		return nil
	}
	for _, name := range singleMergePiMutableInputs {
		if err := copySingleMergePiInput(hostRoot, destination, name); err != nil {
			return err
		}
	}
	for _, name := range singleMergePiResourceInputs {
		if err := linkSingleMergePiResource(hostRoot, destination, name); err != nil {
			return err
		}
	}
	return nil
}

func hostPiAgentDirectory() (string, error) {
	root := strings.TrimSpace(os.Getenv("PI_CODING_AGENT_DIR"))
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", safeSingleMergePiConfigError("resolve", "home", err)
		}
		root = filepath.Join(home, ".pi", "agent")
	}
	info, err := os.Stat(root) //nolint:gosec // this is the user-selected Pi agent directory, not provider output.
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", safeSingleMergePiConfigError("inspect", "agent directory", err)
	}
	if !info.IsDir() {
		return "", errors.New("prepare private Pi configuration: agent directory is not a directory")
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", safeSingleMergePiConfigError("resolve", "agent directory", err)
	}
	return canonical, nil
}

func copySingleMergePiInput(hostRoot, destination, label string) error {
	source := filepath.Join(hostRoot, label)
	info, err := os.Lstat(source)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return safeSingleMergePiConfigError("inspect", label, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("prepare private Pi configuration: %s is not a regular file", label)
	}
	if info.Size() > maxSingleMergePiConfigBytes {
		return fmt.Errorf("prepare private Pi configuration: %s exceeds %d bytes", label, maxSingleMergePiConfigBytes)
	}
	input, err := os.Open(source) //nolint:gosec // source is one allowlisted file below the canonical Pi agent directory.
	if err != nil {
		return safeSingleMergePiConfigError("open", label, err)
	}
	defer func() { _ = input.Close() }()
	openedInfo, err := input.Stat()
	if err != nil {
		return safeSingleMergePiConfigError("inspect opened", label, err)
	}
	if !os.SameFile(info, openedInfo) || !openedInfo.Mode().IsRegular() {
		return fmt.Errorf("prepare private Pi configuration: %s changed while opening", label)
	}
	contents, err := io.ReadAll(io.LimitReader(input, maxSingleMergePiConfigBytes+1))
	if err != nil {
		return safeSingleMergePiConfigError("copy", label, err)
	}
	if len(contents) > maxSingleMergePiConfigBytes {
		return fmt.Errorf("prepare private Pi configuration: %s exceeds %d bytes", label, maxSingleMergePiConfigBytes)
	}
	if !json.Valid(contents) {
		return fmt.Errorf("prepare private Pi configuration: %s is malformed JSON", label)
	}
	if err := os.WriteFile(filepath.Join(destination, label), contents, 0o600); err != nil {
		return safeSingleMergePiConfigError("create projection for", label, err)
	}
	return nil
}

func linkSingleMergePiResource(hostRoot, destination, label string) error {
	source := filepath.Join(hostRoot, label)
	info, err := os.Stat(source)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return safeSingleMergePiConfigError("inspect", label, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("prepare private Pi configuration: %s is not a directory", label)
	}
	canonical, err := filepath.EvalSymlinks(source)
	if err != nil {
		return safeSingleMergePiConfigError("resolve", label, err)
	}
	if err := os.Symlink(canonical, filepath.Join(destination, label)); err != nil {
		return safeSingleMergePiConfigError("expose read-only", label, err)
	}
	return nil
}

func safeSingleMergePiConfigError(action, label string, err error) error {
	detail := "unavailable"
	if errors.Is(err, os.ErrPermission) {
		detail = "permission denied"
	} else if errors.Is(err, os.ErrExist) {
		detail = "already exists"
	}
	return fmt.Errorf("prepare private Pi configuration: %s %s: %s", action, label, detail)
}

type confinementCleanupProcess struct {
	process    agent.Process
	runtime    *singleMergeInvocationRuntime
	waitOnce   sync.Once
	killOnce   sync.Once
	waitDone   chan struct{}
	waitErr    error
	killErr    error
	cleanupErr error
}

func newConfinementCleanupProcess(process agent.Process, runtime *singleMergeInvocationRuntime) *confinementCleanupProcess {
	return &confinementCleanupProcess{process: process, runtime: runtime, waitDone: make(chan struct{})}
}

func (p *confinementCleanupProcess) Stdin() io.WriteCloser { return p.process.Stdin() }
func (p *confinementCleanupProcess) Stdout() io.Reader     { return p.process.Stdout() }
func (p *confinementCleanupProcess) Stderr() io.Reader     { return p.process.Stderr() }

func (p *confinementCleanupProcess) Wait() error {
	p.waitOnce.Do(func() {
		p.waitErr = p.process.Wait()
		p.cleanupErr = p.runtime.cleanup()
		if p.cleanupErr != nil {
			p.waitErr = errors.Join(p.waitErr, fmt.Errorf("remove provider confinement runtime: %w", p.cleanupErr))
		}
		close(p.waitDone)
	})
	<-p.waitDone
	return p.waitErr
}

func (p *confinementCleanupProcess) Kill() error {
	p.killOnce.Do(func() { p.killErr = p.process.Kill() })
	_ = p.Wait()
	if p.cleanupErr != nil {
		return errors.Join(p.killErr, fmt.Errorf("remove provider confinement runtime: %w", p.cleanupErr))
	}
	return p.killErr
}

func singleMergeFilesystemConfinementCommand(policy singleMergeFilesystemConfinement, runtimeRoot, name string, args []string) (string, []string, error) {
	return singleMergeFilesystemConfinementCommandForProvider(policy, runtimeRoot, name, args, filepath.Base(name) == "pi")
}

func singleMergeFilesystemConfinementCommandForProvider(policy singleMergeFilesystemConfinement, runtimeRoot, name string, args []string, piProvider bool) (string, []string, error) {
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
	if !policy.allowEdits && workspace.PathWithinRoot(integrationRoot, runtimeRoot) {
		return "", nil, errors.New("protect provider filesystem boundary: reviewer runtime overlaps integration worktree")
	}
	writable := []string{runtimeRoot}
	if policy.allowEdits {
		writable = append(writable, integrationRoot)
	}
	environment := []string{
		"/usr/bin/env",
		"TMPDIR=" + runtimeRoot,
		"TMP=" + runtimeRoot,
		"TEMP=" + runtimeRoot,
		"XDG_CACHE_HOME=" + filepath.Join(runtimeRoot, "cache"),
		"XDG_STATE_HOME=" + filepath.Join(runtimeRoot, "state"),
		"PI_CODING_AGENT_SESSION_DIR=" + filepath.Join(runtimeRoot, "sessions"),
	}
	if piProvider {
		environment = append(environment,
			"HOME="+filepath.Join(runtimeRoot, "home"),
			"XDG_CONFIG_HOME="+filepath.Join(runtimeRoot, "config"),
			"XDG_DATA_HOME="+filepath.Join(runtimeRoot, "data"),
			"PI_CODING_AGENT_DIR="+filepath.Join(runtimeRoot, "agent"),
			"PI_OFFLINE=1",
			"PI_NO_UPDATE_CHECK=1",
			"PI_SKIP_VERSION_CHECK=1",
			"NO_UPDATE_NOTIFIER=1",
			"NPM_CONFIG_UPDATE_NOTIFIER=false",
			"NPM_CONFIG_OFFLINE=true",
		)
	}
	command := make([]string, 0, len(environment)+1+len(args))
	command = append(command, environment...)
	command = append(command, name)
	command = append(command, args...)
	confiner, err := singleMergeConfinementExecutable()
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

// singleMergeConfinementExecutable is a test-only discovery seam, package-private
// by design so callers cannot conjure the sandbox away. It affects both the probe
// launch spec and the attributed launch through their shared command constructor.
var singleMergeConfinementExecutable = singleMergeFilesystemConfinementExecutable

// singleMergeReadOnlyGitProbe is a package-private test-only seam; tests using a
// stubbed fake confiner must also stub this probe.
var singleMergeReadOnlyGitProbe = probeSingleMergeReadOnlyGit

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
			if workspace.PathWithinRoot(protectedPath, path) {
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

type boundedSingleMergeProbeError struct {
	cause error
}

func (e boundedSingleMergeProbeError) Error() string {
	const prefix = "probe provider filesystem confinement: "
	detail := e.cause.Error()
	limit := maxSingleMergeConfinementProbeOutputBytes - len(prefix)
	if len(detail) > limit {
		detail = detail[:limit]
	}
	return prefix + detail
}

func (e boundedSingleMergeProbeError) Unwrap() error { return e.cause }

// ProbeSingleMergePiReadiness passively exercises the production confinement,
// ephemeral configuration projection, RPC initialization, selected-model, and
// local-credential readiness path. It sends no prompt and therefore makes no
// model request. The temporary roots and invocation runtime are always removed.
func ProbeSingleMergePiReadiness(ctx context.Context, providerExecutable string) error {
	integrationRoot, err := os.MkdirTemp("", "tao-doctor-pi-worktree-*")
	if err != nil {
		return fmt.Errorf("prepare Pi readiness worktree: %w", err)
	}
	defer func() { _ = os.RemoveAll(integrationRoot) }()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return fmt.Errorf("resolve git for Pi readiness worktree: %w", err)
	}
	command := exec.CommandContext(ctx, gitPath, "init", "-q", "-b", "main") // #nosec G204,G702 -- PATH-resolved Git initializes only the disposable readiness repository.
	command.Dir = integrationRoot
	if err := command.Run(); err != nil {
		return fmt.Errorf("initialize Pi readiness worktree: %w", err)
	}
	protectedRoot, err := os.MkdirTemp("", "tao-doctor-pi-protected-*")
	if err != nil {
		return fmt.Errorf("prepare Pi readiness protected path: %w", err)
	}
	defer func() { _ = os.RemoveAll(protectedRoot) }()
	policy := singleMergeFilesystemConfinement{
		protectedPaths: []string{protectedRoot}, integrationRoot: integrationRoot,
	}
	if err := singleMergeReadOnlyGitProbe(ctx, policy); err != nil {
		return err
	}
	return probeSingleMergePiRPCReadiness(ctx, policy, providerExecutable, "", "")
}

// SingleMergeStartupCapabilityForError maps a bounded launch diagnostic to its
// stable capability name for doctor and merge rendering.
func SingleMergeStartupCapabilityForError(err error) plan.SingleMergeStartupCapability {
	if err == nil {
		return ""
	}
	return startupCapability(err)
}

func probeSingleMergePiRPCReadiness(ctx context.Context, policy singleMergeFilesystemConfinement, providerExecutable, model, effort string) error {
	// Readiness gets the same fresh projection and generated sandbox as the
	// attributed process, but never receives integration-worktree write access.
	probePolicy := policy
	probePolicy.allowEdits = false
	starter := singleMergeFilesystemConfiningProcessStarter(agent.DefaultProcessStarter, func(string) (string, error) {
		return providerExecutable, nil
	})
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	probeCtx = context.WithValue(probeCtx, singleMergeFilesystemConfinementContextKey{}, probePolicy)
	if err := (piagent.Client{ProcessStarter: starter}).CheckReadiness(probeCtx, policy.integrationRoot, model, effort); err != nil {
		return boundedSingleMergeProbeError{cause: err}
	}
	return nil
}

const singleMergeFilesystemProbeTimeout = 5 * time.Second

// Probe Git itself because provider startup does not detect sandbox profile
// regressions that deny Git's read-only startup (including opening /dev/null).
func probeSingleMergeReadOnlyGit(ctx context.Context, policy singleMergeFilesystemConfinement) error {
	probePolicy := policy
	probePolicy.allowEdits = false
	spec, err := (singleMergeLaunchSpecBuilder{lookPath: exec.LookPath}).build(probePolicy, policy.integrationRoot, "git", []string{"rev-parse", "--git-dir"})
	if err == nil {
		err = executeSingleMergeFilesystemProbe(ctx, spec, singleMergeFilesystemProbeTimeout, nil)
	}
	if err != nil {
		return fmt.Errorf("read-only git inside the provider sandbox confinement failed: %w", err)
	}
	return nil
}

func probeSingleMergeFilesystemConfinement(ctx context.Context, policy singleMergeFilesystemConfinement, providerExecutable string) error {
	return probeSingleMergeFilesystemConfinementWithTimeout(ctx, policy, providerExecutable, singleMergeFilesystemProbeTimeout)
}

func probeSingleMergeFilesystemConfinementWithTimeout(ctx context.Context, policy singleMergeFilesystemConfinement, providerExecutable string, timeout time.Duration) error {
	// Non-Pi providers retain a no-session version launch. Pi uses the RPC
	// readiness path above because --version cannot exercise configuration locks
	// or selected-model loading.
	probePolicy := policy
	probePolicy.allowEdits = false
	providerName := filepath.Base(providerExecutable)
	builder := singleMergeLaunchSpecBuilder{lookPath: func(string) (string, error) { return providerExecutable, nil }}
	spec, err := builder.build(probePolicy, policy.integrationRoot, providerName, []string{"--version"})
	if err != nil {
		return err
	}
	return executeSingleMergeFilesystemProbe(ctx, spec, timeout, nil)
}

// observe is a per-call test seam for inspecting the effective deadline without
// waiting for the probe budget to elapse. Execution and cleanup remain identical.
func executeSingleMergeFilesystemProbe(ctx context.Context, spec *singleMergeLaunchSpec, timeout time.Duration, observe func(context.Context)) error {
	defer func() { _ = spec.runtime.cleanup() }()
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if observe != nil {
		observe(probeCtx)
	}
	command := exec.CommandContext(probeCtx, spec.name, spec.args...) // #nosec G204,G702 -- Tao resolves the configured provider and runs only a fixed read-only probe through Tao's platform confiner.
	command.Dir = spec.cwd
	var output singleMergeConfinementProbeOutput
	command.Stdout = &output
	command.Stderr = &output
	err := command.Run()
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
	profile.WriteString(darwinDeviceWriteAllowRules())
	for _, path := range writable {
		profile.WriteString(`(allow file-write* (literal "` + sandboxProfileEscape(path) + `"))`)
		profile.WriteString(`(allow file-write* (subpath "` + sandboxProfileEscape(path) + `"))`)
	}
	profile.WriteString(darwinGitWriteDenyRules(protected))
	return profile.String()
}

// darwinDeviceWriteAllowRules restores the pseudo-device writes that a blanket
// file-write denial removes. Git opens /dev/null read-write on every startup
// and shells redirect to /dev/null, /dev/stderr, and /dev/fd/N constantly, so
// without these a confined resolver or reviewer cannot run a single Git
// command. The set mirrors the private /dev that bubblewrap provides on Linux
// and grants nothing on the host filesystem.
func darwinDeviceWriteAllowRules() string {
	var profile strings.Builder
	for _, device := range []string{"/dev/null", "/dev/zero", "/dev/full", "/dev/random", "/dev/urandom", "/dev/tty"} {
		profile.WriteString(`(allow file-write* (literal "` + device + `"))`)
	}
	profile.WriteString(`(allow file-write* (regex #"^/dev/fd/[0-9]+$"))`)
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

// Preflight validates the single-plan filesystem boundary and provider readiness
// without sending a model prompt or consuming resolution authority.
func (s BatchAgentSession) Preflight(ctx context.Context, request BatchAgentSessionRequest) error {
	if s.deferred != nil {
		session, err := NewBatchAgentSession(*s.deferred)
		if err != nil {
			return err
		}
		return session.Preflight(ctx, request)
	}
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
		if s.confinementProbe == nil {
			if err := singleMergeReadOnlyGitProbe(ctx, *policy); err != nil {
				return nil, err
			}
		}
		if s.confinementProbe != nil {
			if err := s.confinementProbe(); err != nil {
				return nil, err
			}
		} else if s.providerToolName == "pi" {
			// Resolution requires a subsequent integration review. Check both
			// effective selectors before consuming the one-shot resolution authority.
			type selection struct{ model, effort string }
			selections := []selection{
				{s.models.For(runtimeconfig.ModelRoleResolver), s.models.EffortFor(runtimeconfig.ModelRoleResolver)},
				{s.models.For(runtimeconfig.ModelRoleMergeReview), s.models.EffortFor(runtimeconfig.ModelRoleMergeReview)},
			}
			for _, selected := range slices.Compact(selections) {
				if err := probeSingleMergePiRPCReadiness(ctx, *policy, providerExecutable, selected.model, selected.effort); err != nil {
					return nil, err
				}
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
	if s.deferred != nil {
		session, err := NewBatchAgentSession(*s.deferred)
		if err != nil {
			return BatchAgentSessionResult{}, err
		}
		return session.Resolve(ctx, request)
	}
	// Guarded single-plan callers run the disposable readiness preflight before
	// recording request authority. The attributed process performs its own RPC
	// readiness handshake before prompt transmission, so probing again here
	// would add an unattributed process after durable request evidence. Explicit
	// test probes retain the historical direct-Resolve capability check.
	policy, err := s.confinementPolicy(ctx, request, s.confinementProbe != nil)
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
	role := runtimeconfig.ModelRoleResolver
	switch request.Operation {
	case BatchAgentOperationAggregateReview, BatchAgentOperationSinglePlanReview:
		role = runtimeconfig.ModelRoleMergeReview
	case BatchAgentOperationProposalGeneration:
		role = runtimeconfig.ModelRoleDefault
	}
	result, err := run(ctx, agentsession.Request{
		Model:    s.models.For(role),
		Effort:   s.models.EffortFor(role),
		RepoRoot: request.IntegrationRoot, ControlRoot: s.controlRoot, Prompt: request.Prompt, CollectMetrics: true, Log: s.framedLog,
	})
	summary := agentsession.Summarize(result, err)
	if s.metrics != nil {
		metrics := agent.Metrics{}
		if result.Metrics != nil {
			metrics = *result.Metrics
		}
		s.metrics(metrics, result.MetricsWarning)
	} else if summary.ReportWarning && s.log != nil {
		s.writeLogDiagnostic("tao telemetry warning: " + summary.WarningMessage)
	}
	sessionResult := BatchAgentSessionResult{Output: agentsession.ResultText(result), Provider: result, ReasoningEffort: s.models.EffortFor(role)}
	s.recordTelemetry(request, result, err)
	if s.observe != nil {
		s.observe(request, sessionResult, err)
	}
	return sessionResult, err
}

func (s BatchAgentSession) recordTelemetry(request BatchAgentSessionRequest, result agentsession.Result, sessionErr error) {
	if s.eventAppender == nil || request.BatchID == "" || !result.Invoked {
		return
	}
	outcome := BatchAgentOutcomeCompleted
	if sessionErr != nil {
		outcome = BatchAgentOutcomeFailed
	}
	summary := agentsession.Summarize(result, sessionErr)
	if summary.TimedOut {
		outcome = BatchAgentOutcomeTimedOut
		durationSeconds := summary.TimeoutSeconds
		event := BatchAgentEvent{
			Schema: BatchAgentEventSchema, Type: BatchAgentEventTypeTimeout, BatchID: request.BatchID,
			Timestamp: s.now().UTC(), Operation: request.Operation, Attempt: request.Attempt,
			Agent: result.AgentLabel, PlanID: request.CandidatePlanID, Outcome: outcome,
			TimeoutDurationSeconds: &durationSeconds,
		}
		s.appendTelemetry(event, "session timeout")
	}
	event := BatchAgentEvent{
		Schema: BatchAgentEventSchema, Type: BatchAgentEventTypeMetrics, BatchID: request.BatchID,
		Timestamp: s.now().UTC(), Operation: request.Operation, Attempt: request.Attempt,
		Agent: result.AgentLabel, PlanID: request.CandidatePlanID, Outcome: outcome,
		Metrics: newBatchAgentMetrics(result),
	}
	s.appendTelemetry(event, "metrics")
}

func (s BatchAgentSession) writeLogDiagnostic(message string) {
	record := logrecord.Record{Type: logrecord.TypeDiagnostic, Content: message}
	if s.framedLog != nil {
		_ = logrecord.Write(s.framedLog, record)
	} else if s.log != nil {
		_ = logrecord.Render(s.log, record)
	}
}

func (s BatchAgentSession) appendTelemetry(event BatchAgentEvent, label string) {
	if err := s.eventAppender.AppendAgentEvent(event); err != nil && s.log != nil {
		s.writeLogDiagnostic(fmt.Sprintf("tao telemetry warning: append merge-batch %s event: %v", label, err))
	}
}
