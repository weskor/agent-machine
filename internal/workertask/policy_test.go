package workertask

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/weskor/agent-machine/internal/state"
)

func TestRetryBackoffByRole(t *testing.T) {
	for _, tc := range []struct {
		role string
		want time.Duration
	}{
		{role: RoleImplementation, want: 5 * time.Minute},
		{role: RoleWork, want: 5 * time.Minute},
		{role: RoleReview, want: 2 * time.Minute},
		{role: RoleHandoff, want: 2 * time.Minute},
		{role: RoleMerge, want: 2 * time.Minute},
		{role: RoleScheduler, want: time.Minute},
		{role: RoleCleanup, want: time.Minute},
		{role: RoleLinearStatus, want: time.Minute},
		{role: RoleReconciliation, want: time.Minute},
		{role: RoleStatus, want: time.Minute},
		{role: RolePlan, want: time.Minute},
		{role: "unknown", want: time.Minute},
	} {
		if got := RetryBackoff(tc.role); got != tc.want {
			t.Fatalf("RetryBackoff(%q) = %s, want %s", tc.role, got, tc.want)
		}
	}
}

func TestAvailableAtAfterLatestFailureUsesFinishedAtBackoff(t *testing.T) {
	ctx := context.Background()
	store, err := state.Open(ctx, filepath.Join(t.TempDir(), "am.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	finishedAt := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	taskKey := "implementation:CAG-242:1"
	if err := store.UpsertWorkerResult(ctx, state.WorkerResult{TaskKey: taskKey, Role: RoleImplementation, IssueKey: "CAG-242", Attempt: 1, Status: state.WorkerTaskStatusFailed, StartedAt: finishedAt.Add(-time.Minute), FinishedAt: finishedAt, UpdatedAt: finishedAt}); err != nil {
		t.Fatal(err)
	}

	got, err := AvailableAtAfterLatestFailure(ctx, store, taskKey, RoleImplementation, finishedAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	want := finishedAt.Add(5 * time.Minute)
	if !got.Equal(want) {
		t.Fatalf("AvailableAtAfterLatestFailure() = %s, want %s", got, want)
	}
}

func TestAvailableAtAfterLatestFailureReturnsNowWhenBackoffElapsed(t *testing.T) {
	ctx := context.Background()
	store, err := state.Open(ctx, filepath.Join(t.TempDir(), "am.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	finishedAt := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	taskKey := "review:CAG-242:1"
	now := finishedAt.Add(10 * time.Minute)
	if err := store.UpsertWorkerResult(ctx, state.WorkerResult{TaskKey: taskKey, Role: RoleReview, IssueKey: "CAG-242", Attempt: 1, Status: state.WorkerTaskStatusFailed, StartedAt: finishedAt.Add(-time.Minute), FinishedAt: finishedAt, UpdatedAt: finishedAt}); err != nil {
		t.Fatal(err)
	}

	got, err := AvailableAtAfterLatestFailure(ctx, store, taskKey, RoleReview, now)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(now) {
		t.Fatalf("AvailableAtAfterLatestFailure() = %s, want now %s", got, now)
	}
}
