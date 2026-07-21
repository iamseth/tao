package plan

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileRepositoryArtifactAndEditMethods(t *testing.T) {
	root := t.TempDir()
	writeEditPlan(t, root)
	planDir := filepath.Join(root, "edit")
	repo := NewFileRepository(root)
	detail, err := repo.ResolvePlan(context.Background(), "edit")
	if err != nil {
		t.Fatal(err)
	}

	log, err := repo.OpenLogAppend(planDir)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(log, "first\nsecond\n")
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	if text, err := repo.ReadLog(planDir); err != nil || text != "first\nsecond\n" {
		t.Fatalf("ReadLog = %q, %v", text, err)
	}
	if tail, err := repo.ReadLogTail(planDir, 1); err != nil || tail != "second\n" {
		t.Fatalf("ReadLogTail = %q, %v", tail, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var followed bytes.Buffer
	if err := repo.FollowLog(ctx, planDir, &followed); !errors.Is(err, context.Canceled) || followed.String() != "first\nsecond\n" {
		t.Fatalf("FollowLog = %q, %v", followed.String(), err)
	}

	state := detail.State
	state.Plan.Title = "Changed"
	if err := repo.writeState(planDir, state); err != nil {
		t.Fatal(err)
	}
	slices := detail.Slices
	slices.Slices[0].Title = "Changed A"
	if err := repo.writeSlices(planDir, slices); err != nil {
		t.Fatal(err)
	}
	if err := repo.AppendEvent(planDir, Event{Type: "manual", Timestamp: editTime(), PlanID: "edit"}); err != nil {
		t.Fatal(err)
	}
	loaded, err := repo.ResolvePlan(context.Background(), "edit")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State.Plan.Title != "Changed" || loaded.Slices.Slices[0].Title != "Changed A" || len(loaded.Events) != 1 {
		t.Fatalf("artifact methods did not persist: state=%#v slice=%#v events=%#v", loaded.State.Plan, loaded.Slices.Slices[0], loaded.Events)
	}

	if err := testRepoRecord(repo, loaded).RemoveSlice("003-c", editTime()); err != nil {
		t.Fatal(err)
	}
	if findSlice(loaded, "003-c") != nil {
		t.Fatalf("RemoveSlice left slice in detail")
	}
	if err := testRepoRecord(repo, loaded).ReorderPendingSlices([]string{"001-a", "002-b"}, editTime()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(loaded.State.Plan.PendingSlices, ","); got != "001-a,002-b" {
		t.Fatalf("unexpected pending order %q", got)
	}
	loaded.Slices.Slices[0].Approval = &Approval{Required: true, Reason: "approval"}
	record := testRepoRecord(repo, loaded)
	if err := record.PersistArtifacts(); err != nil {
		t.Fatal(err)
	}
	if err := record.ApproveSlice("001-a", "Seth", editTime()); err != nil {
		t.Fatal(err)
	}
	if findSlice(loaded, "001-a").Approval == nil || !findSlice(loaded, "001-a").Approval.Approved {
		t.Fatalf("ApproveSlice did not update detail: %#v", findSlice(loaded, "001-a"))
	}
	loaded.State.Status = StatusBlocked
	loaded.Slices.Slices[0].Status = StatusBlocked
	record = testRepoRecord(repo, loaded)
	if err := record.PersistArtifacts(); err != nil {
		t.Fatal(err)
	}
	if err := record.ContinueBlocked(editTime()); err != nil {
		t.Fatal(err)
	}
	if loaded.State.Status == StatusBlocked || findSlice(loaded, "001-a").Status == StatusBlocked {
		t.Fatalf("ContinueBlocked did not unblock detail")
	}
}

func TestFileRepositoryRejectsInvalidRemoveSliceWithoutPersisting(t *testing.T) {
	root := t.TempDir()
	writeEditPlan(t, root)
	repo := NewFileRepository(root)
	detail, err := repo.ResolvePlan(context.Background(), "edit")
	if err != nil {
		t.Fatal(err)
	}
	beforePending := strings.Join(detail.State.Plan.PendingSlices, ",")
	beforeUpdated := detail.State.UpdatedAt

	err = testRepoRecord(repo, detail).RemoveSlice("001-a", editTime())
	if err == nil || !strings.Contains(err.Error(), "pending slices depend on it") {
		t.Fatalf("expected dependent rejection, got %v", err)
	}
	if got := strings.Join(detail.State.Plan.PendingSlices, ","); got != beforePending || !detail.State.UpdatedAt.Equal(beforeUpdated) || findSlice(detail, "001-a") == nil {
		t.Fatalf("invalid RemoveSlice changed in-memory detail: pending=%v updated=%v slices=%#v", detail.State.Plan.PendingSlices, detail.State.UpdatedAt, detail.Slices.Slices)
	}
	reloaded, err := repo.ResolvePlan(context.Background(), "edit")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(reloaded.State.Plan.PendingSlices, ","); got != beforePending || len(reloaded.Events) != 0 || findSlice(reloaded, "001-a") == nil {
		t.Fatalf("invalid RemoveSlice persisted changes: pending=%v events=%#v slices=%#v", reloaded.State.Plan.PendingSlices, reloaded.Events, reloaded.Slices.Slices)
	}
}
