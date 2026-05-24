package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/weskor/pi-symphony/internal/state"
)

func reviewReadyWorkerTaskKey(issueKey string, attempt int) string {
	if attempt <= 0 {
		attempt = 1
	}
	return fmt.Sprintf("%s:%s:%d:resume", reviewWorkerRole, issueKey, attempt)
}

func enqueueReviewReadyWorkerTask(ctx context.Context, store *state.Store, candidate issue, pr pullRequestSummary, workspace string, now time.Time) (state.WorkerTask, bool, error) {
	if store == nil {
		return state.WorkerTask{}, false, nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	taskKey := reviewReadyWorkerTaskKey(candidate.Identifier, 1)
	tasks, err := store.WorkerTasks(ctx, reviewWorkerRole)
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
	availableAt, err := workerTaskAvailableAtAfterLatestFailure(ctx, store, taskKey, reviewWorkerRole, now)
	if err != nil {
		return state.WorkerTask{}, false, err
	}
	payload, err := json.Marshal(map[string]any{
		"phase":     "review_resume",
		"issue_key": candidate.Identifier,
		"workspace": workspace,
		"pr_url":    pr.URL,
	})
	if err != nil {
		return state.WorkerTask{}, false, fmt.Errorf("encode review worker task payload: %w", err)
	}
	task := state.WorkerTask{
		TaskKey:     taskKey,
		Role:        reviewWorkerRole,
		IssueKey:    candidate.Identifier,
		IssueID:     candidate.ID,
		Attempt:     1,
		Status:      "queued",
		Priority:    candidate.Priority,
		AvailableAt: availableAt,
		LeaseName:   "worker:review:" + candidate.Identifier,
		Payload:     payload,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := store.UpsertWorkerTask(ctx, task); err != nil {
		return state.WorkerTask{}, false, err
	}
	recordContinuousWorkerTaskEvent(ctx, store, state.EventWorkerTaskQueued, task, map[string]any{"lane": "review", "issue_key": candidate.Identifier, "pr_url": pr.URL})
	return task, true, nil
}

func claimNextQueuedReviewReadyWorkerTask(ctx context.Context, store *state.Store, now time.Time) (state.WorkerTask, bool, error) {
	if store == nil {
		return state.WorkerTask{}, false, nil
	}
	claimed, ok, err := store.ClaimNextWorkerTask(ctx, reviewWorkerRole, now)
	if err != nil || !ok {
		return claimed, ok, err
	}
	if claimed.TaskKey == "continuous:review" || claimed.TaskKey == "process:review" {
		if err := store.CompleteWorkerTask(ctx, claimed.TaskKey, "completed", now); err != nil {
			return state.WorkerTask{}, false, err
		}
		return state.WorkerTask{}, false, nil
	}
	recordContinuousWorkerTaskEvent(ctx, store, state.EventWorkerTaskClaimed, claimed, map[string]any{"lane": "review", "issue_key": claimed.IssueKey})
	return claimed, true, nil
}

func completeClaimedReviewReadyWorkerTask(ctx context.Context, store *state.Store, task state.WorkerTask, status string, didWork bool, reason, errorText string, startedAt, finishedAt time.Time) error {
	if store == nil || task.TaskKey == "" {
		return nil
	}
	if status == "" {
		status = "completed"
	}
	if reason == "" {
		reason = "review_resume_completed"
	}
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	if finishedAt.IsZero() {
		finishedAt = time.Now().UTC()
	}
	completeErr := store.CompleteWorkerTask(ctx, task.TaskKey, status, finishedAt)
	payload := map[string]any{
		"lane":      "review",
		"task_key":  task.TaskKey,
		"role":      reviewWorkerRole,
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
		return fmt.Errorf("encode review worker result payload: %w", err)
	}
	resultErr := store.UpsertWorkerResult(ctx, state.WorkerResult{
		TaskKey:    task.TaskKey,
		Role:       reviewWorkerRole,
		LaneName:   "review",
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
