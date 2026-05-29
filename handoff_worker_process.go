package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/weskor/agent-machine/internal/state"
	"github.com/weskor/agent-machine/internal/terminalpr"
)

type claimedHandoffPendingAttempt struct {
	Payload     handoffPendingPayload
	ReleaseLock func()
	PayloadRef  *state.WorkerPayloadRef
}

type claimedPRHandoffPendingAttempt struct {
	Payload     prHandoffPendingPayload
	ReleaseLock func()
	PayloadRef  *state.WorkerPayloadRef
}

func recordReviewPendingPayloadRef(store *state.Store, payload reviewPendingPayload, payloadPath string) error {
	return recordReviewPendingPayloadRefContext(context.Background(), store, payload, payloadPath)
}

func recordReviewPendingPayloadRefContext(ctx context.Context, store *state.Store, payload reviewPendingPayload, payloadPath string) error {
	if store == nil {
		return nil
	}
	return store.UpsertWorkerPayloadRef(ctx, state.WorkerPayloadRef{
		Role:          reviewWorkerRole,
		Phase:         runProgressPhaseReviewPending,
		IssueKey:      payload.IssueIdentifier,
		IssueID:       payload.IssueID,
		Attempt:       1,
		WorkspacePath: payload.Workspace,
		BranchName:    payload.Branch,
		PRURL:         payload.PRURL,
		PayloadPath:   payloadPath,
		Status:        "pending",
		UpdatedAt:     time.Now().UTC(),
	})
}

func completeReviewPendingPayloadRef(ctx context.Context, store *state.Store, payload reviewPendingPayload, workerErr error) error {
	if store == nil {
		return nil
	}
	return completeWorkerPayloadRef(ctx, store, &state.WorkerPayloadRef{
		Role:     reviewWorkerRole,
		Phase:    runProgressPhaseReviewPending,
		IssueKey: payload.IssueIdentifier,
		Attempt:  1,
	}, workerErr)
}

func recordPRHandoffPendingPayloadRef(store *state.Store, payload prHandoffPendingPayload, payloadPath string) error {
	return recordPRHandoffPendingPayloadRefContext(context.Background(), store, payload, payloadPath)
}

func recordPRHandoffPendingPayloadRefContext(ctx context.Context, store *state.Store, payload prHandoffPendingPayload, payloadPath string) error {
	if store == nil {
		return nil
	}
	now := time.Now().UTC()
	if err := store.UpsertPRHandoffIntent(ctx, state.PRHandoffIntent{
		IssueKey:      payload.IssueIdentifier,
		IssueID:       payload.IssueID,
		Attempt:       1,
		WorkspacePath: payload.Workspace,
		BranchName:    payload.Branch,
		AgentPRURL:    payload.AgentPRURL,
		PayloadPath:   payloadPath,
		Status:        state.PRHandoffIntentStatusPending,
		UpdatedAt:     now,
	}); err != nil {
		return err
	}
	return store.UpsertWorkerPayloadRef(ctx, state.WorkerPayloadRef{
		Role:          handoffWorkerRole,
		Phase:         runProgressPhasePRHandoffPending,
		IssueKey:      payload.IssueIdentifier,
		IssueID:       payload.IssueID,
		Attempt:       1,
		WorkspacePath: payload.Workspace,
		BranchName:    payload.Branch,
		PRURL:         payload.AgentPRURL,
		PayloadPath:   payloadPath,
		Status:        "pending",
		UpdatedAt:     now,
	})
}

func recordHandoffPendingPayloadRef(store *state.Store, payload handoffPendingPayload, payloadPath string) error {
	return recordHandoffPendingPayloadRefContext(context.Background(), store, payload, payloadPath)
}

func recordHandoffPendingPayloadRefContext(ctx context.Context, store *state.Store, payload handoffPendingPayload, payloadPath string) error {
	if store == nil {
		return nil
	}
	return store.UpsertWorkerPayloadRef(ctx, state.WorkerPayloadRef{
		Role:          handoffWorkerRole,
		Phase:         runProgressPhaseHandoffPending,
		IssueKey:      payload.IssueIdentifier,
		IssueID:       payload.IssueID,
		Attempt:       1,
		WorkspacePath: payload.Workspace,
		BranchName:    payload.Branch,
		PRURL:         payload.PRURL,
		PayloadPath:   payloadPath,
		Status:        "pending",
		UpdatedAt:     time.Now().UTC(),
	})
}

func completeWorkerPayloadRef(ctx context.Context, store *state.Store, ref *state.WorkerPayloadRef, workerErr error) error {
	if store == nil || ref == nil {
		return nil
	}
	completeCtx := ctx
	if ctx.Err() != nil {
		completeCtx = context.WithoutCancel(ctx)
	}
	status := "completed"
	if workerErr != nil && !errors.Is(workerErr, terminalpr.ErrTerminalPullRequest) {
		status = "failed"
	}
	return store.CompleteWorkerPayloadRef(completeCtx, *ref, status, time.Now().UTC())
}

func completePRHandoffIntent(ctx context.Context, store *state.Store, payload prHandoffPendingPayload, prURL string, workerErr error) error {
	if store == nil {
		return nil
	}
	completeCtx := ctx
	if ctx.Err() != nil {
		completeCtx = context.WithoutCancel(ctx)
	}
	status := state.PRHandoffIntentStatusCompleted
	errorText := ""
	if workerErr != nil && !errors.Is(workerErr, terminalpr.ErrTerminalPullRequest) {
		status = state.PRHandoffIntentStatusFailed
		errorText = workerErr.Error()
	}
	return store.CompletePRHandoffIntent(completeCtx, payload.IssueIdentifier, 1, status, prURL, errorText, time.Now().UTC())
}

func runHandoffPendingAttempt(client linearClient, config runnerConfig, stateStore *state.Store) (bool, error) {
	return runHandoffPendingAttemptContext(context.Background(), client, config, stateStore)
}

func runHandoffPendingAttemptContext(ctx context.Context, client linearClient, config runnerConfig, stateStore *state.Store) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if stateStore == nil {
		return false, fmt.Errorf("SQLite state store unavailable for handoff worker at %s", state.DefaultDBPath(config.WorkspaceRoot))
	}
	if removed, err := cleanupStaleRunLocksWithStateContext(ctx, stateStore, config.WorkspaceRoot, time.Now()); err != nil {
		return false, err
	} else if removed > 0 {
		log("removed %d stale/dead run lock(s) before handoff selection", removed)
	}
	prClaim, didWork, err := claimNextPRHandoffPendingAttemptContext(ctx, config, stateStore)
	if err != nil || prClaim != nil {
		if prClaim == nil {
			return didWork, err
		}
		return executePRHandoffPendingAttempt(ctx, client, config, stateStore, *prClaim)
	}
	claim, didWork, err := claimNextHandoffPendingAttemptContext(ctx, config, stateStore)
	if err != nil || claim == nil {
		return didWork, err
	}
	return executeHandoffPendingAttempt(ctx, client, config, stateStore, *claim)
}

func claimNextPRHandoffPendingAttempt(config runnerConfig, stateStore *state.Store) (*claimedPRHandoffPendingAttempt, bool, error) {
	return claimNextPRHandoffPendingAttemptContext(context.Background(), config, stateStore)
}

func claimNextPRHandoffPendingAttemptContext(ctx context.Context, config runnerConfig, stateStore *state.Store) (*claimedPRHandoffPendingAttempt, bool, error) {
	if stateStore == nil {
		return nil, false, nil
	}
	refs, err := stateStore.PendingWorkerPayloadRefs(ctx, handoffWorkerRole, runProgressPhasePRHandoffPending)
	if err != nil {
		return nil, false, err
	}
	for _, ref := range refs {
		payload, err := readPRHandoffPendingPayloadFromPath(ref.PayloadPath)
		if err != nil {
			return nil, true, err
		}
		normalizePRHandoffPendingPayloadFromRef(&payload, ref)
		candidate := payload.Issue()
		if strings.TrimSpace(candidate.Identifier) == "" {
			return nil, true, fmt.Errorf("PR handoff pending payload for %s has no issue identifier", ref.IssueKey)
		}
		lock, releaseLock, err := acquireRunLockWithStateContext(ctx, stateStore, payload.Workspace, candidate, payload.Branch, time.Now())
		if err != nil {
			if errors.Is(err, errRunLocked) {
				log("%v", err)
				continue
			}
			return nil, true, err
		}
		payload.Branch = lock.Branch
		refCopy := ref
		return &claimedPRHandoffPendingAttempt{Payload: payload, ReleaseLock: releaseLock, PayloadRef: &refCopy}, true, nil
	}
	return nil, false, nil
}

func normalizePRHandoffPendingPayloadFromRef(payload *prHandoffPendingPayload, ref state.WorkerPayloadRef) {
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
	if strings.TrimSpace(payload.AgentPRURL) == "" {
		payload.AgentPRURL = ref.PRURL
	}
}

func executePRHandoffPendingAttempt(ctx context.Context, client linearClient, config runnerConfig, stateStore *state.Store, claimed claimedPRHandoffPendingAttempt) (didWork bool, err error) {
	if claimed.ReleaseLock != nil {
		defer claimed.ReleaseLock()
	}
	payload := claimed.Payload
	prURL := ""
	defer func() {
		intentErr := completePRHandoffIntent(ctx, stateStore, payload, prURL, err)
		markErr := completeWorkerPayloadRef(ctx, stateStore, claimed.PayloadRef, err)
		if intentErr != nil || markErr != nil {
			err = errors.Join(err, intentErr, markErr)
		}
	}()
	if claimed.PayloadRef != nil {
		if recordErr := recordPRHandoffPendingPayloadRefContext(ctx, stateStore, payload, claimed.PayloadRef.PayloadPath); recordErr != nil {
			return true, recordErr
		}
	}
	githubEnv, githubAuth, err := githubAppEnvFromEnvironmentForHandoffWorker()
	if err != nil {
		now := time.Now()
		candidate := payload.Issue()
		decision := decideAttemptLifecycle(attemptLifecycleInput{Phase: attemptLifecyclePhaseHandoff, PRURL: payload.AgentPRURL, Error: err.Error()})
		writeRunRecordWithCommandStateContext(ctx, stateStore, payload.Workspace, runRecordFor(candidate, payload.Workspace, config.RuntimeImplementationCommand(), "github_app_error", payload.AttemptStartedAt, now, payload.RuntimeUsage, nil, payload.AgentPRURL, decision.Status, err.Error(), config.Budget.Active(), ""))
		return true, err
	}
	payload.GitHubAuth = githubAuth
	prURL, err = executePRHandoffPendingPayloadContext(ctx, config, payload, githubEnv)
	if err != nil {
		if completeTerminalPullRequestHandoffProgress(config, payload.Issue(), payload.Workspace, payload.Branch, payload.ProgressStarted, firstNonEmpty(prURL, payload.AgentPRURL), err) {
			err = nil
			return true, nil
		}
		now := time.Now()
		candidate := payload.Issue()
		decision := decideAttemptLifecycle(attemptLifecycleInput{Phase: attemptLifecyclePhaseHandoff, PRURL: payload.AgentPRURL, Error: err.Error()})
		writeRunRecordWithCommandStateContext(ctx, stateStore, payload.Workspace, runRecordFor(candidate, payload.Workspace, config.RuntimeImplementationCommand(), githubAuth, payload.AttemptStartedAt, now, payload.RuntimeUsage, nil, payload.AgentPRURL, decision.Status, err.Error(), config.Budget.Active(), ""))
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
		if err := writeReviewPendingStateContext(ctx, review); err != nil {
			return true, err
		}
		return true, nil
	}
	writeHandoffPendingStateContext(ctx, payload.HandoffCompletion(client, config, stateStore, nil, prURL, githubAuth))
	return true, nil
}

func claimNextHandoffPendingAttempt(config runnerConfig, stateStore *state.Store) (*claimedHandoffPendingAttempt, bool, error) {
	return claimNextHandoffPendingAttemptContext(context.Background(), config, stateStore)
}

func claimNextHandoffPendingAttemptContext(ctx context.Context, config runnerConfig, stateStore *state.Store) (*claimedHandoffPendingAttempt, bool, error) {
	if stateStore == nil {
		return nil, false, nil
	}
	refs, err := stateStore.PendingWorkerPayloadRefs(ctx, handoffWorkerRole, runProgressPhaseHandoffPending)
	if err != nil {
		return nil, false, err
	}
	for _, ref := range refs {
		payload, err := readHandoffPendingPayloadFromPath(ref.PayloadPath)
		if err != nil {
			return nil, true, err
		}
		normalizeHandoffPendingPayloadFromRef(&payload, ref)
		completion := payload.Completion(linearClient{}, config, stateStore, nil)
		if strings.TrimSpace(completion.prURL) == "" {
			return nil, true, fmt.Errorf("handoff pending payload for %s has no PR URL", ref.IssueKey)
		}
		lock, releaseLock, err := acquireRunLockWithStateContext(ctx, stateStore, completion.workspace, completion.candidate, completion.branch, time.Now())
		if err != nil {
			if errors.Is(err, errRunLocked) {
				log("%v", err)
				continue
			}
			return nil, true, err
		}
		payload.Branch = lock.Branch
		refCopy := ref
		return &claimedHandoffPendingAttempt{Payload: payload, ReleaseLock: releaseLock, PayloadRef: &refCopy}, true, nil
	}
	return nil, false, nil
}

func normalizeHandoffPendingPayloadFromRef(payload *handoffPendingPayload, ref state.WorkerPayloadRef) {
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

func executeHandoffPendingAttempt(ctx context.Context, client linearClient, config runnerConfig, stateStore *state.Store, claimed claimedHandoffPendingAttempt) (didWork bool, err error) {
	if claimed.ReleaseLock != nil {
		defer claimed.ReleaseLock()
	}
	defer func() {
		if markErr := completeWorkerPayloadRef(ctx, stateStore, claimed.PayloadRef, err); err == nil && markErr != nil {
			err = markErr
		}
	}()
	payload := claimed.Payload
	var states []workflowState
	if strings.TrimSpace(config.HandoffState) != "" {
		if strings.TrimSpace(payload.TeamID) == "" {
			return true, fmt.Errorf("handoff pending payload for %s has no team ID", payload.IssueIdentifier)
		}
		var err error
		states, err = client.workflowStatesContext(ctx, payload.TeamID)
		if err != nil {
			return true, err
		}
	}
	return executeHandoffPendingPayload(ctx, client, config, stateStore, payload, states)
}

var githubAppEnvFromEnvironmentForHandoffWorker = githubAppEnvFromEnvironment
