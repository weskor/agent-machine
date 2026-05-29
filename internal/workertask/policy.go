package workertask

import (
	"context"
	"time"

	"github.com/weskor/agent-machine/internal/state"
)

const (
	RoleStatus         = "status"
	RolePlan           = "plan"
	RoleScheduler      = "scheduler"
	RoleCleanup        = "cleanup"
	RoleMerge          = "merge"
	RoleReconciliation = "reconciliation"
	RoleReview         = "review"
	RoleImplementation = "implementation"
	RoleHandoff        = "handoff"
	RoleLinearStatus   = "linear-status"
	RoleWork           = "work"
)

func RetryBackoff(role string) time.Duration {
	switch role {
	case RoleImplementation, RoleWork:
		return 5 * time.Minute
	case RoleReview, RoleHandoff, RoleMerge:
		return 2 * time.Minute
	case RoleScheduler, RoleCleanup, RoleLinearStatus, RoleReconciliation, RoleStatus, RolePlan:
		return time.Minute
	default:
		return time.Minute
	}
}

func AvailableAtAfterLatestFailure(ctx context.Context, store *state.Store, taskKey, role string, now time.Time) (time.Time, error) {
	if store == nil || taskKey == "" || role == "" {
		return now, nil
	}
	results, err := store.WorkerResults(ctx, role)
	if err != nil {
		return time.Time{}, err
	}
	for _, result := range results {
		if result.TaskKey != taskKey {
			continue
		}
		if result.Status != state.WorkerTaskStatusFailed {
			return now, nil
		}
		finishedAt := result.FinishedAt
		if finishedAt.IsZero() {
			finishedAt = result.UpdatedAt
		}
		availableAt := finishedAt.Add(RetryBackoff(role))
		if availableAt.After(now) {
			return availableAt, nil
		}
		return now, nil
	}
	return now, nil
}
