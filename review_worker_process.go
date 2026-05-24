package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/weskor/pi-symphony/internal/agentruntime"
	cfg "github.com/weskor/pi-symphony/internal/config"
	"github.com/weskor/pi-symphony/internal/state"
)

type claimedReviewPendingAttempt struct {
	Payload     reviewPendingPayload
	ReleaseLock func()
}

func runReviewReadyAttempt(client linearClient, wf workflow, config runnerConfig, stateStore *state.Store) (bool, error) {
	if stateStore == nil {
		return false, fmt.Errorf("SQLite state store unavailable for review worker at %s", state.DefaultDBPath(config.WorkspaceRoot))
	}
	if didWork, err := runReviewPendingAttempt(client, config, stateStore); err != nil || didWork {
		return didWork, err
	}
	claim, didWork, err := claimNextReviewReadyAttempt(client, wf, config, stateStore)
	if err != nil || claim == nil {
		return didWork, err
	}
	return executeReviewReadyAttempt(client, config, stateStore, *claim)
}

func runReviewPendingAttempt(client linearClient, config runnerConfig, stateStore *state.Store) (bool, error) {
	if strings.TrimSpace(config.ReviewCommand) == "" {
		log("review worker idle: review command is not configured")
		return false, nil
	}
	if removed, err := cleanupStaleRunLocksWithState(stateStore, config.WorkspaceRoot, time.Now()); err != nil {
		return false, err
	} else if removed > 0 {
		log("removed %d stale/dead run lock(s) before pending review selection", removed)
	}
	claim, didWork, err := claimNextReviewPendingAttempt(config, stateStore)
	if err != nil || claim == nil {
		return didWork, err
	}
	return executeReviewPendingAttempt(context.Background(), client, config, stateStore, *claim)
}

func claimNextReviewPendingAttempt(config runnerConfig, stateStore *state.Store) (*claimedReviewPendingAttempt, bool, error) {
	root := runProgressRoot(config.WorkspaceRoot)
	if strings.TrimSpace(root) == "" {
		return nil, false, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		identifier := entry.Name()
		snapshot, err := readRunProgress(config.WorkspaceRoot, identifier)
		if err != nil {
			return nil, true, err
		}
		if snapshot.Phase != runProgressPhaseReviewPending {
			continue
		}
		payload, err := readReviewPendingPayload(config.WorkspaceRoot, identifier)
		if err != nil {
			return nil, true, err
		}
		normalizeReviewPendingPayload(&payload, snapshot)
		if strings.TrimSpace(payload.Workspace) == "" {
			workspace, err := safeWorkspacePath(config.WorkspaceRoot, identifier)
			if err != nil {
				return nil, true, err
			}
			payload.Workspace = workspace
		}
		if strings.TrimSpace(payload.Branch) == "" {
			payload.Branch = expectedWorkspaceBranch(identifier)
		}
		worker := payload.Worker(linearClient{}, config, stateStore, nil, nil)
		if strings.TrimSpace(worker.prURL) == "" {
			return nil, true, fmt.Errorf("review pending payload for %s has no PR URL", identifier)
		}
		lock, releaseLock, err := acquireRunLockWithState(stateStore, worker.workspace, worker.candidate, worker.branch, time.Now())
		if err != nil {
			if errors.Is(err, errRunLocked) {
				log("%v", err)
				continue
			}
			return nil, true, err
		}
		payload.Branch = lock.Branch
		return &claimedReviewPendingAttempt{Payload: payload, ReleaseLock: releaseLock}, true, nil
	}
	return nil, false, nil
}

func normalizeReviewPendingPayload(payload *reviewPendingPayload, snapshot runProgressSnapshot) {
	if payload == nil {
		return
	}
	if strings.TrimSpace(payload.IssueIdentifier) == "" {
		payload.IssueIdentifier = snapshot.IssueIdentifier
	}
	if strings.TrimSpace(payload.IssueTitle) == "" {
		payload.IssueTitle = snapshot.IssueTitle
	}
	if strings.TrimSpace(payload.Workspace) == "" {
		payload.Workspace = snapshot.Workspace
	}
	if strings.TrimSpace(payload.Branch) == "" {
		payload.Branch = snapshot.Branch
	}
	if strings.TrimSpace(payload.PRURL) == "" {
		payload.PRURL = snapshot.PRURL
	}
	if payload.ProgressStarted.IsZero() {
		payload.ProgressStarted = snapshot.StartedAt
	}
	if payload.StartedAt.IsZero() {
		payload.StartedAt = snapshot.StartedAt
	}
}

func executeReviewPendingAttempt(ctx context.Context, client linearClient, config runnerConfig, stateStore *state.Store, claimed claimedReviewPendingAttempt) (bool, error) {
	if claimed.ReleaseLock != nil {
		defer claimed.ReleaseLock()
	}
	payload := claimed.Payload
	if strings.TrimSpace(payload.TeamID) == "" {
		return true, fmt.Errorf("review pending payload for %s has no team ID", payload.IssueIdentifier)
	}
	states, err := client.workflowStates(payload.TeamID)
	if err != nil {
		return true, err
	}
	githubEnv, githubAuth, err := githubAppEnvFromEnvironmentForReviewWorker()
	if err != nil {
		now := time.Now()
		worker := payload.Worker(client, config, stateStore, states, nil)
		writeRunRecordWithCommandState(stateStore, worker.workspace, runRecordFor(worker.candidate, worker.workspace, configuredRuntimeCommand(config), "github_app_error", now, now, worker.runtimeUsage, nil, worker.prURL, runAttemptStatusFailed, err.Error(), config.Budget.Active(), ""))
		return true, err
	}
	payload.GitHubAuth = githubAuth
	result, err := executeReviewPendingPayload(ctx, client, config, stateStore, payload, states, githubEnv)
	if err != nil || result.Terminal {
		return true, err
	}
	writeHandoffPendingState(reviewPayloadHandoffCompletion(payload, client, config, stateStore, result.Review))
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
		githubAuth:      worker.githubAuth,
	}
}

func claimNextReviewReadyAttempt(client linearClient, wf workflow, config runnerConfig, stateStore *state.Store) (*claimedRunAttempt, bool, error) {
	if strings.TrimSpace(config.ReviewCommand) == "" {
		log("review worker idle: review command is not configured")
		return nil, false, nil
	}
	if removed, err := cleanupStaleRunLocksWithState(stateStore, config.WorkspaceRoot, time.Now()); err != nil {
		return nil, false, err
	} else if removed > 0 {
		log("removed %d stale/dead run lock(s) before review selection", removed)
	}
	candidates, err := client.candidates(config.ProjectSlug, config.ActiveStates)
	if err != nil || len(candidates) == 0 {
		return nil, false, err
	}
	prsByIssue, err := openPRsByIssueForSelection(config)
	if err != nil {
		return nil, false, err
	}
	readiness := newReviewReadinessModule(config.WorkspaceRoot)
	for _, candidate := range orderCandidates(candidates, config.ReadyState) {
		pr := prsByIssue[candidate.Identifier]
		if pr == nil || !readiness.ShouldResume(candidate.Identifier, *pr) {
			continue
		}
		decision := reconcileCandidateForSelection(config, candidate, pr, stateStore)
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
		if _, err := runtime.Preflight(context.Background(), agentruntime.PreflightInput{ImplementationCommand: configuredRuntimeCommand(config), ReviewCommand: config.ReviewCommand, MaxTurns: cfg.AgentMaxTurnsFromWorkflow(wf.YAML)}); err != nil {
			snapshot := runProgressForIssue(&candidate, workspace, "failed", progressStarted)
			snapshot.Error = err.Error()
			writeRunProgress(config.WorkspaceRoot, snapshot)
			return nil, true, err
		}
		branch, _ := currentGitBranch(workspace)
		lock, releaseLock, err := acquireRunLockWithState(stateStore, workspace, &candidate, branch, time.Now())
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

func executeReviewReadyAttempt(client linearClient, config runnerConfig, stateStore *state.Store, claimed claimedRunAttempt) (bool, error) {
	if claimed.ReleaseLock != nil {
		defer claimed.ReleaseLock()
	}
	candidate := claimed.Candidate
	if candidate == nil || claimed.SelectedPR == nil {
		return false, nil
	}
	states, err := client.workflowStates(candidate.Team.ID)
	if err != nil {
		return true, err
	}
	githubEnv, githubAuth, err := githubAppEnvFromEnvironment()
	if err != nil {
		now := time.Now()
		writeRunRecordWithCommandState(stateStore, claimed.Workspace, runRecordFor(candidate, claimed.Workspace, configuredRuntimeCommand(config), "github_app_error", now, now, nil, nil, claimed.SelectedPR.URL, runAttemptStatusFailed, err.Error(), config.Budget.Active(), ""))
		return true, err
	}
	return resumeReviewReadyRun(client, stateStore, config, candidate, states, claimed.Workspace, claimed.Branch, githubEnv, githubAuth, claimed.ProgressStarted, time.Now(), claimed.SelectedPR)
}

var githubAppEnvFromEnvironmentForReviewWorker = githubAppEnvFromEnvironment
