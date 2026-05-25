package main

import (
	"context"
	"time"

	"github.com/weskor/agent-machine/internal/state"
)

func workerTaskRetryBackoff(role string) time.Duration {
	switch role {
	case implementationWorkerRole, workWorkerRole:
		return 5 * time.Minute
	case reviewWorkerRole, handoffWorkerRole, mergeWorkerRole:
		return 2 * time.Minute
	case schedulerWorkerRole, cleanupWorkerRole, linearStatusWorkerRole, reconciliationWorkerRole, statusWorkerRole, planWorkerRole:
		return time.Minute
	default:
		return time.Minute
	}
}

func workerTaskAvailableAtAfterLatestFailure(ctx context.Context, store *state.Store, taskKey, role string, now time.Time) (time.Time, error) {
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
		availableAt := finishedAt.Add(workerTaskRetryBackoff(role))
		if availableAt.After(now) {
			return availableAt, nil
		}
		return now, nil
	}
	return now, nil
}
