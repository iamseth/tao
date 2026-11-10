package insights

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/commandrunner"
	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/testenv"
)

type defectConfigAbsent struct{}

func (defectConfigAbsent) Error() string { return "config absent" }
func (defectConfigAbsent) ExitCode() int { return 1 }

func TestDefectCollectionOldGitFallback(t *testing.T) {
	for _, partial := range []string{"", "remote", "extension", "config-error"} {
		t.Run("partial="+partial, func(t *testing.T) {
			calls, configs, history := 0, 0, 0
			collector := NewGitDefectCollector("repo", t.TempDir(), t.TempDir(), func(_ context.Context, _, _ string, args []string, out, stderr io.Writer) error {
				calls++
				if calls == 1 {
					if args[3] != "--no-lazy-fetch" {
						t.Fatalf("first command lacks protection: %v", args)
					}
					_, _ = io.WriteString(stderr, "unknown option: --no-lazy-fetch\nusage: git ...")
					return errors.New("exit status 129")
				}
				for _, arg := range args {
					if arg == "--no-lazy-fetch" {
						t.Fatalf("unsupported flag retried: %v", args)
					}
				}
				probe := args[14:]
				switch probe[0] {
				case "config":
					configs++
					switch {
					case reflect.DeepEqual(probe, []string{"config", "--get-regexp", `^remote\..*\.promisor$`}):
						if partial == "remote" {
							_, _ = io.WriteString(out, "remote.origin.promisor true\n")
							return nil
						}
					case reflect.DeepEqual(probe, []string{"config", "--get", "extensions.partialclone"}):
						if partial == "extension" {
							_, _ = io.WriteString(out, "origin\n")
							return nil
						}
					default:
						t.Fatalf("unexpected config probe: %v", probe)
					}
					if partial == "config-error" {
						return errors.New("cannot read config")
					}
					return defectConfigAbsent{}
				case "symbolic-ref":
					return errors.New("absent")
				case "rev-parse":
					if partial != "" {
						t.Fatal("object access before safe fallback established")
					}
					if probe[1] == "--is-shallow-repository" {
						_, _ = io.WriteString(out, "false\n")
					} else {
						_, _ = io.WriteString(out, strings.Repeat("a", 40))
					}
				case "log":
					history++
				default:
					t.Fatalf("unexpected command: %v", probe)
				}
				return nil
			})
			report, err := collector.CollectDefects(context.Background(), nil, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if partial == "" {
				if !report.Coverage.Scan.Usable || !report.Coverage.Scan.Complete || report.Coverage.Scan.MissingHistory || configs != 2 || history != 1 {
					t.Fatalf("healthy old Git scan failed: %+v, config=%d history=%d", report, configs, history)
				}
			} else if report.Availability != DefectUnavailable || !report.Coverage.Scan.MissingHistory || history != 0 {
				t.Fatalf("unsafe fallback not refused: %+v", report)
			}
		})
	}
}

func TestDefectCollectionScalesWithCandidateTargets(t *testing.T) {
	const planCount, fixCount = 244, 97
	now := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	sha := func(i int) string { return fmt.Sprintf("%040x", i) }
	plans := make([]DefectPlan, planCount)
	for i := range plans {
		plans[i] = DefectPlan{ID: fmt.Sprintf("20260901-000000-plan%d", i), MergeSHA: sha(i + 1), CurrentMerged: true, CreatedAt: now.AddDate(0, -2, 0), MergeAt: now.AddDate(0, -2, 1)}
	}
	var history strings.Builder
	for i := range fixCount {
		fmt.Fprintf(&history, "%s\x00%s\x00%d\x00fix: repair\n\nFixes-Plan: %s\n\x00", sha(1000+i), sha(900), now.AddDate(0, -2, 2).Unix(), plans[i%10].ID)
	}
	calls := 0
	pairs := make(map[string]int)
	collector := NewGitDefectCollector("repo", t.TempDir(), t.TempDir(), func(_ context.Context, _, _ string, args []string, out, _ io.Writer) error {
		calls++
		probe := args[15:]
		switch probe[0] {
		case "symbolic-ref":
			return errors.New("absent")
		case "rev-parse":
			if probe[1] == "--is-shallow-repository" {
				_, _ = io.WriteString(out, "false\n")
			} else {
				_, _ = io.WriteString(out, sha(2000))
			}
		case "log":
			_, _ = io.WriteString(out, history.String())
		case "merge-base":
			pairs[strings.Join(probe[1:], " ")]++
			_, _ = io.WriteString(out, probe[1])
		case "diff":
			// Empty path list; explicit assertions supply the targets.
		default:
			t.Fatalf("unexpected probe: %v", probe)
		}
		return nil
	})
	report, err := collector.CollectDefects(context.Background(), plans, now)
	if err != nil || report.Availability != DefectAvailable || report.Coverage.Attributed != fixCount {
		t.Fatalf("attributed %d/%d, availability %s, calls %d, error %v", report.Coverage.Attributed, fixCount, report.Availability, calls, err)
	}
	if calls >= defectCommandLimit/2 {
		t.Fatalf("too many Git calls: %d", calls)
	}
	for pair, count := range pairs {
		if count != 1 {
			t.Fatalf("repeated ancestry check %s: %d", pair, count)
		}
	}
	if report.Plans[0].WindowFixes == 0 || report.Coverage.Unattributed != 0 {
		t.Fatal("lost attribution")
	}
}

func TestDefectCollectionFirstParentScale(t *testing.T) {
	const planCount, fixCount, files = 240, 100, 6
	now := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	sha := func(i int) string { return fmt.Sprintf("%040x", i) }
	plans := make([]DefectPlan, planCount)
	for i := range plans {
		plans[i] = DefectPlan{ID: fmt.Sprintf("20260901-000000-plan%d", i), MergeSHA: sha(i + 1), CurrentMerged: true, CreatedAt: now.AddDate(0, -2, 0), MergeAt: now.AddDate(0, -2, 1)}
	}
	var history strings.Builder
	for i := planCount + fixCount; i > 0; i-- {
		subject := "feat: plan"
		if i > planCount {
			subject = "fix: repair"
		}
		fmt.Fprintf(&history, "%s\x00%s\x00%d\x00%s\n\x00", sha(i), sha(i-1), now.AddDate(0, -2, 2).Unix(), subject)
	}
	calls, patches, merges, blames := 0, 0, 0, 0
	collector := NewGitDefectCollector("repo", t.TempDir(), t.TempDir(), func(_ context.Context, _, _ string, args []string, out, _ io.Writer) error {
		calls++
		probe := args[15:]
		switch probe[0] {
		case "symbolic-ref":
			return errors.New("absent")
		case "rev-parse":
			if probe[1] == "--is-shallow-repository" {
				_, _ = io.WriteString(out, "false\n")
			} else {
				_, _ = io.WriteString(out, sha(planCount+fixCount))
			}
		case "log":
			_, _ = io.WriteString(out, history.String())
		case "merge-base":
			merges++
			_, _ = io.WriteString(out, probe[1])
		case "diff":
			if defectContains(probe, "--name-only") {
				for i := range files {
					_, _ = fmt.Fprintf(out, "file%d\x00", i)
				}
			} else {
				patches++
				for i := range files {
					_, _ = fmt.Fprintf(out, "diff --git a/file%d b/file%d\n@@ -1 +1 @@\n-old\n+new\n@@ -10 +10 @@\n-old\n+new\n", i, i)
				}
			}
		case "blame":
			blames++
			_, _ = fmt.Fprintf(out, "%s 1 1 1\n%s 10 10 1\n", sha(1), sha(2))
		case "show":
			id := plans[0].ID
			if probe[len(probe)-2] == sha(2) {
				id = plans[1].ID
			}
			_, _ = fmt.Fprintf(out, "feat: plan\n\nTao-Plan: %s\n", id)
		default:
			t.Fatalf("unexpected probe %q", probe)
		}
		return nil
	})
	report, err := collector.CollectDefects(context.Background(), plans, now)
	if err != nil || report.Availability != DefectAvailable || report.Coverage.Attributed != fixCount || report.Coverage.Examined != fixCount || patches != fixCount || merges != 0 || blames != fixCount*files || calls >= defectCommandLimit/2 {
		t.Fatalf("report=%+v err=%v patches=%d merges=%d blames=%d calls=%d", report.Coverage, err, patches, merges, blames, calls)
	}
}

func TestDefectFirstParentAncestry(t *testing.T) {
	s := defectGitScan{firstParent: map[string]int{"new": 0, "old": 1}}
	if !s.ancestor("old", "new") || !s.ancestor("new", "new") || s.ancestor("new", "old") {
		t.Fatal("incorrect first-parent ancestry")
	}
}

func TestDefectPatchSectionMismatch(t *testing.T) {
	for _, sections := range []int{0, 1, 3} {
		t.Run(fmt.Sprint(sections), func(t *testing.T) {
			s := defectGitScan{ctx: context.Background(), scan: DefectScan{Complete: true}}
			s.collector = gitDefectCollector{root: t.TempDir(), runner: func(_ context.Context, _, _ string, args []string, out, _ io.Writer) error {
				probe := args[15:]
				if probe[0] != "diff" {
					t.Fatalf("unexpected probe %q", probe)
				}
				if defectContains(probe, "--name-only") {
					_, _ = io.WriteString(out, "a\x00b\x00")
				} else {
					for range sections {
						_, _ = io.WriteString(out, "diff --git a/file b/file\n@@ -1 +1 @@\n")
					}
				}
				return nil
			}}
			if paths := s.paths(DefectFix{SHA: strings.Repeat("a", 40), Parent: strings.Repeat("b", 40)}); len(paths) != 0 || !s.scan.MissingHistory || s.scan.Complete {
				t.Fatalf("paths=%v scan=%+v", paths, s.scan)
			}
		})
	}
}

func TestDefectTargetDiscovery(t *testing.T) {
	const explicit = "20260901-000000-explicit"
	const linked = "20260901-000000-linked"
	const line = "20260901-000000-line"
	const file = "20260901-000000-file"
	const source = "20260901-000000-source"
	f := DefectFix{Message: "fix: repair\n\nTao-Plan: " + source + "\nFixes-Plan: " + explicit, Paths: []DefectPath{{Lines: []DefectCandidate{{PlanID: line}, {PlanID: line}, {}}, File: []DefectCandidate{{PlanID: file}}}}}
	for _, legacy := range []bool{false, true} {
		n := DefectNote{Tags: []string{"bug"}, Text: "Fixes-Plan: " + linked}
		if legacy {
			n.LegacyPromotionPlanID = source
		} else {
			n.ArchivePlanID = source
		}
		got := defectTargetIDs(f, []DefectNote{n, {ArchivePlanID: "unlinked", Text: "Fixes-Plan: 20260901-000000-wrong"}})
		if want := []string{explicit, file, line, linked}; !reflect.DeepEqual(got, want) {
			t.Fatalf("targets = %v, want %v", got, want)
		}
	}
}

func TestDefectAncestryCachesNegativeAndMissing(t *testing.T) {
	for _, missing := range []bool{false, true} {
		calls := 0
		s := defectGitScan{ctx: context.Background(), scan: DefectScan{Complete: true}, collector: gitDefectCollector{root: t.TempDir(), runner: func(_ context.Context, _, _ string, _ []string, out, _ io.Writer) error {
			calls++
			if missing {
				return errors.New("missing object")
			}
			_, _ = io.WriteString(out, strings.Repeat("c", 40))
			return nil
		}}}
		for range 2 {
			if s.ancestor(strings.Repeat("a", 40), strings.Repeat("b", 40)) {
				t.Fatal("negative evidence accepted")
			}
		}
		if calls != 1 || s.scan.MissingHistory != missing {
			t.Fatalf("calls %d, coverage %+v", calls, s.scan)
		}
	}
}

func TestGitDefectAbsentAndCancelled(t *testing.T) {
	calls := 0
	c := NewGitDefectCollector("repo", t.TempDir(), t.TempDir(), func(ctx context.Context, cwd, name string, args []string, out, errOut io.Writer) error {
		calls++
		return errors.New("no ref")
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.CollectDefects(ctx, nil, time.Now()); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if calls != 0 {
		t.Fatal("called runner after cancellation")
	}
	report, err := c.CollectDefects(context.Background(), nil, time.Now())
	if err != nil || report.Availability != DefectUnavailable {
		t.Fatalf("%+v %v", report, err)
	}
}

func TestDefectGitArgvAndBounds(t *testing.T) {
	root := t.TempDir()
	prefix := []string{"-C", root, "--no-optional-locks", "--no-lazy-fetch", "--no-pager", "--no-replace-objects", "--literal-pathspecs", "-c", "diff.autoRefreshIndex=false", "-c", "core.fsmonitor=false", "-c", "core.quotePath=false", "-c", "blame.ignoreRevsFile="}
	for _, tc := range []struct {
		name           string
		stdout, stderr int
		timeout        bool
	}{
		{name: "stdout", stdout: defectStdoutLimit + 1}, {name: "stderr", stderr: defectStderrLimit + 1}, {name: "timeout", timeout: true}, {name: "ok"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := defectGitScan{ctx: context.Background(), scan: DefectScan{Complete: true}, collector: gitDefectCollector{root: root, runner: func(ctx context.Context, cwd, name string, args []string, out, errOut io.Writer) error {
				if cwd != "" || name != "git" || !reflect.DeepEqual(args, append(append([]string{}, prefix...), "rev-parse", "--is-shallow-repository")) {
					t.Fatalf("unexpected command %q %q %q", cwd, name, args)
				}
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > defectCommandTimeout {
					t.Fatal("missing command deadline")
				}
				if tc.timeout {
					return context.DeadlineExceeded
				}
				_, _ = io.WriteString(out, strings.Repeat("x", tc.stdout))
				_, _ = io.WriteString(errOut, strings.Repeat("x", tc.stderr))
				return nil
			}}}
			_, err := s.git("rev-parse", "--is-shallow-repository")
			if tc.name == "ok" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("accepted failed probe")
			}
			if tc.stdout > 0 || tc.stderr > 0 {
				if !s.scan.Truncated {
					t.Fatal("overflow not exposed")
				}
			}
			s.commands = defectCommandLimit
			if _, err = s.git("anything"); !errors.Is(err, errDefectBound) {
				t.Fatalf("command cap: %v", err)
			}
			s.commands = 0
			s.output = defectOutputLimit
			if _, err = s.git("anything"); !errors.Is(err, errDefectBound) {
				t.Fatalf("output cap: %v", err)
			}
		})
	}
}

func TestDefectPinnedTipPrefersLocal(t *testing.T) {
	sha := strings.Repeat("a", 40)
	calls := 0
	s := defectGitScan{ctx: context.Background(), collector: gitDefectCollector{root: t.TempDir(), runner: func(_ context.Context, _, _ string, args []string, out, _ io.Writer) error {
		calls++
		switch calls {
		case 1:
			if args[len(args)-1] != "refs/remotes/origin/HEAD" {
				t.Fatal(args)
			}
			_, _ = io.WriteString(out, "refs/remotes/origin/trunk\n")
		case 2:
			if args[len(args)-1] != "refs/heads/trunk^{commit}" {
				t.Fatal(args)
			}
			_, _ = io.WriteString(out, sha+"\n")
		default:
			t.Fatal("unexpected fallback")
		}
		return nil
	}}}
	if got := s.tip(); got != sha {
		t.Fatal(got)
	}
}

func TestDefectCollectionFailuresAndLimits(t *testing.T) {
	sha, parent := strings.Repeat("a", 40), strings.Repeat("b", 40)
	for _, kind := range []string{"malformed", "missing", "history", "fixes", "cancel", "timeout"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			runner := func(_ context.Context, _, _ string, args []string, out, _ io.Writer) error {
				probe := args[15:]
				switch probe[0] {
				case "symbolic-ref":
					return errors.New("absent")
				case "rev-parse":
					if probe[1] == "--is-shallow-repository" {
						_, _ = io.WriteString(out, "false\n")
					} else {
						_, _ = io.WriteString(out, sha+"\n")
					}
				case "log":
					switch kind {
					case "missing":
						return errors.New("missing object")
					case "malformed":
						_, _ = io.WriteString(out, "malformed\x00")
					case "cancel":
						cancel()
						return ctx.Err()
					case "timeout":
						return context.DeadlineExceeded
					case "history", "fixes":
						count := defectHistoryLimit + 1
						message := "chore: x"
						parents := ""
						if kind == "fixes" {
							count = defectFixLimit + 1
							message = "fix: x"
							parents = parent + " " + parent
						}
						for i := 0; i < count; i++ {
							_, _ = fmt.Fprintf(out, "%040x\x00%s\x001\x00%s\x00", i+1, parents, message)
						}
					}
				case "merge-base":
					if !reflect.DeepEqual(probe, []string{"merge-base", parent, sha}) {
						t.Fatalf("unexpected eligibility probe %q", probe)
					}
					_, _ = io.WriteString(out, parent)
				default:
					t.Fatalf("unexpected probe %q", probe)
				}
				return nil
			}
			plans := []DefectPlan{{ID: "20260101-000000-target", CurrentMerged: true, MergeSHA: parent, MergeAt: time.Unix(1, 0), CreatedAt: time.Unix(1, 0)}}
			r, err := NewGitDefectCollector("repo", t.TempDir(), t.TempDir(), runner).CollectDefects(ctx, plans, time.Now())
			if kind == "cancel" {
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				return
			}
			if err != nil || r.Availability != DefectIncomplete {
				t.Fatalf("%+v %v", r, err)
			}
			if (kind == "history" || kind == "fixes") && !r.Coverage.Scan.Truncated {
				t.Fatal("missing truncation")
			}
			if kind == "timeout" && !r.Coverage.Scan.TimedOut {
				t.Fatal("missing timeout")
			}
		})
	}
}

func TestDefectPathsBatchBlameRangesPerFile(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		t.Run(fmt.Sprintf("missing-header=%t", malformed), func(t *testing.T) {
			const files = 60
			parent := strings.Repeat("b", 40)
			sha := strings.Repeat("a", 40)
			calls := 0
			blames := map[string]int{}
			s := defectGitScan{ctx: context.Background(), scan: DefectScan{Complete: true}, tags: map[string][]string{parent: {}}}
			s.collector = gitDefectCollector{root: t.TempDir(), runner: func(_ context.Context, _, _ string, args []string, out, _ io.Writer) error {
				calls++
				probe := args[15:]
				switch probe[0] {
				case "diff":
					if defectContains(probe, "--name-only") {
						for i := 0; i < files; i++ {
							_, _ = fmt.Fprintf(out, "file%03d\x00", i)
						}
					} else {
						for range files {
							_, _ = io.WriteString(out, "diff --git a/file b/file\n@@ -1,2 +1,2 @@\n@@ -10 +10 @@\n@@ -20,3 +20,3 @@\n@@ -30,0 +30 @@\n")
						}
					}
				case "blame":
					name := probe[len(probe)-1]
					blames[name]++
					want := []string{"blame", "--line-porcelain", "--no-textconv", "--ignore-revs-file", "", "-L", "1,2", "-L", "10,10", "-L", "20,22", parent, "--", name}
					if !reflect.DeepEqual(probe, want) {
						t.Fatalf("blame ranges = %q, want %q", probe, want)
					}
					count := 6
					if malformed {
						count--
					}
					for i := 0; i < count; i++ {
						_, _ = fmt.Fprintf(out, "%s %d %d 1\n", parent, i+1, i+1)
					}
				default:
					t.Fatalf("unexpected probe %q", probe)
				}
				return nil
			}}
			paths := s.paths(DefectFix{SHA: sha, Parent: parent})
			if len(paths) != files || len(blames) != files || s.ranges != files*3 {
				t.Fatalf("paths=%d blames=%d ranges=%d", len(paths), len(blames), s.ranges)
			}
			for name, count := range blames {
				if count != 1 {
					t.Fatalf("%s: %d blame calls", name, count)
				}
			}
			if calls != 2+files || calls >= defectCommandLimit/10 {
				t.Fatalf("unexpected Git command count: %d", calls)
			}
			if s.scan.Complete == malformed || s.scan.MissingHistory != malformed || s.scan.Truncated {
				t.Fatalf("unexpected coverage: %+v", s.scan)
			}
		})
	}
}

func TestDefectPathBoundsAndMalformedBlame(t *testing.T) {
	for _, kind := range []string{"files", "ranges", "blame"} {
		t.Run(kind, func(t *testing.T) {
			s := defectGitScan{ctx: context.Background(), scan: DefectScan{Complete: true}, tags: map[string][]string{}}
			if kind == "ranges" {
				s.ranges = defectRangeLimit
			}
			s.collector = gitDefectCollector{root: t.TempDir(), runner: func(_ context.Context, _, _ string, args []string, out, _ io.Writer) error {
				probe := args[15:]
				switch probe[0] {
				case "diff":
					switch {
					case defectContains(probe, "--name-only"):
						count := 1
						if kind == "files" {
							count = defectFileLimit + 1
						}
						for i := count; i > 0; i-- {
							_, _ = fmt.Fprintf(out, "file%03d\x00", i)
						}
					case kind == "files":
						for range defectFileLimit + 1 {
							_, _ = io.WriteString(out, "diff --git a/file b/file\nBinary files differ\n")
						}
					default:
						_, _ = io.WriteString(out, "diff --git a/file b/file\n@@ -1 +1 @@\n-old\n+new\n")
					}
				case "log": // No parent-side file touch is admissible.
				case "blame":
					if kind != "blame" {
						t.Fatal("range budget bypassed")
					}
					_, _ = io.WriteString(out, "malformed\n")
				default:
					t.Fatal(probe)
				}
				return nil
			}}
			paths := s.paths(DefectFix{SHA: strings.Repeat("a", 40), Parent: strings.Repeat("b", 40)})
			if kind == "files" && len(paths) != defectFileLimit {
				t.Fatal(len(paths))
			}
			if s.scan.Complete {
				t.Fatal("lost incomplete coverage")
			}
			if kind == "blame" {
				if !s.scan.MissingHistory {
					t.Fatal(s.scan)
				}
			} else if !s.scan.Truncated {
				t.Fatal(s.scan)
			}
		})
	}
}

func TestDefectCommitMalformed(t *testing.T) {
	sha := strings.Repeat("a", 40)
	for _, v := range [][]string{{"--evil", sha, "1", "fix: x"}, {sha, "bad", "1", "fix: x"}, {sha, sha, "bad", "fix: x"}} {
		if _, ok := parseDefectCommit(v); ok {
			t.Fatal(v)
		}
	}
	for _, parents := range []string{"", sha + " " + sha} {
		f, ok := parseDefectCommit([]string{sha, parents, "1", "fix: x"})
		if !ok || !f.Unsupported {
			t.Fatal(f)
		}
	}
}

func TestGitDefectLocalHistoryReadOnly(t *testing.T) {
	testenv.SkipIfShort(t)
	root := t.TempDir()
	notes := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...) // #nosec G204 -- test-owned local fixture arguments.
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z")
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %q: %s %v", args, b, err)
		}
		return strings.TrimSpace(string(b))
	}
	git("init", "-b", "main")
	git("config", "user.name", "Fixture")
	git("config", "user.email", "fixture@example.invalid")
	write := func(path, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, path), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("seed", "seed\n")
	git("add", "--", "seed")
	git("commit", "-m", "fix: root")
	id := "20260101-000000-target"
	paths := []string{"line\tname\nfile", "-dash", "binary"}
	write(paths[0], "bad\nkeep\n")
	write(paths[1], "one\n")
	write(paths[2], "\x00old")
	git("add", "--", paths[0], paths[1], paths[2])
	git("commit", "-m", "feat: implementation\n\nTao-Plan: "+id)
	merged := git("rev-parse", "HEAD")
	write(paths[0], "good\nkeep\n")
	write(paths[1], "one\ntwo\n")
	write(paths[2], "\x00new")
	git("add", "--", paths[0], paths[1], paths[2])
	git("commit", "-m", "fix: repair")
	fix := git("rev-parse", "HEAD")
	// Poison commands that would execute if external diff/textconv were enabled.
	git("config", "diff.external", "/definitely/not/a/command")
	git("config", "diff.evil.textconv", "/definitely/not/a/command")
	git("config", "core.fsmonitor", "/definitely/not/a/command")
	write(".gitattributes", "* diff=evil\n")
	git("switch", "-c", "feature")
	write("feature", "not default history\n")
	git("add", "--", "feature")
	git("commit", "-m", "fix: feature only\n\nFixes-Plan: "+id)
	head := git("rev-parse", "HEAD")
	index, err := os.ReadFile(filepath.Join(root, ".git", "index")) // #nosec G304 -- test fixture.
	if err != nil {
		t.Fatal(err)
	}
	snapshots := map[string]string{}
	for _, p := range append(paths, ".gitattributes", "feature") {
		b, e := os.ReadFile(filepath.Join(root, p)) // #nosec G304 -- test fixture.
		if e != nil {
			t.Fatal(e)
		}
		snapshots[p] = string(b)
	}
	plans := []DefectPlan{{ID: id, CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), CurrentMerged: true, MergeAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), MergeSHA: merged}}
	var commands []string
	runner := func(ctx context.Context, cwd, name string, args []string, out, stderr io.Writer) error {
		commands = append(commands, strings.Join(args, " "))
		var diagnostic bytes.Buffer
		err := commandrunner.DefaultLocal(ctx, cwd, name, args, out, io.MultiWriter(stderr, &diagnostic))
		if err != nil {
			t.Logf("probe %q: %s (%v)", args, diagnostic.String(), err)
		}
		return err
	}
	c := NewGitDefectCollector("repo", root, notes, runner)
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	report, err := c.CollectDefects(context.Background(), plans, now)
	if err != nil {
		t.Fatal(err)
	}
	if report.Availability != DefectAvailable || report.Coverage.Scan.PinnedTip != fix || report.Count.Numerator != 1 || report.Plans[0].Methods.Line != 1 {
		t.Fatalf("%+v plans %+v", report, report.Plans)
	}
	repeated, err := c.CollectDefects(context.Background(), plans, now)
	if err != nil || !reflect.DeepEqual(report, repeated) {
		t.Fatalf("nondeterministic %+v %v", repeated, err)
	}
	if git("rev-parse", "HEAD") != head {
		t.Fatal("HEAD changed")
	}
	after, _ := os.ReadFile(filepath.Join(root, ".git", "index")) // #nosec G304 -- test fixture.
	if !reflect.DeepEqual(index, after) {
		t.Fatal("index changed")
	}
	for p, want := range snapshots {
		b, e := os.ReadFile(filepath.Join(root, p)) // #nosec G304 -- test fixture.
		if e != nil || string(b) != want {
			t.Fatal("worktree changed", p)
		}
	}
	for _, cmd := range commands {
		for _, bad := range []string{" fetch ", " add ", " checkout ", " update-index "} {
			if strings.Contains(cmd, bad) {
				t.Fatal(cmd)
			}
		}
	}
	// Unknown last-touch trailers must not search back to the tagged implementation.
	git("switch", "main")
	write(paths[0], "new\nkeep\n")
	git("add", "--", paths[0])
	git("commit", "-m", "chore: untagged touch")
	write(paths[0], "repaired\nkeep\n")
	git("add", "--", paths[0])
	git("commit", "-m", "fix: untagged repair")
	report, err = c.CollectDefects(context.Background(), plans, now)
	if err != nil || report.Count.Numerator != 1 || report.Coverage.Untagged == 0 {
		t.Fatalf("%+v %v", report, err)
	}
	// A valid explicit assertion overrides the untagged heuristic.
	write(paths[0], "explicit\nkeep\n")
	git("add", "--", paths[0])
	git("commit", "-m", fmt.Sprintf("fix: explicit repair\n\nFixes-Plan: %s", id))
	report, err = c.CollectDefects(context.Background(), plans, now)
	if err != nil || report.Count.Numerator != 2 || report.Plans[0].Methods.Explicit != 1 {
		t.Fatalf("%+v %v", report, err)
	}
	// The parent-side last file touch is untagged after the first binary fix:
	// do not skip it to rediscover the tagged implementation.
	write(paths[2], "\x00again")
	git("add", "--", paths[2])
	git("commit", "-m", "fix: binary again")
	// A separate tagged binary file demonstrates weak file fallback.
	write("weak", "\x00old")
	git("add", "--", "weak")
	git("commit", "-m", "feat: binary\n\nTao-Plan: "+id)
	write("weak", "\x00new")
	git("add", "--", "weak")
	git("commit", "-m", "fix: binary fallback")
	for i, legacy := range []bool{false, true} {
		source := fmt.Sprintf("20260102-00000%d-source", i)
		n := note.Note{Schema: note.Schema, ID: fmt.Sprintf("note%d", i), Repo: note.RepoReference{ID: "repo"}, CreatedAt: now, UpdatedAt: now, Status: note.StatusArchived, Text: "Fixes-Plan: " + id, Tags: []string{" BUG "}, Archive: &note.ArchiveMetadata{Plan: &note.PlanLink{ID: source}}}
		if legacy {
			n.Status = note.StatusPromoted
			n.Archive = nil
			n.Promotion = &note.PromotionLinks{Plan: &note.PlanLink{ID: source}}
		}
		data, e := json.Marshal(n)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(notes, n.ID+".json"), data, 0600); e != nil {
			t.Fatal(e)
		}
		write(paths[0], fmt.Sprintf("note %d\n", i))
		git("add", "--", paths[0])
		git("commit", "-m", "chore: note repair\n\nTao-Plan: "+source)
	}
	report, err = c.CollectDefects(context.Background(), plans, now)
	if err != nil || report.Count.Numerator != 5 || report.Plans[0].Methods.File != 1 || report.Plans[0].Methods.Explicit != 3 {
		t.Fatalf("%+v plans %+v %v", report, report.Plans, err)
	}
	// No rename detection: delete/add retains weak or unavailable evidence.
	git("mv", "--", "weak", "renamed")
	git("commit", "-m", "fix: rename")
	git("switch", "-c", "side")
	write("side", "side\n")
	git("add", "--", "side")
	git("commit", "-m", "chore: side")
	git("switch", "main")
	git("merge", "--no-ff", "side", "-m", "fix: merge repair")
	report, err = c.CollectDefects(context.Background(), plans, now)
	if err != nil || report.Coverage.Unsupported != 2 {
		t.Fatalf("%+v %v", report, err)
	}
	future, e := c.CollectDefects(context.Background(), plans, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
	if e != nil || future.Coverage.FutureFixes == 0 {
		t.Fatalf("%+v %v", future, e)
	}
	git("config", "blame.ignoreRevsFile", "/definitely/not/a/file")
	report, err = c.CollectDefects(context.Background(), plans, now)
	if err != nil || report.Availability != DefectIncomplete {
		t.Fatalf("unsafe config must reduce coverage: %+v %v", report, err)
	}
	git("config", "--unset", "blame.ignoreRevsFile")
	if e = os.WriteFile(filepath.Join(root, ".git", "shallow"), []byte(merged+"\n"), 0600); e != nil {
		t.Fatal(e)
	}
	report, err = c.CollectDefects(context.Background(), plans, now)
	if err != nil || report.Availability != DefectIncomplete || !report.Coverage.Scan.MissingHistory {
		t.Fatalf("%+v %v", report, err)
	}
}
