package plan

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
)

func (r *FileRepository) GetPlan(ctx context.Context, id string) (*PlanDetail, error) {
	candidate, err := resolvePlanCandidate(ctx, r.artifacts(), r.Dir, id, planResolutionOptions{})
	if err != nil {
		return nil, err
	}
	return r.loadPlanDir(candidate.Dir)
}

func (r *FileRepository) GetPlanInput(ctx context.Context, input string) (*PlanDetail, error) {
	return r.ResolvePlan(ctx, input)
}

func (r *FileRepository) ResolvePlan(ctx context.Context, input string) (*PlanDetail, error) {
	candidate, err := resolvePlanCandidate(ctx, r.artifacts(), r.Dir, input, planResolutionOptions{allowPathInput: true})
	if err != nil {
		return nil, err
	}
	return r.loadPlanDir(candidate.Dir)
}

func (r *FileRepository) resolveDeletePlanDir(ctx context.Context, input string) (string, string, error) {
	candidate, err := resolvePlanCandidate(ctx, r.artifacts(), r.Dir, input, planResolutionOptions{allowPathInput: true})
	if err != nil {
		return "", "", err
	}
	return filepath.Clean(candidate.Dir), candidate.ID, nil
}

func (r *FileRepository) safeDeletePlanDir(dir string) (string, error) {
	if dir == "" {
		return "", errors.New("plan directory is required")
	}
	root := r.Dir
	if root == "" {
		root = DefaultDir()
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	targetAbs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	rootAbs = filepath.Clean(rootAbs)
	targetAbs = filepath.Clean(targetAbs)
	if targetAbs == rootAbs {
		return "", classify(ErrInvalid, "refusing to delete plans root %q", rootAbs)
	}
	if filepath.Dir(targetAbs) != rootAbs {
		return "", classify(ErrInvalid, "refusing to delete unrelated path %q", targetAbs)
	}
	return targetAbs, nil
}

func PlanSlug(id string) (string, bool) {
	first := strings.IndexByte(id, '-')
	if first != 8 || !digits(id[:first]) {
		return "", false
	}
	second := strings.IndexByte(id[first+1:], '-')
	if (second != 4 && second != 6) || !digits(id[first+1:first+1+second]) {
		return "", false
	}
	slug := id[first+1+second+1:]
	return slug, slug != ""
}

func digits(value string) bool {
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return value != ""
}
