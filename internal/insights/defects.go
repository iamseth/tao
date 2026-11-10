package insights

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"time"
)

// DefectCollector supplies advisory evidence only; it confers no lifecycle authority.
type DefectCollector interface {
	CollectDefects(context.Context, []DefectPlan, time.Time) (DefectReport, error)
}

// DefectCount is a count quotient, not a binomial rate. Zero denominators have
// quotient zero; availability must be consulted before interpreting that value.
type DefectCount struct {
	Numerator   int
	Denominator int
	Quotient    float64
}

func defectCount(n, d int) DefectCount {
	c := DefectCount{Numerator: n, Denominator: d}
	if d > 0 {
		c.Quotient = float64(n) / float64(d)
	}
	return c
}

type DefectInput struct {
	RepositoryID string
	Now          time.Time
	Plans        []DefectPlan
	Fixes        []DefectFix
	Notes        []DefectNote
	Coverage     DefectScan
}

type DefectPlan struct {
	ID            string
	CreatedAt     time.Time
	CurrentMerged bool
	MergeAt       time.Time
	MergeSHA      string
	PlannerLabel  string
}

// DefectNote contains only same-repository persisted links. Planning-session
// links alone do not qualify. Text is untrusted and never appears in reports.
type DefectNote struct {
	RepositoryID          string
	ArchivePlanID         string
	LegacyPromotionPlanID string
	Tags                  []string
	Text                  string
}

// DefectFix is a pinned commit observation. AncestorPlanIDs contains only plans
// whose merge SHA was proven ancestral to Parent, never guesses from timestamps.
// References and TaoPlanIDs are parsed trailer values (not arbitrary body text).
// Message may instead supply a complete commit message for trailer parsing.
type DefectFix struct {
	SHA, Parent            string
	CommitterAt            time.Time
	Subject, Message       string
	TaoPlanIDs, References []string
	AncestorPlanIDs        []string
	Paths                  []DefectPath
	Unsupported            bool
}

// DefectPath describes last touches, not a search through earlier history.
// File candidates are admissible only when there are no usable old lines.
// An empty PlanID marks an untagged last touch and must not be walked past.
type DefectPath struct {
	HasUsableOldLines bool
	Lines             []DefectCandidate
	File              []DefectCandidate
}

type DefectCandidate struct {
	PlanID   string
	Ancestor bool
}

type DefectScan struct {
	Usable, Complete                    bool
	PinnedTip                           string
	Truncated, TimedOut, MissingHistory bool
}

type DefectCoverage struct {
	Scan                                                      DefectScan
	Attribution                                               DefectCount
	Examined, Attributed, Untagged, Unattributed, Unsupported int
	InvalidReferences, FutureFixes                            int
}

type DefectAvailability string

const (
	DefectAvailable   DefectAvailability = "available"
	DefectIncomplete  DefectAvailability = "incomplete"
	DefectUnavailable DefectAvailability = "unavailable"
	DefectUnknown     DefectAvailability = "unknown"
	DefectImmature    DefectAvailability = "immature"
)

type DefectMethod string

const (
	DefectExplicit DefectMethod = "explicit"
	DefectLine     DefectMethod = "line"
	DefectFile     DefectMethod = "file"
)

type DefectConfidence string

const (
	DefectAssertion     DefectConfidence = "assertion"
	DefectLowConfidence DefectConfidence = "low"
)

type DefectAttribution struct {
	FixSHA      string
	CommitterAt time.Time
	Method      DefectMethod
	Confidence  DefectConfidence
}

type DefectMethods struct{ Explicit, Line, File int }

type DefectPlanResult struct {
	ID, CreationMonth string
	Planner           PlannerTreatment
	MergeAt           time.Time
	Mature            bool
	Availability      DefectAvailability
	WindowFixes       int
	Methods           DefectMethods
	Fixes, LaterFixes []DefectAttribution
}

// DefectReport deliberately excludes raw subjects, note text, paths and errors.
// Count includes only mature, known plans, and is usable only with Availability.
type DefectReport struct {
	RepositoryID string
	ObservedAt   time.Time
	Plans        []DefectPlanResult
	Coverage     DefectCoverage
	Count        DefectCount
	Availability DefectAvailability
}

var defectPlanID = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}-[a-z0-9]+(-[a-z0-9]+)*$`)
var defectFixSubject = regexp.MustCompile(`^fix(\([^()\r\n]+\))?!?:[ \t]+[^ \t\r\n][^\r\n]*$`)
var defectTrailerKey = regexp.MustCompile(`^[A-Za-z0-9-]+$`)
var defectHash = regexp.MustCompile(`^[a-fA-F0-9]{1,64}$`)

// ParseDefectTrailers reads only a terminal, blank-line-separated trailer block.
// Continuations belong to their trailer and cannot introduce a new reference.
func ParseDefectTrailers(message string) (taoPlans, references []string) {
	lines := strings.Split(strings.TrimRight(message, "\r\n"), "\n")
	start := len(lines) - 1
	for start >= 0 && strings.TrimSpace(lines[start]) != "" {
		start--
	}
	if start < 0 {
		return nil, nil
	}
	var key, value string
	flush := func() {
		switch key {
		case "Tao-Plan":
			taoPlans = append(taoPlans, value)
		case "Fixes-Plan":
			references = append(references, value)
		}
	}
	for _, line := range lines[start+1:] {
		line = strings.TrimSuffix(line, "\r")
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			if key == "" {
				return nil, nil
			}
			value += "\n" + line
			continue
		}
		flush()
		k, v, ok := strings.Cut(line, ":")
		if !ok || !defectTrailerKey.MatchString(k) {
			return nil, nil
		}
		key, value = k, strings.TrimSpace(v)
	}
	flush()
	return taoPlans, references
}

// DefectNoteReferences accepts only standalone lines, never URLs or prose.
func DefectNoteReferences(text string) []string {
	var refs []string
	for _, line := range strings.Split(text, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSuffix(line, "\r"), "Fixes-Plan:"); ok {
			refs = append(refs, strings.TrimSpace(v))
		}
	}
	return refs
}

func defectContains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

func defectKnown(p DefectPlan) bool {
	return p.CurrentMerged && !p.MergeAt.IsZero() && defectHash.MatchString(p.MergeSHA)
}

// AttributeDefects is deterministic and does not mutate its inputs. Explicit
// assertions override heuristics, but never override merge/ancestry eligibility.
func AttributeDefects(input DefectInput) DefectReport {
	scan := input.Coverage
	if !defectHash.MatchString(scan.PinnedTip) {
		scan.PinnedTip = ""
	}
	if scan.Truncated || scan.TimedOut || scan.MissingHistory {
		scan.Complete = false
	}
	r := DefectReport{RepositoryID: sanitizeTreatmentLabel(input.RepositoryID), ObservedAt: input.Now.UTC(), Coverage: DefectCoverage{Scan: scan}, Availability: DefectAvailable}
	if !scan.Usable {
		r.Availability = DefectUnavailable
	} else if !scan.Complete {
		r.Availability = DefectIncomplete
	}
	plans := make(map[string]DefectPlan)
	// Ambiguous duplicate plan records are not evidence.
	duplicates := make(map[string]bool)
	for _, p := range input.Plans {
		if !defectPlanID.MatchString(p.ID) {
			continue
		}
		if _, ok := plans[p.ID]; ok {
			duplicates[p.ID] = true
		}
		plans[p.ID] = p
	}
	for id := range duplicates {
		delete(plans, id)
	}
	ids := make([]string, 0, len(plans))
	for id := range plans {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	results := make(map[string]*DefectPlanResult)
	for _, id := range ids {
		p := plans[id]
		v := &DefectPlanResult{ID: id, Planner: NormalizePlannerLabel(p.PlannerLabel), MergeAt: p.MergeAt.UTC(), Availability: r.Availability}
		if !p.CreatedAt.IsZero() {
			v.CreationMonth = p.CreatedAt.UTC().Format("2006-01")
		}
		v.Mature = defectKnown(p) && !input.Now.Before(p.MergeAt.Add(30*24*time.Hour))
		if scan.Usable {
			if !defectKnown(p) {
				v.Availability = DefectUnknown
			} else if !v.Mature {
				v.Availability = DefectImmature
			}
		}
		results[id] = v
	}
	if scan.Usable {
		attributeDefectFixes(input, plans, results, &r.Coverage)
	}
	n, d := 0, 0
	for _, id := range ids {
		v := results[id]
		sortDefectDetails(v.Fixes)
		sortDefectDetails(v.LaterFixes)
		if scan.Usable && v.Mature {
			n += v.WindowFixes
			d++
		}
		r.Plans = append(r.Plans, *v)
	}
	r.Count = defectCount(n, d)
	if scan.Usable && d == 0 {
		r.Availability = DefectUnknown
	}
	r.Coverage.Attribution = defectCount(r.Coverage.Attributed, r.Coverage.Examined)
	return r
}

func sortDefectDetails(v []DefectAttribution) {
	sort.Slice(v, func(i, j int) bool {
		if !v[i].CommitterAt.Equal(v[j].CommitterAt) {
			return v[i].CommitterAt.Before(v[j].CommitterAt)
		}
		return v[i].FixSHA < v[j].FixSHA
	})
}

func attributeDefectFixes(input DefectInput, plans map[string]DefectPlan, results map[string]*DefectPlanResult, coverage *DefectCoverage) {
	// Repeated observations union their evidence. Conflicting immutable commit
	// facts are unsupported rather than resolved by input order.
	fixes := make(map[string]DefectFix)
	conflicting := make(map[string]bool)
	for _, f := range input.Fixes {
		if old, ok := fixes[f.SHA]; ok {
			if old.Parent != f.Parent || !old.CommitterAt.Equal(f.CommitterAt) || old.Subject != f.Subject || old.Message != f.Message {
				conflicting[f.SHA] = true
			}
			old.TaoPlanIDs = append(append([]string(nil), old.TaoPlanIDs...), f.TaoPlanIDs...)
			old.References = append(append([]string(nil), old.References...), f.References...)
			old.AncestorPlanIDs = append(append([]string(nil), old.AncestorPlanIDs...), f.AncestorPlanIDs...)
			old.Paths = append(append([]DefectPath(nil), old.Paths...), f.Paths...)
			old.Unsupported = old.Unsupported || f.Unsupported
			fixes[f.SHA] = old
		} else {
			fixes[f.SHA] = f
		}
	}
	for sha, f := range fixes {
		coverage.Examined++
		if conflicting[sha] || f.Unsupported || !defectHash.MatchString(f.SHA) || !defectHash.MatchString(f.Parent) || f.CommitterAt.IsZero() {
			coverage.Unsupported++
			continue
		}
		if f.CommitterAt.After(input.Now) {
			coverage.FutureFixes++
			continue
		}
		sources := append([]string(nil), f.TaoPlanIDs...)
		refs := append([]string(nil), f.References...)
		tp, rp := ParseDefectTrailers(f.Message)
		sources = append(sources, tp...)
		refs = append(refs, rp...)
		bug := false
		for _, note := range input.Notes {
			if note.RepositoryID != input.RepositoryID {
				continue
			}
			linked := false
			for _, id := range []string{note.ArchivePlanID, note.LegacyPromotionPlanID} {
				if defectPlanID.MatchString(id) && defectContains(sources, id) {
					linked = true
				}
			}
			if linked {
				bug = bug || defectContains(note.Tags, "bug")
				refs = append(refs, DefectNoteReferences(note.Text)...)
			}
		}
		if !bug && !defectFixSubject.MatchString(f.Subject) {
			coverage.Unattributed++
			continue
		}
		eligible := func(id string) bool {
			p, ok := plans[id]
			return ok && !defectContains(sources, id) && defectKnown(p) && !p.CreatedAt.After(f.CommitterAt) && !p.MergeAt.After(f.CommitterAt) && defectContains(f.AncestorPlanIDs, id)
		}
		targets := make(map[string]DefectMethod)
		seenRefs := make(map[string]bool)
		for _, ref := range refs {
			if seenRefs[ref] {
				continue
			}
			seenRefs[ref] = true
			if !defectPlanID.MatchString(ref) || !eligible(ref) {
				coverage.InvalidReferences++
				continue
			}
			targets[ref] = DefectExplicit
		}
		untagged := false
		if len(targets) == 0 {
			for _, path := range f.Paths {
				candidates, method := path.File, DefectFile
				if path.HasUsableOldLines {
					candidates, method = path.Lines, DefectLine
				}
				for _, candidate := range candidates {
					if candidate.PlanID == "" {
						untagged = true
						continue
					}
					if !candidate.Ancestor || !eligible(candidate.PlanID) {
						continue
					}
					if targets[candidate.PlanID] != DefectLine {
						targets[candidate.PlanID] = method
					}
				}
			}
		}
		if untagged {
			coverage.Untagged++
		}
		if len(targets) == 0 {
			coverage.Unattributed++
			continue
		}
		coverage.Attributed++
		for id, method := range targets {
			v := results[id]
			detail := DefectAttribution{FixSHA: f.SHA, CommitterAt: f.CommitterAt.UTC(), Method: method, Confidence: DefectLowConfidence}
			if method == DefectExplicit {
				detail.Confidence = DefectAssertion
			}
			if !f.CommitterAt.Before(plans[id].MergeAt.Add(30 * 24 * time.Hour)) {
				v.LaterFixes = append(v.LaterFixes, detail)
				continue
			}
			v.WindowFixes++
			v.Fixes = append(v.Fixes, detail)
			switch method {
			case DefectExplicit:
				v.Methods.Explicit++
			case DefectLine:
				v.Methods.Line++
			case DefectFile:
				v.Methods.File++
			}
		}
	}
}
