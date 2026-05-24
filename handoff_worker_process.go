package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/weskor/pi-symphony/internal/state"
)

type claimedHandoffPendingAttempt struct {
	Payload     handoffPendingPayload
	ReleaseLock func()
}

type claimedPRHandoffPendingAttempt struct {
	Payload     prHandoffPendingPayload
	ReleaseLock func()
}

func runHandoffPendingAttempt(client linearClient, config runnerConfig, stateStore *state.Store) (bool, error) {
	if stateStore == nil {
		return false, fmt.Errorf("SQLite state store unavailable for handoff worker at %s", state.DefaultDBPath(config.WorkspaceRoot))
	}
	if removed, err := cleanupStaleRunLocksWithState(stateStore, config.WorkspaceRoot, time.Now()); err != nil {
		return false, err
	} else if removed > 0 {
		log("removed %d stale/dead run lock(s) before handoff selection", removed)
	}
	prClaim, didWork, err := claimNextPRHandoffPendingAttempt(config, stateStore)
	if err != nil || prClaim != nil {
		if prClaim == nil {
			return didWork, err
		}
		return executePRHandoffPendingAttempt(context.Background(), client, config, stateStore, *prClaim)
	}
	claim, didWork, err := claimNextHandoffPendingAttempt(config, stateStore)
	if err != nil || claim == nil {
		return didWork, err
	}
	return executeHandoffPendingAttempt(context.Background(), client, config, stateStore, *claim)
}

func claimNextPRHandoffPendingAttempt(config runnerConfig, stateStore *state.Store) (*claimedPRHandoffPendingAttempt, bool, error) {
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
		if snapshot.Phase != runProgressPhasePRHandoffPending {
			continue
		}
		payload, err := readPRHandoffPendingPayload(config.WorkspaceRoot, identifier)
		if err != nil {
			return nil, true, err
		}
		normalizePRHandoffPendingPayload(&payload, snapshot)
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
		candidate := payload.Issue()
		if strings.TrimSpace(candidate.Identifier) == "" {
			return nil, true, fmt.Errorf("PR handoff pending payload for %s has no issue identifier", identifier)
		}
		lock, releaseLock, err := acquireRunLockWithState(stateStore, payload.Workspace, candidate, payload.Branch, time.Now())
		if err != nil {
			if errors.Is(err, errRunLocked) {
				log("%v", err)
				continue
			}
			return nil, true, err
		}
		payload.Branch = lock.Branch
		return &claimedPRHandoffPendingAttempt{Payload: payload, ReleaseLock: releaseLock}, true, nil
	}
	return nil, false, nil
}

func normalizePRHandoffPendingPayload(payload *prHandoffPendingPayload, snapshot runProgressSnapshot) {
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
	if strings.TrimSpace(payload.AgentPRURL) == "" {
		payload.AgentPRURL = snapshot.PRURL
	}
	if payload.ProgressStarted.IsZero() {
		payload.ProgressStarted = snapshot.StartedAt
	}
	if payload.StartedAt.IsZero() {
		payload.StartedAt = snapshot.StartedAt
	}
}

func executePRHandoffPendingAttempt(ctx context.Context, client linearClient, config runnerConfig, stateStore *state.Store, claimed claimedPRHandoffPendingAttempt) (bool, error) {
	if claimed.ReleaseLock != nil {
		defer claimed.ReleaseLock()
	}
	payload := claimed.Payload
	githubEnv, githubAuth, err := githubAppEnvFromEnvironmentForHandoffWorker()
	if err != nil {
		now := time.Now()
		candidate := payload.Issue()
		decision := decideAttemptLifecycle(attemptLifecycleInput{Phase: attemptLifecyclePhaseHandoff, PRURL: payload.AgentPRURL, Error: err.Error()})
		writeRunRecordWithCommandState(stateStore, payload.Workspace, runRecordFor(candidate, payload.Workspace, configuredRuntimeCommand(config), "github_app_error", payload.AttemptStartedAt, now, payload.RuntimeUsage, nil, payload.AgentPRURL, decision.Status, err.Error(), config.Budget.Active(), ""))
		return true, err
	}
	payload.GitHubAuth = githubAuth
	prURL, err := executePRHandoffPendingPayload(config, payload, githubEnv)
	if err != nil {
		now := time.Now()
		candidate := payload.Issue()
		decision := decideAttemptLifecycle(attemptLifecycleInput{Phase: attemptLifecyclePhaseHandoff, PRURL: payload.AgentPRURL, Error: err.Error()})
		writeRunRecordWithCommandState(stateStore, payload.Workspace, runRecordFor(candidate, payload.Workspace, configuredRuntimeCommand(config), githubAuth, payload.AttemptStartedAt, now, payload.RuntimeUsage, nil, payload.AgentPRURL, decision.Status, err.Error(), config.Budget.Active(), ""))
		return true, err
	}
	payload.AgentPRURL = prURL
	handoff := runProgressForIssue(payload.Issue(), payload.Workspace, "handoff_pr", payload.ProgressStarted)
	handoff.Branch = payload.Branch
	handoff.PRURL = prURL
	writeRunProgress(config.WorkspaceRoot, handoff)
	if strings.TrimSpace(config.ReviewCommand) != "" {
		review := payload.ReviewWorker(client, config, stateStore, githubEnv)
		review.prURL = prURL
		review.githubAuth = githubAuth
		if err := writeReviewPendingState(review); err != nil {
			return true, err
		}
		return true, nil
	}
	writeHandoffPendingState(payload.HandoffCompletion(client, config, stateStore, nil, prURL, githubAuth))
	return true, nil
}

func claimNextHandoffPendingAttempt(config runnerConfig, stateStore *state.Store) (*claimedHandoffPendingAttempt, bool, error) {
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
		if snapshot.Phase != runProgressPhaseHandoffPending {
			continue
		}
		payload, err := readHandoffPendingPayload(config.WorkspaceRoot, identifier)
		if err != nil {
			return nil, true, err
		}
		normalizeHandoffPendingPayload(&payload, snapshot)
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
		completion := payload.Completion(linearClient{}, config, stateStore, nil)
		if strings.TrimSpace(completion.prURL) == "" {
			return nil, true, fmt.Errorf("handoff pending payload for %s has no PR URL", identifier)
		}
		lock, releaseLock, err := acquireRunLockWithState(stateStore, completion.workspace, completion.candidate, completion.branch, time.Now())
		if err != nil {
			if errors.Is(err, errRunLocked) {
				log("%v", err)
				continue
			}
			return nil, true, err
		}
		payload.Branch = lock.Branch
		return &claimedHandoffPendingAttempt{Payload: payload, ReleaseLock: releaseLock}, true, nil
	}
	log("no handoff-pending issues")
	return nil, false, nil
}

func normalizeHandoffPendingPayload(payload *handoffPendingPayload, snapshot runProgressSnapshot) {
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

func executeHandoffPendingAttempt(ctx context.Context, client linearClient, config runnerConfig, stateStore *state.Store, claimed claimedHandoffPendingAttempt) (bool, error) {
	if claimed.ReleaseLock != nil {
		defer claimed.ReleaseLock()
	}
	payload := claimed.Payload
	var states []workflowState
	if strings.TrimSpace(config.HandoffState) != "" {
		if strings.TrimSpace(payload.TeamID) == "" {
			return true, fmt.Errorf("handoff pending payload for %s has no team ID", payload.IssueIdentifier)
		}
		var err error
		states, err = client.workflowStates(payload.TeamID)
		if err != nil {
			return true, err
		}
	}
	return executeHandoffPendingPayload(ctx, client, config, stateStore, payload, states)
}

var githubAppEnvFromEnvironmentForHandoffWorker = githubAppEnvFromEnvironment
