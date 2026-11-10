package insights

import (
	"reflect"
	"testing"
	"time"
)

const defectTarget = "20261001-120000-target"
const defectSource = "20261002-120000-source"

func defectFixture() DefectInput {
	merge := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	return DefectInput{RepositoryID: "repo", Now: merge.Add(40 * 24 * time.Hour), Coverage: DefectScan{Usable: true, Complete: true, PinnedTip: "abcdef"}, Plans: []DefectPlan{{ID: defectTarget, CreatedAt: merge.Add(-24 * time.Hour), CurrentMerged: true, MergeAt: merge, MergeSHA: "abc", PlannerLabel: "pi"}}, Fixes: []DefectFix{{SHA: "def", Parent: "abc", CommitterAt: merge.Add(time.Hour), Subject: "fix: regression", References: []string{defectTarget}, AncestorPlanIDs: []string{defectTarget}}}}
}

func TestAttributeDefectsContract(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		mutate                func(*DefectInput)
		count, later, invalid int
		state                 DefectAvailability
	}{
		{"explicit", func(*DefectInput) {}, 1, 0, 0, DefectAvailable},
		{"duplicates", func(i *DefectInput) {
			i.Fixes = append(i.Fixes, i.Fixes[0])
			i.Fixes[0].References = append(i.Fixes[0].References, defectTarget)
		}, 1, 0, 0, DefectAvailable},
		{"window end", func(i *DefectInput) { i.Fixes[0].CommitterAt = i.Plans[0].MergeAt.Add(30 * 24 * time.Hour) }, 0, 1, 0, DefectAvailable},
		{"just inside", func(i *DefectInput) {
			i.Fixes[0].CommitterAt = i.Plans[0].MergeAt.Add(30*24*time.Hour - time.Nanosecond)
		}, 1, 0, 0, DefectAvailable},
		{"merge edge", func(i *DefectInput) { i.Fixes[0].CommitterAt = i.Plans[0].MergeAt }, 1, 0, 0, DefectAvailable},
		{"before merge", func(i *DefectInput) { i.Fixes[0].CommitterAt = i.Plans[0].MergeAt.Add(-time.Second) }, 0, 0, 1, DefectAvailable},
		{"future", func(i *DefectInput) { i.Fixes[0].CommitterAt = i.Now.Add(time.Second) }, 0, 0, 0, DefectAvailable},
		{"no ancestry", func(i *DefectInput) { i.Fixes[0].AncestorPlanIDs = nil }, 0, 0, 1, DefectAvailable},
		{"self", func(i *DefectInput) { i.Fixes[0].TaoPlanIDs = []string{defectTarget} }, 0, 0, 1, DefectAvailable},
		{"future plan", func(i *DefectInput) { i.Plans[0].CreatedAt = i.Now }, 0, 0, 1, DefectAvailable},
		{"missing merge", func(i *DefectInput) { i.Plans[0].MergeAt = time.Time{} }, 0, 0, 1, DefectUnknown},
		{"missing sha", func(i *DefectInput) { i.Plans[0].MergeSHA = "" }, 0, 0, 1, DefectUnknown},
		{"legacy completion", func(i *DefectInput) { i.Plans[0].CurrentMerged = false }, 0, 0, 1, DefectUnknown},
		{"partial", func(i *DefectInput) { i.Coverage.Complete = false }, 1, 0, 0, DefectIncomplete},
		{"truncated", func(i *DefectInput) { i.Coverage.Truncated = true }, 1, 0, 0, DefectIncomplete},
		{"unavailable", func(i *DefectInput) { i.Coverage.Usable = false }, 0, 0, 0, DefectUnavailable},
		{"immature", func(i *DefectInput) { i.Now = i.Plans[0].MergeAt.Add(30*24*time.Hour - time.Nanosecond) }, 1, 0, 0, DefectImmature},
		{"maturity edge", func(i *DefectInput) { i.Now = i.Plans[0].MergeAt.Add(30 * 24 * time.Hour) }, 1, 0, 0, DefectAvailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := defectFixture()
			tc.mutate(&in)
			got := AttributeDefects(in)
			p := got.Plans[0]
			if p.WindowFixes != tc.count || len(p.LaterFixes) != tc.later || got.Coverage.InvalidReferences != tc.invalid || p.Availability != tc.state {
				t.Fatalf("report: %+v plan: %+v", got, p)
			}
		})
	}
}

func TestDefectSyntax(t *testing.T) {
	for _, tc := range []struct {
		subject string
		want    bool
	}{{"fix: bug", true}, {"fix(api)!: bug", true}, {"fix!: bug", true}, {"fix(api): bug", true}, {"prefix fix: bug", false}, {"Fix: bug", false}, {"fix(): bug", false}, {"fix: ", false}, {"fix:\n bug", false}, {"feat: bug fix", false}} {
		t.Run(tc.subject, func(t *testing.T) {
			in := defectFixture()
			in.Fixes[0].Subject = tc.subject
			got := AttributeDefects(in)
			if (got.Plans[0].WindowFixes == 1) != tc.want {
				t.Fatalf("%+v", got)
			}
		})
	}
}

func TestDefectDeterminismAndCount(t *testing.T) {
	in := defectFixture()
	second := in.Fixes[0]
	second.SHA = "fed"
	in.Fixes = append(in.Fixes, second)
	before := defectFixture()
	before.Fixes = append(before.Fixes, second)
	a := AttributeDefects(in)
	if !reflect.DeepEqual(in, before) {
		t.Fatal("mutated input")
	}
	in.Fixes[0], in.Fixes[1] = in.Fixes[1], in.Fixes[0]
	if !reflect.DeepEqual(a, AttributeDefects(in)) {
		t.Fatal("order dependent")
	}
	if a.Count.Numerator != 2 || a.Count.Denominator != 1 || a.Count.Quotient != 2 {
		t.Fatalf("count %+v", a.Count)
	}
	if a.Plans[0].CreationMonth != "2026-09" || a.Plans[0].Planner.Label != "pi" {
		t.Fatal("cohort changed")
	}
}

func TestDefectTrailersAndNotes(t *testing.T) {
	for _, tc := range []struct {
		name, message string
		want          int
	}{
		{"trailers", "fix: x\n\nTao-Plan: " + defectSource + "\nFixes-Plan: " + defectTarget + "\n", 1},
		{"body", "fix: x\n\nFixes-Plan: " + defectTarget + "\n\nordinary body", 0},
		{"subject", "Fixes-Plan: " + defectTarget, 0},
		{"prose", "fix: x\n\nmentions Fixes-Plan: " + defectTarget, 0},
		{"continuation", "fix: x\n\nOther: x\n Fixes-Plan: " + defectTarget, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, refs := ParseDefectTrailers(tc.message)
			if len(refs) != tc.want {
				t.Fatalf("refs %v", refs)
			}
		})
	}
	for _, tc := range []struct {
		name, tag, repo string
		legacy          bool
		want            int
	}{
		{"current", "bug", "repo", false, 1}, {"legacy", "bug", "repo", true, 1}, {"exact tag", "bugs", "repo", false, 0}, {"case", "BUG", "repo", false, 0}, {"prose not tag", "", "repo", false, 0}, {"cross repo", "bug", "elsewhere", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := defectFixture()
			f := &in.Fixes[0]
			f.Subject = "feat: repair"
			f.References = nil
			f.Message = "feat: repair\n\nTao-Plan: " + defectSource
			note := DefectNote{RepositoryID: tc.repo, Tags: []string{tc.tag}, Text: "bug in prose\nFixes-Plan: " + defectTarget + "\nprose Fixes-Plan: invalid\nhttps://example.org"}
			if tc.legacy {
				note.LegacyPromotionPlanID = defectSource
			} else {
				note.ArchivePlanID = defectSource
			}
			in.Notes = []DefectNote{note, note}
			got := AttributeDefects(in)
			if got.Plans[0].WindowFixes != tc.want {
				t.Fatalf("%+v", got)
			}
		})
	}
}

func TestDefectHeuristicPrecedenceAndInvalidReferences(t *testing.T) {
	for _, tc := range []struct {
		name              string
		refs              []string
		old               bool
		lines, file       []DefectCandidate
		method            DefectMethod
		invalid, untagged int
	}{
		{"explicit wins", []string{defectTarget}, true, []DefectCandidate{{PlanID: defectSource, Ancestor: true}}, nil, DefectExplicit, 0, 0},
		{"line", nil, true, []DefectCandidate{{PlanID: defectTarget, Ancestor: true}}, nil, DefectLine, 0, 0},
		{"file", nil, false, nil, []DefectCandidate{{PlanID: defectTarget, Ancestor: true}}, DefectFile, 0, 0},
		{"untagged last touch", nil, true, []DefectCandidate{{}}, []DefectCandidate{{PlanID: defectTarget, Ancestor: true}}, "", 0, 1},
		{"no proven touch", nil, true, []DefectCandidate{{PlanID: defectTarget}}, nil, "", 0, 0},
		{"invalid fallback", []string{"repo/" + defectTarget, "20261001", "https://x/" + defectTarget, "$(command)", "unknown", "unknown"}, true, []DefectCandidate{{PlanID: defectTarget, Ancestor: true}}, nil, DefectLine, 5, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := defectFixture()
			in.Fixes[0].References = tc.refs
			in.Fixes[0].Paths = []DefectPath{{HasUsableOldLines: tc.old, Lines: tc.lines, File: tc.file}}
			got := AttributeDefects(in)
			if got.Coverage.InvalidReferences != tc.invalid || got.Coverage.Untagged != tc.untagged {
				t.Fatalf("coverage %+v", got.Coverage)
			}
			details := got.Plans[0].Fixes
			if tc.method == "" {
				if len(details) != 0 {
					t.Fatal(details)
				}
				return
			}
			if len(details) != 1 || details[0].Method != tc.method {
				t.Fatal(details)
			}
			confidence := DefectLowConfidence
			if tc.method == DefectExplicit {
				confidence = DefectAssertion
			}
			if details[0].Confidence != confidence {
				t.Fatal(details)
			}
		})
	}
}

func TestDefectInvalidReferenceSurvivesExplicitSuccess(t *testing.T) {
	in := defectFixture()
	in.Fixes[0].References = append(in.Fixes[0].References, "other-repo/"+defectTarget, defectSource, defectSource)
	got := AttributeDefects(in)
	if got.Plans[0].WindowFixes != 1 || got.Coverage.InvalidReferences != 2 {
		t.Fatal(got)
	}
	// Complete scans with no candidates are distinguishable from absent scans.
	in.Fixes = nil
	got = AttributeDefects(in)
	if got.Availability != DefectAvailable || got.Count.Numerator != 0 || got.Count.Denominator != 1 || got.Coverage.Attribution.Quotient != 0 {
		t.Fatal(got)
	}
}

func TestDefectMultipleSourcesTargetsAndCoverage(t *testing.T) {
	in := defectFixture()
	p := in.Plans[0]
	p.ID = "20260930-120000-other"
	in.Plans = append(in.Plans, p)
	f := &in.Fixes[0]
	f.TaoPlanIDs = []string{defectSource, "20261003-120000-source"}
	f.AncestorPlanIDs = append(f.AncestorPlanIDs, p.ID)
	in.Notes = []DefectNote{{RepositoryID: "repo", ArchivePlanID: defectSource, Text: "Fixes-Plan: " + p.ID}, {RepositoryID: "repo", LegacyPromotionPlanID: f.TaoPlanIDs[1], Text: "Fixes-Plan: " + defectTarget}}
	a := AttributeDefects(in)
	if a.Count.Numerator != 2 || a.Coverage.Attributed != 1 || a.Coverage.Attribution.Quotient != 1 {
		t.Fatalf("%+v", a)
	}
	in.Plans[0], in.Plans[1] = in.Plans[1], in.Plans[0]
	in.Notes[0], in.Notes[1] = in.Notes[1], in.Notes[0]
	if !reflect.DeepEqual(a, AttributeDefects(in)) {
		t.Fatal("plan/note order dependent")
	}
	future := in.Fixes[0]
	future.SHA = "aaa"
	future.CommitterAt = in.Now.Add(time.Second)
	unsupported := in.Fixes[0]
	unsupported.SHA = "bbb"
	unsupported.Unsupported = true
	in.Fixes = append(in.Fixes, future, unsupported)
	got := AttributeDefects(in)
	if got.Coverage.FutureFixes != 1 || got.Coverage.Unsupported != 1 || got.Coverage.Attribution.Denominator != 3 {
		t.Fatal(got.Coverage)
	}
	in.Coverage.Usable = false
	got = AttributeDefects(in)
	if got.Count.Denominator != 0 || got.Count.Quotient != 0 || got.Availability != DefectUnavailable {
		t.Fatal(got)
	}
}
