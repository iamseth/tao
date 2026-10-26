package repoadmission

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/runstatus"
	"github.com/iamseth/tao/internal/taodata"
)

const observationLimit = 128

// Owner is an operational observation, never lifecycle or lock authority.
type Owner struct {
	PlanID string
	PID    int
	Phase  runstatus.Phase
}

type ObservedPlan struct{ ID, Dir string }

// ObservationSource permits deterministic read-only liveness tests. Missing or
// invalid signals are skipped; they cannot cancel authoritative contention.
type ObservationSource struct {
	Now        func() time.Time
	ReadLock   func(string, string) (plan.RunLock, error)
	ReadStatus func(string) (runstatus.Record, error)
	Warn       func(error)
	SelfPID    int
}

func (s ObservationSource) warn(err error) {
	if err != nil && !os.IsNotExist(err) && s.Warn != nil {
		s.Warn(err)
	}
}

func mergePhase(phase runstatus.Phase) bool {
	switch phase {
	case runstatus.PhaseMergeIntegrating, runstatus.PhaseMergeVerifying, runstatus.PhaseMergeRecording, runstatus.PhaseMergeCleanup:
		return true
	}
	return false
}

func invocationPID(id string) int {
	prefix, suffix, ok := strings.Cut(id, "-")
	if !ok || suffix == "" {
		return 0
	}
	pid, err := strconv.Atoi(prefix)
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}

func (s ObservationSource) Observe(ctx context.Context, request Request, repoID string, plans []ObservedPlan) []Owner {
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	plans = append([]ObservedPlan(nil), plans...)
	sort.Slice(plans, func(i, j int) bool {
		if plans[i].ID != plans[j].ID {
			return plans[i].ID < plans[j].ID
		}
		return plans[i].Dir < plans[j].Dir
	})
	seen := make(map[string]bool)
	var owners []Owner
	for i, candidate := range plans {
		if i >= observationLimit || ctx.Err() != nil {
			break
		}
		if seen[candidate.Dir] {
			continue
		}
		seen[candidate.Dir] = true
		var lock plan.RunLock
		if s.ReadLock != nil {
			var err error
			lock, err = s.ReadLock(candidate.Dir, candidate.ID)
			if err != nil {
				s.warn(err)
				lock = plan.RunLock{}
			}
		}
		if s.SelfPID > 0 && lock.PID == s.SelfPID {
			continue
		}
		owner := Owner{PlanID: candidate.ID}
		if lock.ProcessAlive {
			owner.PID = lock.PID
		}
		fresh := false
		if s.ReadStatus != nil {
			record, err := s.ReadStatus(candidate.ID)
			switch {
			case err != nil:
				s.warn(err)
			case record.Schema != runstatus.Schema || record.RepoID != repoID || record.PlanID != candidate.ID || record.InvocationID == "" || record.HeartbeatAt.IsZero() || record.InvocationStartedAt.IsZero() || record.Phase == "":
				s.warn(fmt.Errorf("invalid admission status for plan %q", candidate.ID))
			case runstatus.DeriveFreshness(record, now) == runstatus.FreshnessFresh:
				pid := invocationPID(record.InvocationID)
				if s.SelfPID > 0 && pid == s.SelfPID {
					continue
				}
				// A lock and a heartbeat may belong to different invocations. Never
				// attach the latter's phase to an unmatched lock owner.
				if lock.PID == 0 || (pid > 0 && pid == lock.PID) {
					fresh = true
					owner.Phase = record.Phase
					if owner.PID == 0 {
						owner.PID = pid
					}
				}
			}
		}
		if request.Mode == Merge && (lock.ProcessAlive || fresh) || request.Mode == Run && fresh && mergePhase(owner.Phase) {
			owners = append(owners, owner)
		}
	}
	return owners
}

// observeSiblings bounds candidate examination and individual artifact reads.
// Inactive directory entries require only presence checks, not state or Git
// reads. It never loads plan events, repairs files or consults monitor projections.
func observeSiblings(ctx context.Context, request Request, repository string) []Owner {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	warn := request.Warn
	if warn == nil {
		warn = func(err error) { _, _ = fmt.Fprintf(os.Stderr, "warning: repository admission observation: %v\n", err) }
	}
	source := ObservationSource{ReadLock: readObservationLock, Warn: warn, SelfPID: os.Getpid()}
	registry := taodata.NewRegistry("")
	// The normal common directory identifies the control root even when the
	// invocation uses a linked worktree. Nonstandard layouts remain protected
	// by the lease; do not guess a catalog identity for them.
	var repoID string
	var statusPath func(string) (string, error)
	var directories []string
	if filepath.Base(repository) == ".git" {
		root := filepath.Dir(repository)
		repoID = taodata.RepoID(root)
		repo := taodata.Repo{ID: repoID, Root: root}
		directories = append(directories, registry.PlansDir(repo))
		store := runstatus.NewStore(registry.RuntimeStatusDir(repo), nil)
		statusPath = store.Path
		source.ReadStatus = func(id string) (runstatus.Record, error) {
			var record runstatus.Record
			path, err := store.Path(id)
			if err != nil {
				return record, err
			}
			data, err := readObservationFile(path)
			if err != nil {
				return record, err
			}
			err = json.Unmarshal(data, &record)
			return record, err
		}
	}
	if request.PlansDir != "" {
		directories = append(directories, request.PlansDir)
	}
	if request.PlanDir != "" {
		directories = append(directories, filepath.Dir(request.PlanDir))
	}
	sort.Strings(directories)
	seen := make(map[string]bool)
	var candidates []ObservedPlan
	type identityResult struct {
		common string
		err    error
	}
	identities := make(map[string]identityResult)
	examined := 0
	present := func(path string) bool {
		_, err := os.Lstat(path)
		source.warn(err)
		return err == nil
	}
	for _, directory := range directories {
		if ctx.Err() != nil || examined >= observationLimit {
			break
		}
		directory, err := filepath.EvalSymlinks(directory)
		if err != nil {
			source.warn(err)
			continue
		}
		if seen[directory] {
			continue
		}
		seen[directory] = true
		entries, err := os.ReadDir(directory)
		if err != nil {
			source.warn(err)
			continue
		}
		for _, entry := range entries {
			if ctx.Err() != nil || examined >= observationLimit {
				break
			}
			if !entry.IsDir() {
				continue
			}
			dir := filepath.Join(directory, entry.Name())
			hasSignal := present(filepath.Join(dir, ".run.lock"))
			if !hasSignal && statusPath != nil {
				path, err := statusPath(entry.Name())
				if err == nil {
					hasSignal = present(path)
				}
			}
			if !hasSignal {
				continue
			}
			examined++
			state, err := readObservationState(filepath.Join(dir, "state.json"))
			if err != nil {
				source.warn(err)
				continue
			}
			if state.Plan.ID == "" || state.Repo.Root == "" {
				source.warn(fmt.Errorf("invalid admission plan %q", dir))
				continue
			}
			resolved, cached := identities[state.Repo.Root]
			if !cached {
				resolved.common, resolved.err = identity(ctx, state.Repo.Root)
				identities[state.Repo.Root] = resolved
				source.warn(resolved.err)
			}
			if resolved.err != nil || resolved.common != repository {
				continue
			}
			candidates = append(candidates, ObservedPlan{ID: state.Plan.ID, Dir: dir})
		}
	}
	return source.Observe(ctx, request, repoID, candidates)
}

func readObservationState(path string) (plan.State, error) {
	var state plan.State
	data, err := readObservationFile(path)
	if err != nil {
		return state, err
	}
	err = json.Unmarshal(data, &state)
	return state, err
}

func readObservationFile(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0) // #nosec G304 -- bounded read-only diagnostic artifact.
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("nonregular admission artifact %q", path)
	}
	const limit = 64 * 1024
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, fmt.Errorf("admission artifact %q exceeds read limit", path)
	}
	return data, nil
}

// Read the unchanged legacy key=value lock format with a strict size bound.
// A PID supplies liveness only, never a merge phase.
func readObservationLock(dir, planID string) (plan.RunLock, error) {
	var lock plan.RunLock
	data, err := readObservationFile(filepath.Join(dir, ".run.lock"))
	if err != nil {
		return lock, err
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && key == "plan_id" && strings.TrimSpace(value) != planID {
			return lock, fmt.Errorf("admission lock plan mismatch in %q", dir)
		}
		if !ok || key != "pid" {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || pid <= 0 {
			return lock, fmt.Errorf("invalid admission lock PID in %q", dir)
		}
		if lock.PID != 0 {
			return plan.RunLock{}, fmt.Errorf("duplicate admission lock PID in %q", dir)
		}
		lock.PID = pid
	}
	if lock.PID == 0 {
		return lock, fmt.Errorf("missing admission lock PID in %q", dir)
	}
	err = syscall.Kill(lock.PID, 0)
	lock.ProcessAlive = err == nil || errors.Is(err, syscall.EPERM)
	return lock, nil
}
