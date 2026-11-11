package note

import (
	"fmt"
	"strings"

	"github.com/iamseth/tao/internal/taodata"
)

// Readiness check statuses. Unknown is distinct from fail: the evaluator could
// not classify the input, and the verdict stays not ready.
const (
	ReadinessPass    = "pass"
	ReadinessFail    = "fail"
	ReadinessUnknown = "unknown"
)

// ReadinessMarkers are fixed lowercase substrings whose presence in note text
// marks a pending owner decision. Matching is a plain heuristic: retrospective
// prose that merely mentions an owner decision also triggers it.
var ReadinessMarkers = []string{"owner decision", "needs scope", "needs owner"}

// ReadinessTierTags are the literal groom tier tags; exactly one is expected.
var ReadinessTierTags = []string{"tier0", "tier1", "tier2", "tier3"}

// FixSiteCheck reports whether one fix site named by a note exists in the
// registered checkout. Kind is one of path, identifier, or qualified.
type FixSiteCheck struct {
	Name   string
	Kind   string
	Found  bool
	Detail string
}

// ReadinessCheck is one named advisory check. Status is one of
// ReadinessPass, ReadinessFail, or ReadinessUnknown.
type ReadinessCheck struct {
	Name    string
	Status  string
	Reasons []string
}

// ReadinessInput bundles already-resolved evidence; the evaluator performs no
// I/O. Notes is the repository-scoped set used to look up referenced notes,
// References come from ResolveReferences, Health from the repo catalog check,
// and FixSites or FixSiteError from the fix-site existence probe.
type ReadinessInput struct {
	Note         Note
	Notes        []Note
	References   []ReferenceLookup
	Health       taodata.RepoHealth
	FixSites     []FixSiteCheck
	FixSiteError string
}

// Readiness is an advisory planning verdict. It is never lifecycle, approval,
// recovery, execution, or merge authority and never gates tao note run. Ready
// is true only when every check passed.
type Readiness struct {
	Ready  bool
	Checks []ReadinessCheck
}

// EvaluateReadiness classifies a note for unattended promotion. Checks are
// returned in a fixed order: status, references, fix_sites, tier, markers,
// repository. The function reads no link directories and tolerates nil slices.
func EvaluateReadiness(in ReadinessInput) Readiness {
	checks := []ReadinessCheck{
		statusCheck(in.Note),
		referencesCheck(in.References, in.Notes),
		fixSitesCheck(in.FixSites, in.FixSiteError),
		tierCheck(in.Note.Tags),
		markersCheck(in.Note.Text),
		repositoryCheck(in.Health),
	}
	ready := true
	for _, c := range checks {
		if c.Status != ReadinessPass {
			ready = false
			break
		}
	}
	return Readiness{Ready: ready, Checks: checks}
}

func statusCheck(item Note) ReadinessCheck {
	if item.Status == StatusOpen {
		return ReadinessCheck{Name: "status", Status: ReadinessPass}
	}
	reason := fmt.Sprintf("note status is %s, not open", item.Status)
	planID := ""
	if item.Archive != nil && item.Archive.Plan != nil {
		planID = item.Archive.Plan.ID
	}
	if planID == "" && item.Promotion != nil && item.Promotion.Plan != nil {
		planID = item.Promotion.Plan.ID
	}
	if planID != "" {
		reason += fmt.Sprintf("; linked to plan %s (tao note run resumes the linked plan)", planID)
	}
	return ReadinessCheck{Name: "status", Status: ReadinessFail, Reasons: []string{reason}}
}

func referencesCheck(refs []ReferenceLookup, notes []Note) ReadinessCheck {
	check := ReadinessCheck{Name: "references", Status: ReadinessPass}
	if len(refs) == 0 {
		check.Reasons = []string{"no note references"}
		return check
	}
	failed, unknown := false, false
	for _, ref := range refs {
		res := ref.Plan.Resolution
		if ref.NoteID == "" {
			unknown = true
			check.Reasons = append(check.Reasons, fmt.Sprintf("reference %s is %s", ref.Reference, res))
			continue
		}
		if found := findNote(notes, ref.NoteID); found != nil && found.Status == StatusOpen {
			failed = true
			check.Reasons = append(check.Reasons, fmt.Sprintf("prerequisite note %s is open", ref.NoteID))
			continue
		}
		switch res {
		case "resolved":
			if ref.Plan.PlanMerged {
				continue
			}
			failed = true
			check.Reasons = append(check.Reasons, fmt.Sprintf("note %s plan %s is %s, not merged", ref.NoteID, ref.Plan.Destination, ref.Plan.Status))
		default:
			unknown = true
			check.Reasons = append(check.Reasons, fmt.Sprintf("note %s plan lookup is %s", ref.NoteID, res))
		}
	}
	switch {
	case failed:
		check.Status = ReadinessFail
	case unknown:
		check.Status = ReadinessUnknown
	}
	return check
}

func findNote(notes []Note, id string) *Note {
	for i := range notes {
		if notes[i].ID == id {
			return &notes[i]
		}
	}
	return nil
}

func fixSitesCheck(sites []FixSiteCheck, probeErr string) ReadinessCheck {
	check := ReadinessCheck{Name: "fix_sites", Status: ReadinessPass}
	if probeErr != "" {
		check.Status = ReadinessUnknown
		check.Reasons = []string{probeErr}
		return check
	}
	if len(sites) == 0 {
		check.Status = ReadinessFail
		check.Reasons = []string{"no concrete fix site named (backticked Go identifier or repository-relative path)"}
		return check
	}
	for _, site := range sites {
		if site.Found {
			continue
		}
		reason := fmt.Sprintf("%s %s not found in checkout", site.Kind, site.Name)
		if site.Detail != "" {
			reason += ": " + site.Detail
		}
		check.Reasons = append(check.Reasons, reason)
	}
	if len(check.Reasons) > 0 {
		check.Status = ReadinessFail
	}
	return check
}

func tierCheck(tags []string) ReadinessCheck {
	var tiers []string
	for _, tag := range tags {
		normalized := strings.ToLower(strings.TrimSpace(tag))
		for _, tier := range ReadinessTierTags {
			if normalized == tier {
				tiers = append(tiers, normalized)
				break
			}
		}
	}
	switch len(tiers) {
	case 1:
		return ReadinessCheck{Name: "tier", Status: ReadinessPass}
	case 0:
		return ReadinessCheck{Name: "tier", Status: ReadinessFail, Reasons: []string{"no tier tag"}}
	default:
		return ReadinessCheck{Name: "tier", Status: ReadinessFail, Reasons: []string{"conflicting tier tags: " + strings.Join(tiers, ", ")}}
	}
}

func markersCheck(text string) ReadinessCheck {
	check := ReadinessCheck{Name: "markers", Status: ReadinessPass}
	lowered := strings.ToLower(text)
	for _, marker := range ReadinessMarkers {
		if strings.Contains(lowered, marker) {
			check.Reasons = append(check.Reasons, fmt.Sprintf("note text contains %q", marker))
		}
	}
	if len(check.Reasons) > 0 {
		check.Status = ReadinessFail
	}
	return check
}

func repositoryCheck(health taodata.RepoHealth) ReadinessCheck {
	if !health.Error {
		return ReadinessCheck{Name: "repository", Status: ReadinessPass}
	}
	return ReadinessCheck{Name: "repository", Status: ReadinessFail, Reasons: []string{fmt.Sprintf("repository %s: %s", health.Status, health.Message)}}
}
