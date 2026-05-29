package main

import (
	"context"
	"errors"
	"time"

	"github.com/weskor/agent-machine/internal/state"
)

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
	if workerErr != nil && !errors.Is(workerErr, errTerminalPullRequest) {
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
	if workerErr != nil && !errors.Is(workerErr, errTerminalPullRequest) {
		status = state.PRHandoffIntentStatusFailed
		errorText = workerErr.Error()
	}
	return store.CompletePRHandoffIntent(completeCtx, payload.IssueIdentifier, 1, status, prURL, errorText, time.Now().UTC())
}
