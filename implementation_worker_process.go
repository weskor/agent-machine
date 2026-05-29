package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/weskor/agent-machine/internal/agentruntime"
	"github.com/weskor/agent-machine/internal/state"
	"github.com/weskor/agent-machine/internal/workertask"
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
	availableAt, err := workertask.AvailableAtAfterLatestFailure(ctx, store, taskKey, implementationWorkerRole, now)
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

func runImplementationAttemptBatchContext(ctx context.Context, client linearClient, proj project, config runnerConfig, stateStore *state.Store, capacity int) (bool, error) {
	if stateStore == nil {
		return false, fmt.Errorf("SQLite state store unavailable for implementation worker at %s", state.DefaultDBPath(config.WorkspaceRoot))
	}
	return runClaimedAttemptBatchWithClaimerContext(ctx, client, proj, config, stateStore, capacity, claimNextImplementationAttemptContext)
}

func runQueuedImplementationAttemptBatchContext(ctx context.Context, client linearClient, proj project, config runnerConfig, stateStore *state.Store, capacity int) (bool, error) {
	if stateStore == nil {
		return false, fmt.Errorf("SQLite state store unavailable for queued implementation worker at %s", state.DefaultDBPath(config.WorkspaceRoot))
	}
	return runClaimedAttemptBatchWithClaimerContext(ctx, client, proj, config, stateStore, capacity, claimNextQueuedImplementationAttemptContext)
}

func claimNextImplementationAttempt(client linearClient, proj project, config runnerConfig, stateStore *state.Store) (*claimedRunAttempt, bool, error) {
	return claimNextImplementationAttemptContext(context.Background(), client, proj, config, stateStore)
}

func claimNextImplementationAttemptContext(ctx context.Context, client linearClient, proj project, config runnerConfig, stateStore *state.Store) (*claimedRunAttempt, bool, error) {
	if stateStore == nil {
		return nil, false, fmt.Errorf("SQLite state store unavailable for implementation worker at %s", state.DefaultDBPath(config.WorkspaceRoot))
	}
	now := time.Now().UTC()
	task, ok, err := claimNextQueuedImplementationWorkerTask(ctx, stateStore, now)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		scheduled, didWork, err := scheduleNextImplementationWorkerTask(ctx, client, config, stateStore, now)
		if err != nil || !didWork {
			return nil, didWork, err
		}
		task, ok, err = claimQueuedImplementationWorkerTask(ctx, stateStore, scheduled, time.Now().UTC())
		if err != nil || !ok {
			return nil, true, err
		}
	}
	return prepareClaimedImplementationWorkerTask(ctx, client, proj, config, stateStore, task)
}

func claimNextQueuedImplementationAttemptContext(ctx context.Context, client linearClient, proj project, config runnerConfig, stateStore *state.Store) (*claimedRunAttempt, bool, error) {
	if stateStore == nil {
		return nil, false, fmt.Errorf("SQLite state store unavailable for queued implementation worker at %s", state.DefaultDBPath(config.WorkspaceRoot))
	}
	task, ok, err := claimNextQueuedImplementationWorkerTask(ctx, stateStore, time.Now().UTC())
	if err != nil || !ok {
		return nil, false, err
	}
	return prepareClaimedImplementationWorkerTask(ctx, client, proj, config, stateStore, task)
}

func scheduleImplementationWorkerTasks(ctx context.Context, client linearClient, config runnerConfig, stateStore *state.Store, capacity int) (bool, error) {
	if stateStore == nil {
		return false, fmt.Errorf("SQLite state store unavailable for implementation scheduler at %s", state.DefaultDBPath(config.WorkspaceRoot))
	}
	if capacity < 1 {
		capacity = 1
	}
	didWork := false
	for i := 0; i < capacity; i++ {
		_, scheduled, err := scheduleNextImplementationWorkerTask(ctx, client, config, stateStore, time.Now().UTC())
		if err != nil {
			return didWork, err
		}
		if !scheduled {
			break
		}
		didWork = true
	}
	return didWork, nil
}

func scheduleNextImplementationWorkerTask(ctx context.Context, client linearClient, config runnerConfig, stateStore *state.Store, now time.Time) (state.WorkerTask, bool, error) {
	if removed, err := cleanupStaleRunLocksWithStateContext(ctx, stateStore, config.WorkspaceRoot, time.Now()); err != nil {
		return state.WorkerTask{}, false, err
	} else if removed > 0 {
		log("removed %d stale/dead run lock(s) before implementation task scheduling", removed)
	}
	candidate, _, err := nextRunnableCandidateWithOptions(client, config, stateStore, candidateSelectionOptions{SkipReviewReadyResumes: true})
	if err != nil {
		return state.WorkerTask{}, false, err
	}
	if candidate == nil {
		log("no eligible issues")
		return state.WorkerTask{}, false, nil
	}
	workspace, err := safeWorkspacePath(config.WorkspaceRoot, candidate.Identifier)
	if err != nil {
		return state.WorkerTask{}, true, err
	}
	branch, _ := currentGitBranch(workspace)
	task, enqueued, err := enqueueImplementationWorkerTask(ctx, stateStore, candidate, workspace, branch, now)
	if err != nil {
		return state.WorkerTask{}, true, err
	}
	if !enqueued {
		log("%s implementation task already exists with status=%s", candidate.Identifier, task.Status)
		return task, false, nil
	}
	log("scheduled implementation task for %s", candidate.Identifier)
	return task, true, nil
}

func prepareClaimedImplementationWorkerTask(ctx context.Context, client linearClient, proj project, config runnerConfig, stateStore *state.Store, task state.WorkerTask) (*claimedRunAttempt, bool, error) {
	started := time.Now().UTC()
	failTask := func(reason string, err error) (*claimedRunAttempt, bool, error) {
		errorText := ""
		if err != nil {
			errorText = err.Error()
		}
		completeErr := completeClaimedImplementationWorkerTask(ctx, stateStore, task, "failed", false, reason, errorText, started, time.Now().UTC())
		return nil, true, errors.Join(err, completeErr)
	}
	if task.IssueKey == "" {
		return failTask("missing_issue_key", fmt.Errorf("implementation worker task %s has no issue key", task.TaskKey))
	}
	candidate, err := client.issueByIdentifierContext(ctx, task.IssueKey)
	if err != nil {
		return failTask("linear_issue_lookup_failed", err)
	}
	if candidate == nil {
		return failTask("linear_issue_missing", fmt.Errorf("linear issue %s was not found", task.IssueKey))
	}
	prsByIssue, err := openPRsByIssueForSelection(config)
	if err != nil {
		return failTask("github_pr_lookup_failed", err)
	}
	selectedPR := prsByIssue[candidate.Identifier]
	if skipCandidateForSelectionOptionsContext(ctx, config, *candidate, selectedPR, stateStore, candidateSelectionOptions{SkipReviewReadyResumes: true}) {
		completeErr := completeClaimedImplementationWorkerTask(ctx, stateStore, task, "completed", false, "review_ready_resume", "", started, time.Now().UTC())
		return nil, true, completeErr
	}
	decision := reconcileCandidateForSelectionContext(ctx, config, *candidate, selectedPR, stateStore)
	retryDecision, retryDecisionFound := retryBackoffDecision(ctx, stateStore, *candidate, config, time.Now().UTC())
	if retryDecisionFound && !retryDecision.runnable && !retryGateAllowsRepair(decision, retryDecision) {
		completeErr := completeClaimedImplementationWorkerTask(ctx, stateStore, task, "completed", false, retryDecision.reason, "", started, time.Now().UTC())
		return nil, true, completeErr
	}
	if !decision.CanRun && !retryBackoffOverridesTerminalBlock(decision, retryDecision, retryDecisionFound) {
		completeErr := completeClaimedImplementationWorkerTask(ctx, stateStore, task, "completed", false, "not_runnable", "", started, time.Now().UTC())
		return nil, true, completeErr
	}
	workspace, err := safeWorkspacePath(config.WorkspaceRoot, candidate.Identifier)
	if err != nil {
		return failTask("workspace_path_invalid", err)
	}
	progressStarted := time.Now().UTC()
	log("picked %s: %s (%s)", candidate.Identifier, candidate.Title, candidateOrderReason(*candidate, config.ReadyState))
	writeRunProgress(config.WorkspaceRoot, runProgressForIssue(candidate, workspace, "selected", progressStarted))
	writeRunProgress(config.WorkspaceRoot, runProgressForIssue(candidate, workspace, "preflight", progressStarted))
	runtime, err := newAgentRuntime(config.RuntimeProvider)
	if err != nil {
		decision := decideAttemptLifecycle(attemptLifecycleInput{Phase: attemptLifecyclePhasePreflight, RuntimeErrorKind: "configuration", Error: err.Error()})
		snapshot := runProgressForIssue(candidate, workspace, "failed", progressStarted)
		snapshot.Error = err.Error()
		snapshot.NextAction = decision.NextAction
		writeRunProgress(config.WorkspaceRoot, snapshot)
		return failTask("runtime_preflight_failed", err)
	}
	if _, err := runtime.Preflight(ctx, agentruntime.PreflightInput{ImplementationCommand: config.RuntimeImplementationCommand(), ReviewCommand: config.ReviewCommand}); err != nil {
		decision := decideAttemptLifecycle(attemptLifecycleInput{Phase: attemptLifecyclePhasePreflight, RuntimeErrorKind: "configuration", Error: err.Error()})
		snapshot := runProgressForIssue(candidate, workspace, "failed", progressStarted)
		snapshot.Error = err.Error()
		snapshot.NextAction = decision.NextAction
		writeRunProgress(config.WorkspaceRoot, snapshot)
		return failTask("runtime_preflight_failed", err)
	}
	branch, _ := currentGitBranch(workspace)
	lock, releaseLock, err := acquireRunLockWithStateContext(ctx, stateStore, workspace, candidate, branch, time.Now())
	if err != nil {
		return failTask("run_lock_unavailable", err)
	}
	return &claimedRunAttempt{Candidate: candidate, SelectedPR: selectedPR, Workspace: workspace, Branch: lock.Branch, ProgressStarted: progressStarted, ImplementationWorkerTaskKey: task.TaskKey, ReleaseLock: releaseLock}, true, nil
}
