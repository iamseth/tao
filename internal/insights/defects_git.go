package insights

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/commandrunner"
)

const (
	defectHistoryLimit      = 10000
	defectFixLimit          = 500
	defectFileLimit         = 100
	defectRangeLimit        = 2000
	defectCommandLimit      = 2000
	defectStdoutLimit       = 1 << 20
	defectStderrLimit       = 64 << 10
	defectOutputLimit       = 32 << 20
	defectCommandTimeout    = 5 * time.Second
	defectRepositoryTimeout = 30 * time.Second
)

var defectImmutableSHA = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
var defectHunk = regexp.MustCompile(`^@@ -([0-9]+)(?:,([0-9]+))? \+[0-9]+(?:,[0-9]+)? @@`)
var defectBlameHeader = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64}) [0-9]+ [0-9]+(?: [0-9]+)?$`)
var errDefectBound = errors.New("defect evidence bound exceeded")

type gitDefectCollector struct {
	repositoryID, root, notesDir string
	runner                       commandrunner.Runner
}

// NewGitDefectCollector constructs an opt-in local, read-only evidence source.
func NewGitDefectCollector(repositoryID, root, notesDir string, runner commandrunner.Runner) DefectCollector {
	if runner == nil {
		runner = commandrunner.DefaultLocal
	}
	return gitDefectCollector{repositoryID, root, notesDir, runner}
}

type defectOutput struct {
	buffer   bytes.Buffer
	limit    int
	overflow bool
}

func (w *defectOutput) Write(p []byte) (int, error) {
	n := len(p)
	if n > w.limit-w.buffer.Len() {
		w.overflow = true
		p = p[:max(0, w.limit-w.buffer.Len())]
	}
	_, _ = w.buffer.Write(p)
	if w.overflow {
		return n, errDefectBound
	}
	return n, nil
}

type defectGitScan struct {
	collector                gitDefectCollector
	ctx                      context.Context
	scan                     DefectScan
	commands, output, ranges int
	tags                     map[string][]string
	ancestry                 map[[2]string]bool
	firstParent              map[string]int
	lazyFetchChecked         bool
	omitNoLazyFetch          bool
	unsafeFallback           bool
}

func (s *defectGitScan) git(args ...string) (string, error) {
	if s.unsafeFallback {
		return "", errors.New("cannot safely read history without lazy-fetch protection")
	}
	if err := s.ctx.Err(); err != nil {
		s.scan.TimedOut = true
		s.scan.Complete = false
		return "", err
	}
	if s.commands >= defectCommandLimit || s.output >= defectOutputLimit {
		s.scan.Truncated = true
		s.scan.Complete = false
		return "", errDefectBound
	}
	s.commands++
	ctx, cancel := context.WithTimeout(s.ctx, defectCommandTimeout)
	defer cancel()
	stderr := defectOutput{limit: min(defectStderrLimit, defectOutputLimit-s.output)}
	out := defectOutput{limit: min(defectStdoutLimit, defectOutputLimit-s.output-stderr.limit)}
	// Partial clones must report missing objects, never fetch them as a read side effect.
	argv := []string{"-C", s.collector.root, "--no-optional-locks", "--no-lazy-fetch", "--no-pager", "--no-replace-objects", "--literal-pathspecs", "-c", "diff.autoRefreshIndex=false", "-c", "core.fsmonitor=false", "-c", "core.quotePath=false", "-c", "blame.ignoreRevsFile="}
	if s.omitNoLazyFetch {
		argv = append(argv[:3], argv[4:]...)
	}
	first := !s.lazyFetchChecked
	s.lazyFetchChecked = true
	err := s.collector.runner(ctx, "", "git", append(argv, args...), &out, &stderr)
	s.output += out.buffer.Len() + stderr.buffer.Len()
	if out.overflow || stderr.overflow || s.output > defectOutputLimit {
		s.scan.Truncated = true
		s.scan.Complete = false
		return "", errDefectBound
	}
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
		s.scan.TimedOut = true
		s.scan.Complete = false
		return "", context.DeadlineExceeded
	}
	if first && err != nil && strings.Contains(stderr.buffer.String(), "unknown option: --no-lazy-fetch") {
		s.omitNoLazyFetch = true
		// Older Git cannot suppress lazy fetching. Probe configuration only,
		// before any command that might access a missing object. An unreadable
		// configuration is not evidence that the repository is a full clone.
		for _, probe := range [][]string{
			{"config", "--get-regexp", `^remote\..*\.promisor$`},
			{"config", "--get", "extensions.partialclone"},
		} {
			_, configErr := s.git(probe...)
			var exit interface{ ExitCode() int }
			if !errors.As(configErr, &exit) || exit.ExitCode() != 1 {
				s.unsafeFallback = true
				s.missing()
				return "", errors.New("partial-clone configuration present or unreadable")
			}
		}
		return s.git(args...)
	}
	return out.buffer.String(), err
}

func (s *defectGitScan) missing() { s.scan.MissingHistory = true; s.scan.Complete = false }
func (s *defectGitScan) resolve(ref string) string {
	text, err := s.git("rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	text = strings.TrimSpace(text)
	if err != nil || !defectImmutableSHA.MatchString(text) {
		return ""
	}
	return text
}
func (s *defectGitScan) tip() string {
	ref, err := s.git("symbolic-ref", "--quiet", "refs/remotes/origin/HEAD")
	ref = strings.TrimSpace(ref)
	if err == nil && strings.HasPrefix(ref, "refs/remotes/origin/") && !strings.ContainsAny(ref, "\x00\r\n") {
		branch := strings.TrimPrefix(ref, "refs/remotes/origin/")
		if sha := s.resolve("refs/heads/" + branch); sha != "" {
			return sha
		}
		if sha := s.resolve(ref); sha != "" {
			return sha
		}
	}
	for _, branch := range []string{"main", "master"} {
		if sha := s.resolve("refs/heads/" + branch); sha != "" {
			return sha
		}
	}
	return ""
}

func (c gitDefectCollector) CollectDefects(ctx context.Context, plans []DefectPlan, now time.Time) (DefectReport, error) {
	if ctx.Err() != nil {
		return DefectReport{}, ctx.Err()
	}
	local, cancel := context.WithTimeout(ctx, defectRepositoryTimeout)
	defer cancel()
	s := defectGitScan{collector: c, ctx: local, scan: DefectScan{Complete: true}, tags: make(map[string][]string)}
	plans = append([]DefectPlan(nil), plans...)
	sort.Slice(plans, func(i, j int) bool {
		if plans[i].ID != plans[j].ID {
			return plans[i].ID < plans[j].ID
		}
		return plans[i].MergeSHA < plans[j].MergeSHA
	})
	input := DefectInput{RepositoryID: c.repositoryID, Now: now, Plans: plans}
	finish := func() (DefectReport, error) {
		if local.Err() != nil {
			s.scan.TimedOut = true
			s.scan.Complete = false
		}
		input.Coverage = s.scan
		if ctx.Err() != nil {
			return DefectReport{}, ctx.Err()
		}
		return AttributeDefects(input), nil
	}
	if !filepath.IsAbs(c.root) {
		s.scan.Complete = false
		return finish()
	}
	var notesComplete bool
	input.Notes, notesComplete = readDefectNotes(local, c.repositoryID, c.notesDir)
	if !notesComplete {
		s.scan.Complete = false
	}
	s.scan.PinnedTip = s.tip()
	if s.scan.PinnedTip == "" {
		s.missing()
		return finish()
	}
	s.scan.Usable = true
	shallow, err := s.git("rev-parse", "--is-shallow-repository")
	if err != nil || strings.TrimSpace(shallow) != "false" {
		s.missing()
	}
	history, err := s.git("log", "--first-parent", "--no-show-signature", "--format=%H%x00%P%x00%ct%x00%B", "-z", fmt.Sprintf("--max-count=%d", defectHistoryLimit+1), s.scan.PinnedTip, "--")
	if err != nil {
		s.missing()
		return finish()
	}
	fields := strings.Split(history, "\x00")
	if fields[len(fields)-1] != "" || (len(fields)-1)%4 != 0 {
		s.missing()
		return finish()
	}
	count := (len(fields) - 1) / 4
	if count > defectHistoryLimit {
		s.scan.Truncated = true
		s.scan.Complete = false
		count = defectHistoryLimit
	}
	s.firstParent = make(map[string]int, count)
	for i := 0; i < count; i++ {
		if f, ok := parseDefectCommit(fields[i*4 : i*4+4]); ok {
			s.firstParent[f.SHA] = i
		}
	}
	// Pin plan eligibility once, rather than probing every plan for every fix.
	eligiblePlans := make(map[string][]DefectPlan)
	for _, p := range plans {
		if p.CurrentMerged && defectImmutableSHA.MatchString(p.MergeSHA) && s.ancestor(p.MergeSHA, s.scan.PinnedTip) {
			eligiblePlans[p.ID] = append(eligiblePlans[p.ID], p)
		}
	}
	for i := 0; i < count; i++ {
		if local.Err() != nil {
			s.scan.TimedOut = true
			s.scan.Complete = false
			break
		}
		f, ok := parseDefectCommit(fields[i*4 : i*4+4])
		if !ok {
			s.missing()
			continue
		}
		if !defectFixSubject.MatchString(f.Subject) && !defectBugLinked(f, input.Notes) {
			continue
		}
		if len(input.Fixes) >= defectFixLimit {
			s.scan.Truncated = true
			s.scan.Complete = false
			break
		}
		if !f.Unsupported {
			f.Paths = s.paths(f)
			for _, id := range defectTargetIDs(f, input.Notes) {
				for _, p := range eligiblePlans[id] {
					if s.ancestor(p.MergeSHA, f.Parent) {
						f.AncestorPlanIDs = append(f.AncestorPlanIDs, id)
						break
					}
				}
			}
		}
		input.Fixes = append(input.Fixes, f)
	}
	return finish()
}

func parseDefectCommit(v []string) (DefectFix, bool) {
	f := DefectFix{SHA: v[0], Message: v[3]}
	if !defectImmutableSHA.MatchString(f.SHA) {
		return f, false
	}
	seconds, err := strconv.ParseInt(v[2], 10, 64)
	if err != nil {
		return f, false
	}
	f.CommitterAt = time.Unix(seconds, 0).UTC()
	f.Subject, _, _ = strings.Cut(f.Message, "\n")
	parents := strings.Fields(v[1])
	f.Unsupported = len(parents) != 1
	for _, p := range parents {
		if !defectImmutableSHA.MatchString(p) {
			return f, false
		}
	}
	if len(parents) == 1 {
		f.Parent = parents[0]
	}
	return f, true
}
func defectBugLinked(f DefectFix, notes []DefectNote) bool {
	ids, _ := ParseDefectTrailers(f.Message)
	for _, n := range notes {
		if defectContains(n.Tags, "bug") && (defectContains(ids, n.ArchivePlanID) || defectContains(ids, n.LegacyPromotionPlanID)) {
			return true
		}
	}
	return false
}

// Candidate discovery mirrors explicit references and path evidence consumed by
// AttributeDefects; discovery itself never grants ancestry or eligibility.
func defectTargetIDs(f DefectFix, notes []DefectNote) []string {
	sources, refs := ParseDefectTrailers(f.Message)
	for _, n := range notes {
		if defectContains(sources, n.ArchivePlanID) || defectContains(sources, n.LegacyPromotionPlanID) {
			refs = append(refs, DefectNoteReferences(n.Text)...)
		}
	}
	for _, p := range f.Paths {
		for _, candidates := range [][]DefectCandidate{p.Lines, p.File} {
			for _, c := range candidates {
				refs = append(refs, c.PlanID)
			}
		}
	}
	sort.Strings(refs)
	ids := make([]string, 0, len(refs))
	for _, id := range refs {
		if defectPlanID.MatchString(id) && (len(ids) == 0 || ids[len(ids)-1] != id) {
			ids = append(ids, id)
		}
	}
	return ids
}

func (s *defectGitScan) ancestor(a, b string) bool {
	if ai, ok := s.firstParent[a]; ok {
		if bi, ok := s.firstParent[b]; ok {
			return ai >= bi
		}
	}
	if s.ancestry == nil {
		s.ancestry = make(map[[2]string]bool)
	}
	key := [2]string{a, b}
	if result, ok := s.ancestry[key]; ok {
		return result
	}
	// Failed probes also remain false; missing-history coverage stays sticky.
	s.ancestry[key] = false
	result, err := s.git("merge-base", a, b)
	if err != nil {
		s.missing()
		return false
	}
	result = strings.TrimSpace(result)
	if !defectImmutableSHA.MatchString(result) {
		s.missing()
		return false
	}
	s.ancestry[key] = result == a
	return s.ancestry[key]
}
func (s *defectGitScan) candidates(sha string) []DefectCandidate {
	if !defectImmutableSHA.MatchString(sha) {
		s.missing()
		return nil
	}
	ids, ok := s.tags[sha]
	if !ok {
		msg, err := s.git("show", "--no-patch", "--no-show-signature", "--format=%B", sha, "--")
		if err != nil {
			s.missing()
			return nil
		}
		ids, _ = ParseDefectTrailers(msg)
		s.tags[sha] = ids
	}
	if len(ids) == 0 {
		return []DefectCandidate{{}}
	}
	var result []DefectCandidate
	for _, id := range ids {
		result = append(result, DefectCandidate{PlanID: id, Ancestor: true})
	}
	return result
}
func (s *defectGitScan) paths(f DefectFix) []DefectPath {
	text, err := s.git("diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--no-relative", "--ignore-submodules=none", "--name-only", "-z", f.Parent, f.SHA, "--")
	if err != nil {
		s.missing()
		return nil
	}
	names := strings.Split(text, "\x00")
	if names[len(names)-1] != "" {
		s.missing()
		return nil
	}
	names = names[:len(names)-1]
	sort.Strings(names)
	if len(names) == 0 {
		return nil
	}
	// Do not parse quoted patch filenames: Git emits both lists in byte order.
	patch, err := s.git("diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--no-relative", "--ignore-submodules=none", "--diff-algorithm=myers", "--no-indent-heuristic", "--inter-hunk-context=0", "--no-color", "--unified=0", "-O/dev/null", f.Parent, f.SHA, "--")
	if err != nil || !strings.HasPrefix(patch, "diff --git ") {
		s.missing()
		return nil
	}
	sections := strings.Split("\n"+patch, "\ndiff --git ")[1:]
	if len(sections) != len(names) {
		s.missing()
		return nil
	}
	if len(names) > defectFileLimit {
		names = names[:defectFileLimit]
		s.scan.Truncated = true
		s.scan.Complete = false
	}
	var paths []DefectPath
	for i, name := range names {
		if name == "" {
			s.missing()
			continue
		}
		patch := sections[i]
		p := DefectPath{}
		blameArgs := []string{"blame", "--line-porcelain", "--no-textconv", "--ignore-revs-file", ""}
		expectedLines := 0
		for _, line := range strings.Split(patch, "\n") {
			match := defectHunk.FindStringSubmatch(line)
			if match == nil {
				if strings.HasPrefix(line, "@@") {
					s.missing()
					p.HasUsableOldLines = true
				}
				continue
			}
			start, e1 := strconv.Atoi(match[1])
			count := 1
			var e2 error
			if match[2] != "" {
				count, e2 = strconv.Atoi(match[2])
			}
			if e1 != nil || e2 != nil || start < 0 || count < 0 || (count > 0 && (start == 0 || start > int(^uint(0)>>1)-count)) {
				s.missing()
				continue
			}
			if count == 0 {
				continue
			}
			p.HasUsableOldLines = true
			if s.ranges >= defectRangeLimit {
				s.scan.Truncated = true
				s.scan.Complete = false
				continue
			}
			s.ranges++
			if expectedLines > int(^uint(0)>>1)-count {
				s.missing()
				continue
			}
			expectedLines += count
			blameArgs = append(blameArgs, "-L", fmt.Sprintf("%d,%d", start, start+count-1))
		}
		if expectedLines > 0 {
			blame, err := s.git(append(blameArgs, f.Parent, "--", name)...)
			if err != nil {
				s.missing()
			} else {
				seen := 0
				for _, bl := range strings.Split(blame, "\n") {
					if m := defectBlameHeader.FindStringSubmatch(bl); m != nil {
						seen++
						p.Lines = append(p.Lines, s.candidates(m[1])...)
					}
				}
				if seen != expectedLines {
					s.missing()
				}
			}
		}
		if !p.HasUsableOldLines {
			touch, err := s.git("log", "-1", "--no-show-signature", "--format=%H", f.Parent, "--", name)
			switch {
			case err != nil:
				s.missing()
			case strings.TrimSpace(touch) != "":
				p.File = s.candidates(strings.TrimSpace(touch))
			default:
				p.File = []DefectCandidate{{}}
			}
		}
		paths = append(paths, p)
	}
	return paths
}

var _ io.Writer = (*defectOutput)(nil)
