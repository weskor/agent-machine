package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/weskor/agent-machine/internal/state"
)

type cleanupWorkerTaskPayload struct {
	Phase         string `json:"phase"`
	WorkspaceName string `json:"workspace_name"`
	WorkspacePath string `json:"workspace_path"`
}

func cleanupWorkerTaskKey(workspaceName string) string {
	return fmt.Sprintf("%s:%s", cleanupWorkerRole, workspaceName)
}

func scheduleCleanupWorkerTasks(ctx context.Context, config runnerConfig, store *state.Store) (bool, error) {
	if store == nil {
		return false, fmt.Errorf("SQLite state store unavailable for cleanup scheduling at %s", state.DefaultDBPath(config.WorkspaceRoot))
	}
	safeRoot, err := safeWorkspaceRoot(config.WorkspaceRoot)
	if err != nil {
		return false, err
	}
	entries, err := os.ReadDir(safeRoot)
	if err != nil {
		return false, err
	}
	didWork := false
	now := time.Now().UTC()
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || entry.Name() == "state" {
			continue
		}
		workspace, err := safeWorkspacePath(safeRoot, entry.Name())
		if err != nil {
			return didWork, err
		}
		if _, enqueued, err := enqueueCleanupWorkerTask(ctx, store, entry.Name(), workspace, now); err != nil {
			return didWork, err
		} else if enqueued {
			didWork = true
		}
	}
	return didWork, nil
}

func enqueueCleanupWorkerTask(ctx context.Context, store *state.Store, workspaceName, workspace string, now time.Time) (state.WorkerTask, bool, error) {
	if store == nil {
		return state.WorkerTask{}, false, nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	taskKey := cleanupWorkerTaskKey(workspaceName)
	tasks, err := store.WorkerTasks(ctx, cleanupWorkerRole)
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
	availableAt, err := workerTaskAvailableAtAfterLatestFailure(ctx, store, taskKey, cleanupWorkerRole, now)
	if err != nil {
		return state.WorkerTask{}, false, err
	}
	payload, err := json.Marshal(cleanupWorkerTaskPayload{
		Phase:         "cleanup",
		WorkspaceName: workspaceName,
		WorkspacePath: workspace,
	})
	if err != nil {
		return state.WorkerTask{}, false, fmt.Errorf("encode cleanup worker task payload: %w", err)
	}
	task := state.WorkerTask{
		TaskKey:     taskKey,
		Role:        cleanupWorkerRole,
		IssueKey:    workspaceName,
		Attempt:     1,
		Status:      "queued",
		AvailableAt: availableAt,
		LeaseName:   "worker:cleanup:" + workspaceName,
		Payload:     payload,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := store.UpsertWorkerTask(ctx, task); err != nil {
		return state.WorkerTask{}, false, err
	}
	recordContinuousWorkerTaskEvent(ctx, store, state.EventWorkerTaskQueued, task, map[string]any{"lane": "cleanup", "workspace": workspace})
	return task, true, nil
}

func claimNextQueuedCleanupWorkerTask(ctx context.Context, store *state.Store, now time.Time) (state.WorkerTask, bool, error) {
	if store == nil {
		return state.WorkerTask{}, false, nil
	}
	claimed, ok, err := store.ClaimNextWorkerTask(ctx, cleanupWorkerRole, now)
	if err != nil || !ok {
		return claimed, ok, err
	}
	if claimed.TaskKey == "continuous:cleanup" || claimed.TaskKey == "process:cleanup" {
		if err := store.CompleteWorkerTask(ctx, claimed.TaskKey, "completed", now); err != nil {
			return state.WorkerTask{}, false, err
		}
		return state.WorkerTask{}, false, nil
	}
	recordContinuousWorkerTaskEvent(ctx, store, state.EventWorkerTaskClaimed, claimed, map[string]any{"lane": "cleanup", "issue_key": claimed.IssueKey})
	return claimed, true, nil
}

func runQueuedCleanupWorkerTask(client linearClient, config runnerConfig, store *state.Store) (bool, error) {
	return runQueuedCleanupWorkerTaskContext(context.Background(), client, config, store)
}

func runQueuedCleanupWorkerTaskContext(ctx context.Context, client linearClient, config runnerConfig, store *state.Store) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if store == nil {
		return false, fmt.Errorf("SQLite state store unavailable for cleanup worker at %s", state.DefaultDBPath(config.WorkspaceRoot))
	}
	now := time.Now().UTC()
	task, ok, err := claimNextQueuedCleanupWorkerTask(ctx, store, now)
	if err != nil || !ok {
		return false, err
	}
	startedAt := time.Now().UTC()
	payload := cleanupWorkerTaskPayload{WorkspaceName: task.IssueKey}
	if len(task.Payload) > 0 {
		_ = json.Unmarshal(task.Payload, &payload)
	}
	finish := func(status, reason, errorText string) error {
		return completeClaimedCleanupWorkerTask(ctx, store, task, status, true, reason, errorText, startedAt, time.Now().UTC())
	}
	safeRoot, err := safeWorkspaceRoot(config.WorkspaceRoot)
	if err != nil {
		return true, errors.Join(err, finish("failed", "unsafe_workspace_root", err.Error()))
	}
	workspaceName := firstNonEmpty(payload.WorkspaceName, task.IssueKey)
	workspace, err := safeWorkspacePath(safeRoot, workspaceName)
	if err != nil {
		return true, errors.Join(err, finish("failed", "unsafe_workspace_path", err.Error()))
	}
	if payload.WorkspacePath != "" && filepath.Clean(payload.WorkspacePath) != filepath.Clean(workspace) {
		err := fmt.Errorf("cleanup task workspace %s conflicts with expected workspace %s", payload.WorkspacePath, workspace)
		return true, errors.Join(err, finish("failed", "workspace_path_conflict", err.Error()))
	}
	doneIssues, err := issueIdentifiersByStateForContinuousCleanup(ctx, client, config.ProjectSlug, config.DoneState)
	if err != nil {
		return true, errors.Join(err, finish("failed", "linear_done_refresh_failed", err.Error()))
	}
	if _, err := os.Stat(workspace); err != nil {
		if os.IsNotExist(err) {
			return true, finish("completed", "workspace_missing", "")
		}
		return true, errors.Join(err, finish("failed", "workspace_stat_failed", err.Error()))
	}
	decision, err := cleanupDecisionForWorkspace(ctx, safeRoot, workspace, doneIssues, store, workspaceHasChanges)
	if err != nil {
		recordCleanupErrorContext(ctx, store, cleanupResult{IssueIdentifier: workspaceName, WorkspacePath: workspace}, err)
		return true, errors.Join(err, finish("failed", "cleanup_decision_failed", err.Error()))
	}
	recordCleanupEventContext(ctx, store, state.EventCleanupCandidateFound, decision, map[string]any{"reason": decision.Reason, "category": decision.Category, "delete": decision.Delete})
	if !decision.Delete {
		mirrorCleanupState(store, safeRoot, decision, false, cleanupDeletionResult(decision, "kept"), true)
		recordCleanupEventContext(ctx, store, state.EventCleanupSkipped, decision, map[string]any{"reason": decision.Reason, "category": decision.Category})
		log("keep %s [%s]: %s", workspace, decision.Category, decision.Reason)
		return true, finish("completed", "cleanup_kept", "")
	}
	recordCleanupEventContext(ctx, store, state.EventCleanupDeletionAttempted, decision, map[string]any{"reason": decision.Reason, "category": decision.Category})
	if err := assertSafeDeletePath(safeRoot, workspace); err != nil {
		mirrorCleanupState(store, safeRoot, decision, true, "failed", true)
		recordCleanupErrorContext(ctx, store, decision, err)
		return true, errors.Join(err, finish("failed", "cleanup_delete_unsafe", err.Error()))
	}
	if err := os.RemoveAll(workspace); err != nil {
		mirrorCleanupState(store, safeRoot, decision, true, "failed", true)
		recordCleanupErrorContext(ctx, store, decision, err)
		return true, errors.Join(err, finish("failed", "cleanup_delete_failed", err.Error()))
	}
	mirrorCleanupState(store, safeRoot, decision, true, "deleted", false)
	recordCleanupEventContext(ctx, store, state.EventCleanupDeletionSucceeded, decision, map[string]any{"reason": decision.Reason, "category": decision.Category})
	log("deleted %s [%s]: %s", workspace, decision.Category, decision.Reason)
	return true, finish("completed", "cleanup_deleted", "")
}

func completeClaimedCleanupWorkerTask(ctx context.Context, store *state.Store, task state.WorkerTask, status string, didWork bool, reason, errorText string, startedAt, finishedAt time.Time) error {
	if store == nil || task.TaskKey == "" {
		return nil
	}
	if status == "" {
		status = "completed"
	}
	if reason == "" {
		reason = "cleanup_task_completed"
	}
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	if finishedAt.IsZero() {
		finishedAt = time.Now().UTC()
	}
	completeErr := store.CompleteWorkerTask(ctx, task.TaskKey, status, finishedAt)
	payload := map[string]any{
		"lane":      "cleanup",
		"task_key":  task.TaskKey,
		"role":      cleanupWorkerRole,
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
		return fmt.Errorf("encode cleanup worker result payload: %w", err)
	}
	resultErr := store.UpsertWorkerResult(ctx, state.WorkerResult{
		TaskKey:    task.TaskKey,
		Role:       cleanupWorkerRole,
		LaneName:   "cleanup",
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
