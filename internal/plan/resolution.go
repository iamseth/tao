package plan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type planResolutionCandidate struct {
	ID  string
	Dir string
}

type planResolutionOptions struct {
	allowPathInput bool
}

func resolveImmediatePlanCandidate(ctx context.Context, store artifactStore, input string, opts planResolutionOptions) (planResolutionCandidate, bool, error) {
	if err := ctx.Err(); err != nil {
		return planResolutionCandidate{}, true, err
	}
	if input == "" {
		if opts.allowPathInput {
			return planResolutionCandidate{}, true, errors.New("plan input is required")
		}
		return planResolutionCandidate{}, true, classify(ErrInvalid, "invalid plan id %q", input)
	}

	if opts.allowPathInput {
		if dir, ok, err := store.inputDir(input); err != nil {
			return planResolutionCandidate{}, true, err
		} else if ok {
			return planResolutionCandidate{ID: filepath.Base(dir), Dir: dir}, true, nil
		}
		if strings.Contains(input, string(os.PathSeparator)) {
			return planResolutionCandidate{}, true, classify(ErrNotFound, "plan path %q not found", input)
		}
	} else if strings.Contains(input, string(os.PathSeparator)) {
		return planResolutionCandidate{}, true, classify(ErrInvalid, "invalid plan id %q", input)
	}

	return planResolutionCandidate{}, false, nil
}

func resolvePlanCandidate(ctx context.Context, store artifactStore, root, input string, opts planResolutionOptions) (planResolutionCandidate, error) {
	if candidate, done, err := resolveImmediatePlanCandidate(ctx, store, input, opts); done || err != nil {
		return candidate, err
	}
	return resolvePlanIDCandidate(ctx, store, root, input)
}

func resolvePlanIDCandidate(ctx context.Context, store artifactStore, root, id string) (planResolutionCandidate, error) {
	if store.isDir(filepath.Join(root, id)) {
		return planResolutionCandidate{ID: id, Dir: filepath.Join(root, id)}, nil
	}

	entries, err := store.listDirs(ctx, root)
	if err != nil {
		return planResolutionCandidate{}, err
	}

	candidates := make([]planResolutionCandidate, 0, len(entries))
	for _, entry := range entries {
		candidates = append(candidates, planResolutionCandidate(entry))
	}
	return selectPlanResolutionCandidate(id, candidates)
}

func selectPlanResolutionCandidate(input string, candidates []planResolutionCandidate) (planResolutionCandidate, error) {
	matches := matchingPlanCandidates(candidates, func(candidate planResolutionCandidate) bool {
		return candidate.ID == input
	})
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return planResolutionCandidate{}, ambiguousPlanResolutionError(input, "id", matches)
	}

	matches = matchingPlanCandidates(candidates, func(candidate planResolutionCandidate) bool {
		return strings.HasPrefix(candidate.ID, input)
	})
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return planResolutionCandidate{}, ambiguousPlanResolutionError(input, "id", matches)
	}

	matches = matchingPlanCandidates(candidates, func(candidate planResolutionCandidate) bool {
		slug, ok := PlanSlug(candidate.ID)
		return ok && strings.HasPrefix(slug, input)
	})
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return planResolutionCandidate{}, ambiguousPlanResolutionError(input, "slug", matches)
	}

	return planResolutionCandidate{}, classify(ErrNotFound, "plan %q not found", input)
}

func matchingPlanCandidates(candidates []planResolutionCandidate, match func(planResolutionCandidate) bool) []planResolutionCandidate {
	matches := make([]planResolutionCandidate, 0, 1)
	for _, candidate := range candidates {
		if match(candidate) {
			matches = append(matches, candidate)
		}
	}
	return matches
}

func ambiguousPlanResolutionError(input, kind string, matches []planResolutionCandidate) error {
	ids := make([]string, 0, len(matches))
	for _, candidate := range matches {
		ids = append(ids, candidate.ID)
	}
	sort.Strings(ids)
	if kind == "slug" {
		return classify(ErrInvalid, "plan slug %q is ambiguous: %s", input, strings.Join(ids, ", "))
	}
	return classify(ErrInvalid, "plan id %q is ambiguous: %s", input, strings.Join(ids, ", "))
}
