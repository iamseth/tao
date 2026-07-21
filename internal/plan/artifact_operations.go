package plan

import (
	"context"
	"io"
	"os"
)

// artifactOperations owns the shared repository operations that directly read,
// write, append, or mutate plan artifacts through an artifactStore.
type artifactOperations struct {
	store *artifactStore
}

func (o artifactOperations) artifacts() artifactStore {
	if o.store != nil && *o.store != nil {
		return *o.store
	}
	return fileArtifactStore{}
}

func (o artifactOperations) ReadLog(planDir string) (string, error) {
	return o.artifacts().readLog(planDir)
}

func (o artifactOperations) ReadLogTail(planDir string, tail int) (string, error) {
	return o.artifacts().readLogTail(planDir, tail)
}

func (o artifactOperations) FollowLog(ctx context.Context, planDir string, out io.Writer) error {
	return o.artifacts().followLog(ctx, planDir, out)
}

func (o artifactOperations) OpenLogAppend(planDir string) (*os.File, error) {
	return o.artifacts().openLogAppend(planDir)
}

func (o artifactOperations) writeState(planDir string, state State) error {
	return o.artifacts().writeState(planDir, state)
}

func (o artifactOperations) writeSlices(planDir string, slices SlicesFile) error {
	return o.artifacts().writeSlices(planDir, slices)
}

func (o artifactOperations) AppendEvent(planDir string, event Event) error {
	return o.artifacts().appendEvent(planDir, event)
}

func (o artifactOperations) record(planDir string, detail *PlanDetail) (*PlanRecord, error) {
	return newPlanRecord(o.artifacts(), planDir, detail)
}
