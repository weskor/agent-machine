package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/weskor/agent-machine/internal/agentruntime"
	"github.com/weskor/agent-machine/internal/state"
)

type claimedReviewPendingAttempt struct {
	Payload     reviewPendingPayload
	ReleaseLock func()
	PayloadRef  *state.WorkerPayloadRef
}

func runReviewReadyAttemptContext(ctx context.Context, client linearClient, proj project, config runnerConfig, stateStore *state.Store) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if stateStore == nil {
		return false, fmt.Errorf("SQLite state store unavailable for review worker at %s", state.DefaultDBPath(config.WorkspaceRoot))
	}
	if didWork, err := runReviewPendingAttemptContext(ctx, client, config, stateStore); err != nil || didWork {
		return didWork, err
	}
	claim, didWork, err := claimNextQueuedReviewReadyAttemptContext(ctx, client, proj, config, stateStore)
	if err != nil || claim == nil {
		return didWork, err
	}
	return executeReviewReadyAttemptContext(ctx, client, config, stateStore, *claim)
}

func runReviewPendingAttempt(client linearClient, config runnerConfig, stateStore *state.Store) (bool, error) {
	return runReviewPendingAttemptContext(context.Background(), client, config, stateStore)
}

func runReviewPendingAttemptContext(ctx context.Context, client linearClient, config runnerConfig, stateStore *state.Store) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if strings.TrimSpace(config.ReviewCommand) == "" {
		log("review worker idle: review command is not configured")
		return false, nil
	}
	if removed, err := cleanupStaleRunLocksWithStateContext(ctx, stateStore, config.WorkspaceRoot, time.Now()); err != nil {
		return false, err
	} else if removed > 0 {
		log("removed %d stale/dead run lock(s) before pending review selection", removed)
	}
	claim, didWork, err := claimNextReviewPendingAttemptContext(ctx, config, stateStore)
	if err != nil || claim == nil {
		return didWork, err
	}
	return executeReviewPendingAttempt(ctx, client, config, stateStore, *claim)
}

func claimNextReviewPendingAttempt(config runnerConfig, stateStore *state.Store) (*claimedReviewPendingAttempt, bool, error) {
	return claimNextReviewPendingAttemptContext(context.Background(), config, stateStore)
}

func claimNextReviewPendingAttemptContext(ctx context.Context, config runnerConfig, stateStore *state.Store) (*claimedReviewPendingAttempt, bool, error) {
	if stateStore == nil {
		return nil, false, nil
	}
	refs, err := stateStore.PendingWorkerPayloadRefs(ctx, reviewWorkerRole, runProgressPhaseReviewPending)
	if err != nil {
		return nil, false, err
	}
	for _, ref := range refs {
		payload, err := readReviewPendingPayloadFromPath(ref.PayloadPath)
		if err != nil {
			return nil, true, err
		}
		normalizeReviewPendingPayloadFromRef(&payload, ref)
		worker := payload.Worker(linearClient{}, config, stateStore, nil, nil)
		if strings.TrimSpace(worker.prURL) == "" {
			return nil, true, fmt.Errorf("review pending payload for %s has no PR URL", ref.IssueKey)
		}
		lock, releaseLock, err := acquireRunLockWithStateContext(ctx, stateStore, worker.workspace, worker.candidate, worker.branch, time.Now())
		if err != nil {
			if errors.Is(err, errRunLocked) {
				log("%v", err)
				continue
			}
			return nil, true, err
		}
		payload.Branch = lock.Branch
		refCopy := ref
		return &claimedReviewPendingAttempt{Payload: payload, ReleaseLock: releaseLock, PayloadRef: &refCopy}, true, nil
	}
	return nil, false, nil
}

func normalizeReviewPendingPayloadFromRef(payload *reviewPendingPayload, ref state.WorkerPayloadRef) {
	if payload == nil {
		return
	}
	if strings.TrimSpace(payload.IssueID) == "" {
		payload.IssueID = ref.IssueID
	}
	if strings.TrimSpace(payload.IssueIdentifier) == "" {
		payload.IssueIdentifier = ref.IssueKey
	}
	if strings.TrimSpace(payload.Workspace) == "" {
		payload.Workspace = ref.WorkspacePath
	}
	if strings.TrimSpace(payload.Branch) == "" {
		payload.Branch = ref.BranchName
	}
	if strings.TrimSpace(payload.PRURL) == "" {
		payload.PRURL = ref.PRURL
	}
}

func executeReviewPendingAttempt(ctx context.Context, client linearClient, config runnerConfig, stateStore *state.Store, claimed claimedReviewPendingAttempt) (didWork bool, err error) {
	if claimed.ReleaseLock != nil {
		defer claimed.ReleaseLock()
	}
	defer func() {
		if markErr := completeWorkerPayloadRef(ctx, stateStore, claimed.PayloadRef, err); err == nil && markErr != nil {
			err = markErr
		}
	}()
	payload := claimed.Payload
	if strings.TrimSpace(payload.TeamID) == "" {
		return true, fmt.Errorf("review pending payload for %s has no team ID", payload.IssueIdentifier)
	}
	states, err := client.workflowStatesContext(ctx, payload.TeamID)
	if err != nil {
		return true, err
	}
	githubEnv, githubAuth, err := githubAppEnvFromEnvironmentForReviewWorker()
	if err != nil {
		now := time.Now()
		worker := payload.Worker(client, config, stateStore, states, nil)
		writeRunRecordWithCommandStateContext(ctx, stateStore, worker.workspace, runRecordFor(worker.candidate, worker.workspace, config.RuntimeImplementationCommand(), "github_app_error", now, now, worker.runtimeUsage, nil, worker.prURL, runAttemptStatusFailed, err.Error(), config.Budget.Active(), ""))
		return true, err
	}
	payload.GitHubAuth = githubAuth
	result, err := executeReviewPendingPayload(ctx, client, config, stateStore, payload, states, githubEnv)
	if err != nil || result.Terminal {
		return true, err
	}
	writeHandoffPendingStateContext(ctx, reviewPayloadHandoffCompletion(payload, client, config, stateStore, result.Review))
	return true, nil
}

func reviewPayloadHandoffCompletion(payload reviewPendingPayload, client linearClient, config runnerConfig, stateStore *state.Store, review *reviewResult) handoffCompletion {
	worker := payload.Worker(client, config, stateStore, nil, nil)
	return handoffCompletion{
		client:          client,
		config:          config,
		stateStore:      stateStore,
		candidate:       worker.candidate,
		workspace:       worker.workspace,
		branch:          worker.branch,
		progressStarted: worker.progressStarted,
		startedAt:       worker.startedAt,
		runtimeUsage:    worker.runtimeUsage,
		review:          review,
		prURL:           worker.prURL,
		validation:      worker.validation,
		scopeResult:     worker.scopeResult,
		githubAuth:      worker.githubAuth,
	}
}

func claimNextQueuedReviewReadyAttempt(client linearClient, proj project, config runnerConfig, stateStore *state.Store) (*claimedRunAttempt, bool, error) {
	return claimNextQueuedReviewReadyAttemptContext(context.Background(), client, proj, config, stateStore)
}

func claimNextQueuedReviewReadyAttemptContext(ctx context.Context, client linearClient, proj project, config runnerConfig, stateStore *state.Store) (*claimedRunAttempt, bool, error) {
	if strings.TrimSpace(config.ReviewCommand) == "" {
		log("review worker idle: review command is not configured")
		return nil, false, nil
	}
	task, ok, err := claimNextQueuedReviewReadyWorkerTask(ctx, stateStore, time.Now().UTC())
	if err != nil || !ok {
		return nil, false, err
	}
	return prepareClaimedReviewReadyWorkerTask(ctx, client, proj, config, stateStore, task)
}

func scheduleReviewReadyWorkerTasks(ctx context.Context, client linearClient, config runnerConfig, stateStore *state.Store, capacity int) (bool, error) {
	if stateStore == nil {
		return false, fmt.Errorf("SQLite state store unavailable for review scheduler at %s", state.DefaultDBPath(config.WorkspaceRoot))
	}
	if strings.TrimSpace(config.ReviewCommand) == "" {
		return false, nil
	}
	if capacity < 1 {
		capacity = 1
	}
	if removed, err := cleanupStaleRunLocksWithStateContext(ctx, stateStore, config.WorkspaceRoot, time.Now()); err != nil {
		return false, err
	} else if removed > 0 {
		log("removed %d stale/dead run lock(s) before review task scheduling", removed)
	}
	candidates, err := client.candidatesContext(ctx, config.ProjectSlug, config.ActiveStates)
	if err != nil || len(candidates) == 0 {
		return false, err
	}
	prsByIssue, err := openPRsByIssueForSelection(config)
	if err != nil {
		return false, err
	}
	didWork := false
	for _, candidate := range orderCandidates(candidates, config.ReadyState) {
		if capacity <= 0 {
			break
		}
		if active, err := hasActiveWorkerTask(ctx, stateStore, reviewWorkerRole, candidate.Identifier); err != nil {
			return didWork, err
		} else if active {
			continue
		}
		pr := prsByIssue[candidate.Identifier]
		if pr == nil {
			continue
		}
		decision := reconcileCandidateForSelectionContext(ctx, config, candidate, pr, stateStore)
		if !decision.CanRun || decision.NextAction != "run_semantic_review_after_checks_ready" {
			continue
		}
		workspace, err := safeWorkspacePath(config.WorkspaceRoot, candidate.Identifier)
		if err != nil {
			return didWork, err
		}
		if _, enqueued, err := enqueueReviewReadyWorkerTask(ctx, stateStore, candidate, *pr, workspace, time.Now().UTC()); err != nil {
			return didWork, err
		} else if enqueued {
			didWork = true
			capacity--
		}
	}
	return didWork, nil
}

func prepareClaimedReviewReadyWorkerTask(ctx context.Context, client linearClient, proj project, config runnerConfig, stateStore *state.Store, task state.WorkerTask) (*claimedRunAttempt, bool, error) {
	started := time.Now().UTC()
	failTask := func(reason string, err error) (*claimedRunAttempt, bool, error) {
		errorText := ""
		if err != nil {
			errorText = err.Error()
		}
		completeErr := completeClaimedReviewReadyWorkerTask(ctx, stateStore, task, "failed", false, reason, errorText, started, time.Now().UTC())
		return nil, true, errors.Join(err, completeErr)
	}
	if task.IssueKey == "" {
		return failTask("missing_issue_key", fmt.Errorf("review worker task %s has no issue key", task.TaskKey))
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
	pr := prsByIssue[candidate.Identifier]
	if pr == nil {
		completeErr := completeClaimedReviewReadyWorkerTask(ctx, stateStore, task, "completed", false, "missing_open_pr", "", started, time.Now().UTC())
		return nil, true, completeErr
	}
	decision := reconcileCandidateForSelectionContext(ctx, config, *candidate, pr, stateStore)
	if !decision.CanRun || decision.NextAction != "run_semantic_review_after_checks_ready" {
		log("skipping review resume for %s: lifecycle=%s blockers=%s next=%s", candidate.Identifier, decision.Lifecycle, strings.Join(decision.Blockers, "; "), decision.NextAction)
		completeErr := completeClaimedReviewReadyWorkerTask(ctx, stateStore, task, "completed", false, "not_review_ready", "", started, time.Now().UTC())
		return nil, true, completeErr
	}
	workspace, err := safeWorkspacePath(config.WorkspaceRoot, candidate.Identifier)
	if err != nil {
		return failTask("workspace_path_invalid", err)
	}
	progressStarted := time.Now().UTC()
	writeRunProgress(config.WorkspaceRoot, runProgressForIssue(candidate, workspace, "review_resume_selected", progressStarted))
	runtime, err := newAgentRuntime(config.RuntimeProvider)
	if err != nil {
		snapshot := runProgressForIssue(candidate, workspace, "failed", progressStarted)
		snapshot.Error = err.Error()
		writeRunProgress(config.WorkspaceRoot, snapshot)
		return failTask("runtime_preflight_failed", err)
	}
	if _, err := runtime.Preflight(ctx, agentruntime.PreflightInput{ImplementationCommand: config.RuntimeImplementationCommand(), ReviewCommand: config.ReviewCommand}); err != nil {
		snapshot := runProgressForIssue(candidate, workspace, "failed", progressStarted)
		snapshot.Error = err.Error()
		writeRunProgress(config.WorkspaceRoot, snapshot)
		return failTask("runtime_preflight_failed", err)
	}
	branch, _ := currentGitBranch(workspace)
	lock, releaseLock, err := acquireRunLockWithStateContext(ctx, stateStore, workspace, candidate, branch, time.Now())
	if err != nil {
		return failTask("run_lock_unavailable", err)
	}
	return &claimedRunAttempt{Candidate: candidate, SelectedPR: pr, Workspace: workspace, Branch: lock.Branch, ProgressStarted: progressStarted, ReviewWorkerTaskKey: task.TaskKey, ReleaseLock: releaseLock}, true, nil
}

func claimNextReviewReadyAttempt(client linearClient, proj project, config runnerConfig, stateStore *state.Store) (*claimedRunAttempt, bool, error) {
	return claimNextReviewReadyAttemptContext(context.Background(), client, proj, config, stateStore)
}

func claimNextReviewReadyAttemptContext(ctx context.Context, client linearClient, proj project, config runnerConfig, stateStore *state.Store) (*claimedRunAttempt, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if strings.TrimSpace(config.ReviewCommand) == "" {
		log("review worker idle: review command is not configured")
		return nil, false, nil
	}
	if removed, err := cleanupStaleRunLocksWithStateContext(ctx, stateStore, config.WorkspaceRoot, time.Now()); err != nil {
		return nil, false, err
	} else if removed > 0 {
		log("removed %d stale/dead run lock(s) before review selection", removed)
	}
	candidates, err := client.candidatesContext(ctx, config.ProjectSlug, config.ActiveStates)
	if err != nil || len(candidates) == 0 {
		return nil, false, err
	}
	prsByIssue, err := openPRsByIssueForSelection(config)
	if err != nil {
		return nil, false, err
	}
	for _, candidate := range orderCandidates(candidates, config.ReadyState) {
		pr := prsByIssue[candidate.Identifier]
		if pr == nil {
			continue
		}
		decision := reconcileCandidateForSelectionContext(ctx, config, candidate, pr, stateStore)
		if !decision.CanRun || decision.NextAction != "run_semantic_review_after_checks_ready" {
			log("skipping review resume for %s: lifecycle=%s blockers=%s next=%s", candidate.Identifier, decision.Lifecycle, strings.Join(decision.Blockers, "; "), decision.NextAction)
			continue
		}
		workspace, err := safeWorkspacePath(config.WorkspaceRoot, candidate.Identifier)
		if err != nil {
			return nil, true, err
		}
		progressStarted := time.Now().UTC()
		writeRunProgress(config.WorkspaceRoot, runProgressForIssue(&candidate, workspace, "review_resume_selected", progressStarted))
		runtime, err := newAgentRuntime(config.RuntimeProvider)
		if err != nil {
			snapshot := runProgressForIssue(&candidate, workspace, "failed", progressStarted)
			snapshot.Error = err.Error()
			writeRunProgress(config.WorkspaceRoot, snapshot)
			return nil, true, err
		}
		if _, err := runtime.Preflight(ctx, agentruntime.PreflightInput{ImplementationCommand: config.RuntimeImplementationCommand(), ReviewCommand: config.ReviewCommand}); err != nil {
			snapshot := runProgressForIssue(&candidate, workspace, "failed", progressStarted)
			snapshot.Error = err.Error()
			writeRunProgress(config.WorkspaceRoot, snapshot)
			return nil, true, err
		}
		branch, _ := currentGitBranch(workspace)
		lock, releaseLock, err := acquireRunLockWithStateContext(ctx, stateStore, workspace, &candidate, branch, time.Now())
		if err != nil {
			if errors.Is(err, errRunLocked) {
				log("%v", err)
				return nil, false, nil
			}
			return nil, true, err
		}
		return &claimedRunAttempt{Candidate: &candidate, SelectedPR: pr, Workspace: workspace, Branch: lock.Branch, ProgressStarted: progressStarted, ReleaseLock: releaseLock}, true, nil
	}
	log("no review-ready issues")
	return nil, false, nil
}

func executeReviewReadyAttemptContext(ctx context.Context, client linearClient, config runnerConfig, stateStore *state.Store, claimed claimedRunAttempt) (didWork bool, err error) {
	started := time.Now().UTC()
	if claimed.ReviewWorkerTaskKey != "" && claimed.Candidate != nil {
		defer func() {
			status := "completed"
			reason := "review_resume_completed"
			errorText := ""
			if err != nil {
				status = "failed"
				reason = "review_resume_failed"
				errorText = err.Error()
			}
			task := state.WorkerTask{TaskKey: claimed.ReviewWorkerTaskKey, Role: reviewWorkerRole, IssueKey: claimed.Candidate.Identifier, IssueID: claimed.Candidate.ID, Attempt: 1}
			completeErr := completeClaimedReviewReadyWorkerTask(ctx, stateStore, task, status, didWork, reason, errorText, started, time.Now().UTC())
			if err == nil && completeErr != nil {
				err = completeErr
			}
		}()
	}
	if claimed.ReleaseLock != nil {
		defer claimed.ReleaseLock()
	}
	candidate := claimed.Candidate
	if candidate == nil || claimed.SelectedPR == nil {
		return false, nil
	}
	states, err := client.workflowStatesContext(ctx, candidate.Team.ID)
	if err != nil {
		return true, err
	}
	githubEnv, githubAuth, err := githubAppEnvFromEnvironment()
	if err != nil {
		now := time.Now()
		writeRunRecordWithCommandStateContext(ctx, stateStore, claimed.Workspace, runRecordFor(candidate, claimed.Workspace, config.RuntimeImplementationCommand(), "github_app_error", now, now, nil, nil, claimed.SelectedPR.URL, runAttemptStatusFailed, err.Error(), config.Budget.Active(), ""))
		return true, err
	}
	return resumeReviewReadyRunContext(ctx, client, stateStore, config, candidate, states, claimed.Workspace, claimed.Branch, githubEnv, githubAuth, claimed.ProgressStarted, time.Now(), claimed.SelectedPR)
}

var githubAppEnvFromEnvironmentForReviewWorker = githubAppEnvFromEnvironment
