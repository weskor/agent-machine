package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/weskor/pi-symphony/internal/state"
)

func implementationWorkerTaskKey(issueKey string, attempt int) string {
	if attempt <= 0 {
		attempt = 1
	}
	return fmt.Sprintf("%s:%s:%d", implementationWorkerRole, issueKey, attempt)
}

func claimImplementationWorkerTask(ctx context.Context, store *state.Store, candidate *issue, workspace, branch string, now time.Time) (state.WorkerTask, bool, error) {
	task, enqueued, err := enqueueImplementationWorkerTask(ctx, store, candidate, workspace, branch, now)
	if err != nil {
		return task, false, err
	}
	if !enqueued && task.Status != "queued" {
		return task, false, nil
	}
	return claimQueuedImplementationWorkerTask(ctx, store, task, now)
}

func enqueueImplementationWorkerTask(ctx context.Context, store *state.Store, candidate *issue, workspace, branch string, now time.Time) (state.WorkerTask, bool, error) {
	if store == nil || candidate == nil {
		return state.WorkerTask{}, true, nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	attempt := 1
	taskKey := implementationWorkerTaskKey(candidate.Identifier, attempt)
	tasks, err := store.WorkerTasks(ctx, implementationWorkerRole)
	if err != nil {
		return state.WorkerTask{}, false, err
	}
	for _, task := range tasks {
		if task.TaskKey != taskKey {
			continue
		}
		if workerTaskBlocksDispatch(task.Status) {
			return task, false, nil
		}
		break
	}
	availableAt, err := workerTaskAvailableAtAfterLatestFailure(ctx, store, taskKey, implementationWorkerRole, now)
	if err != nil {
		return state.WorkerTask{}, false, err
	}
	payloadBranch := branch
	if payloadBranch == "" {
		payloadBranch = expectedWorkspaceBranch(candidate.Identifier)
	}
	payload, err := json.Marshal(map[string]any{
		"phase":     "implementation",
		"issue_key": candidate.Identifier,
		"workspace": workspace,
		"branch":    payloadBranch,
	})
	if err != nil {
		return state.WorkerTask{}, false, fmt.Errorf("encode implementation worker task payload: %w", err)
	}
	task := state.WorkerTask{
		TaskKey:     taskKey,
		Role:        implementationWorkerRole,
		IssueKey:    candidate.Identifier,
		IssueID:     candidate.ID,
		Attempt:     attempt,
		Status:      "queued",
		Priority:    candidate.Priority,
		AvailableAt: availableAt,
		LeaseName:   "worker:implementation:" + candidate.Identifier,
		Payload:     payload,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := store.UpsertWorkerTask(ctx, task); err != nil {
		return state.WorkerTask{}, false, err
	}
	recordContinuousWorkerTaskEvent(ctx, store, state.EventWorkerTaskQueued, task, map[string]any{"lane": "implementation", "issue_key": candidate.Identifier})
	return task, true, nil
}

func claimQueuedImplementationWorkerTask(ctx context.Context, store *state.Store, task state.WorkerTask, now time.Time) (state.WorkerTask, bool, error) {
	claimed, ok, err := store.ClaimWorkerTask(ctx, task.TaskKey, now)
	if err != nil || !ok {
		return claimed, ok, err
	}
	recordContinuousWorkerTaskEvent(ctx, store, state.EventWorkerTaskClaimed, claimed, map[string]any{"lane": "implementation", "issue_key": claimed.IssueKey})
	return claimed, true, nil
}

func claimNextQueuedImplementationWorkerTask(ctx context.Context, store *state.Store, now time.Time) (state.WorkerTask, bool, error) {
	if store == nil {
		return state.WorkerTask{}, false, nil
	}
	claimed, ok, err := store.ClaimNextWorkerTask(ctx, implementationWorkerRole, now)
	if err != nil || !ok {
		return claimed, ok, err
	}
	recordContinuousWorkerTaskEvent(ctx, store, state.EventWorkerTaskClaimed, claimed, map[string]any{"lane": "implementation", "issue_key": claimed.IssueKey})
	return claimed, true, nil
}

func completeClaimedImplementationWorkerTask(ctx context.Context, store *state.Store, task state.WorkerTask, status string, didWork bool, reason, errorText string, startedAt, finishedAt time.Time) error {
	if store == nil || task.TaskKey == "" {
		return nil
	}
	if status == "" {
		status = "completed"
	}
	if reason == "" {
		reason = "attempt_completed"
	}
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	if finishedAt.IsZero() {
		finishedAt = time.Now().UTC()
	}
	completeErr := store.CompleteWorkerTask(ctx, task.TaskKey, status, finishedAt)
	payload := map[string]any{
		"lane":      "implementation",
		"task_key":  task.TaskKey,
		"role":      implementationWorkerRole,
		"status":    status,
		"reason":    reason,
		"did_work":  didWork,
		"issue_key": task.IssueKey,
	}
	if errorText != "" {
		payload["error"] = errorText
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode implementation worker result payload: %w", err)
	}
	resultErr := store.UpsertWorkerResult(ctx, state.WorkerResult{
		TaskKey:    task.TaskKey,
		Role:       implementationWorkerRole,
		LaneName:   "implementation",
		IssueKey:   task.IssueKey,
		IssueID:    task.IssueID,
		Attempt:    task.Attempt,
		Status:     status,
		DidWork:    didWork,
		Reason:     reason,
		Error:      errorText,
		Payload:    payloadJSON,
		StartedAt:  startedAt,
		FinishedAt: finishedAt,
		UpdatedAt:  finishedAt,
	})
	eventType := state.EventWorkerTaskCompleted
	if status == "failed" {
		eventType = state.EventWorkerTaskFailed
	}
	task.Status = status
	task.UpdatedAt = finishedAt
	recordContinuousWorkerTaskEvent(ctx, store, eventType, task, payload)
	return errors.Join(completeErr, resultErr)
}
