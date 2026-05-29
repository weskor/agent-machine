package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/weskor/agent-machine/internal/agentruntime"
	"github.com/weskor/agent-machine/internal/state"
)

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
	if retryDecisionFound && !retryDecision.runnable && !retryGateAllowsRepairableReviewFailedPR(decision, retryDecision) {
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
