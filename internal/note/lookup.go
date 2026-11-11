package note

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/iamseth/tao/internal/agentinput"
	"github.com/iamseth/tao/internal/plan"
)

// PlanLookup is untrusted advisory context, never lifecycle or recovery evidence.
// Destination is an exact plan ID, never a stored directory or artifact path.
type PlanLookup struct {
	Destination string
	Status      string
	PlanMerged  bool
	Resolution  string
}

// ReferenceLookup projects one candidate reference without traversing note bodies.
type ReferenceLookup struct {
	Reference string
	NoteID    string
	Plan      PlanLookup
}

// LookupPlan resolves typed normal-plan links against repository-scoped summaries.
// Callers supply the repository boundary; this function never loads stored paths.
func LookupPlan(item Note, summaries []plan.PlanSummary) PlanLookup {
	result := PlanLookup{Resolution: "unlinked"}
	var links []*PlanLink
	if item.Archive != nil {
		links = append(links, item.Archive.Plan)
	}
	if item.Promotion != nil {
		links = append(links, item.Promotion.Plan)
	}
	for _, link := range links {
		if link == nil || link.ID == "" {
			continue
		}
		if result.Destination != "" && result.Destination != link.ID {
			return PlanLookup{Resolution: "ambiguous"}
		}
		result.Destination = link.ID
	}
	if result.Destination == "" {
		return result
	}
	var match *plan.PlanSummary
	for i := range summaries {
		if summaries[i].ID != result.Destination {
			continue
		}
		if match != nil {
			result.Resolution = "ambiguous"
			return result
		}
		match = &summaries[i]
	}
	if match == nil {
		result.Resolution = "missing"
		return result
	}
	if match.Status == "" || match.Status == plan.StatusInvalid || len(match.Warnings) > 0 {
		result.Resolution = "unavailable"
		return result
	}
	result.Resolution = "resolved"
	result.Status = match.Status
	result.PlanMerged = match.PlanMerged
	return result
}

var canonicalReference = regexp.MustCompile(`^(?:[0-9]{8}-[0-9]{6}-)?[0-9a-f]{8}$`)

const maxReferenceLookups = 128

// ResolveReferences recognizes whole canonical IDs and eight-hex suffix tokens,
// optionally prefixed with note:. Unique supplied note matches are advisory, not
// proof of a dependency (a bare suffix may also be a commit abbreviation).
// Oversized input is rejected; output is capped in first-mention order. URLs,
// paths, slugs and longer SHA tokens are not split into candidate references.
func ResolveReferences(text string, notes []Note, summaries []plan.PlanSummary) []ReferenceLookup {
	if int64(len(text)) > agentinput.MaxFileBytes {
		return nil
	}
	bounded, err := agentinput.BoundedText(text, "note references")
	if err != nil {
		return nil
	}
	tokens := strings.FieldsFunc(bounded, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune("()[]{}<>,;\"'`!?", r)
	})
	var result []ReferenceLookup
	seen := make(map[string]bool)
	for _, token := range tokens {
		reference := strings.TrimRight(token, ".")
		candidate := strings.TrimPrefix(reference, "note:")
		if !canonicalReference.MatchString(candidate) {
			continue
		}
		found := -1
		ambiguous := false
		for i := range notes {
			id := notes[i].ID
			if !canonicalReference.MatchString(id) || len(id) != 24 {
				continue
			}
			if id == candidate || (len(candidate) == 8 && strings.HasSuffix(id, "-"+candidate)) {
				if found >= 0 {
					ambiguous = true
					break
				}
				found = i
			}
		}
		lookup := ReferenceLookup{Reference: reference, Plan: PlanLookup{Resolution: "missing"}}
		key := candidate
		if ambiguous {
			lookup.Plan.Resolution = "ambiguous"
		} else if found >= 0 {
			lookup.NoteID = notes[found].ID
			key = lookup.NoteID
			lookup.Plan = LookupPlan(notes[found], summaries)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, lookup)
		if len(result) == maxReferenceLookups {
			break
		}
	}
	return result
}
