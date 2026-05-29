package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/weskor/agent-machine/internal/state"
	"github.com/weskor/agent-machine/internal/workertask"
)

type mergeWorkerTaskPayload struct {
	Phase       string `json:"phase"`
	IssueKey    string `json:"issue_key"`
	PRNumber    int    `json:"pr_number"`
	PRURL       string `json:"pr_url"`
	HeadRefName string `json:"head_ref_name"`
	BaseRefName string `json:"base_ref_name"`
}

func mergeWorkerTaskKey(issueKey string, prNumber int) string {
	return fmt.Sprintf("%s:%s:%d", mergeWorkerRole, issueKey, prNumber)
}

func enqueueMergeWorkerTask(ctx context.Context, store *state.Store, candidate issue, pr pullRequestSummary, now time.Time) (state.WorkerTask, bool, error) {
	if store == nil {
		return state.WorkerTask{}, false, nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	taskKey := mergeWorkerTaskKey(candidate.Identifier, pr.Number)
	tasks, err := store.WorkerTasks(ctx, mergeWorkerRole)
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
	availableAt, err := workertask.AvailableAtAfterLatestFailure(ctx, store, taskKey, mergeWorkerRole, now)
	if err != nil {
		return state.WorkerTask{}, false, err
	}
	payload, err := json.Marshal(mergeWorkerTaskPayload{
		Phase:       "merge",
		IssueKey:    candidate.Identifier,
		PRNumber:    pr.Number,
		PRURL:       pr.URL,
		HeadRefName: pr.HeadRefName,
		BaseRefName: pr.BaseRefName,
	})
	if err != nil {
		return state.WorkerTask{}, false, fmt.Errorf("encode merge worker task payload: %w", err)
	}
	task := state.WorkerTask{
		TaskKey:     taskKey,
		Role:        mergeWorkerRole,
		IssueKey:    candidate.Identifier,
		IssueID:     candidate.ID,
		Attempt:     1,
		Status:      "queued",
		Priority:    candidate.Priority,
		AvailableAt: availableAt,
		LeaseName:   "worker:merge:" + candidate.Identifier,
		Payload:     payload,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := store.UpsertWorkerTask(ctx, task); err != nil {
		return state.WorkerTask{}, false, err
	}
	recordContinuousWorkerTaskEvent(ctx, store, state.EventWorkerTaskQueued, task, map[string]any{"lane": "merge", "issue_key": candidate.Identifier, "pr_url": pr.URL, "pr_number": pr.Number})
	return task, true, nil
}

func claimNextQueuedMergeWorkerTask(ctx context.Context, store *state.Store, now time.Time) (state.WorkerTask, bool, error) {
	if store == nil {
		return state.WorkerTask{}, false, nil
	}
	claimed, ok, err := store.ClaimNextWorkerTask(ctx, mergeWorkerRole, now)
	if err != nil || !ok {
		return claimed, ok, err
	}
	if claimed.TaskKey == "continuous:merge" || claimed.TaskKey == "process:merge" {
		if err := store.CompleteWorkerTask(ctx, claimed.TaskKey, "completed", now); err != nil {
			return state.WorkerTask{}, false, err
		}
		return state.WorkerTask{}, false, nil
	}
	recordContinuousWorkerTaskEvent(ctx, store, state.EventWorkerTaskClaimed, claimed, map[string]any{"lane": "merge", "issue_key": claimed.IssueKey})
	return claimed, true, nil
}

func completeClaimedMergeWorkerTask(ctx context.Context, store *state.Store, task state.WorkerTask, status string, didWork bool, reason, errorText string, startedAt, finishedAt time.Time) error {
	if store == nil || task.TaskKey == "" {
		return nil
	}
	if status == "" {
		status = "completed"
	}
	if reason == "" {
		reason = "merge_task_completed"
	}
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	if finishedAt.IsZero() {
		finishedAt = time.Now().UTC()
	}
	completeErr := store.CompleteWorkerTask(ctx, task.TaskKey, status, finishedAt)
	payload := map[string]any{
		"lane":      "merge",
		"task_key":  task.TaskKey,
		"role":      mergeWorkerRole,
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
		return fmt.Errorf("encode merge worker result payload: %w", err)
	}
	resultErr := store.UpsertWorkerResult(ctx, state.WorkerResult{
		TaskKey:    task.TaskKey,
		Role:       mergeWorkerRole,
		LaneName:   "merge",
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
