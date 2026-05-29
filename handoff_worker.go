package main

import (
	"context"
	"time"

	"github.com/weskor/agent-machine/internal/state"
)

type handoffWorker struct {
	client       linearClient
	config       runnerConfig
	stateStore   *state.Store
	candidate    *issue
	states       []workflowState
	workspace    string
	startedAt    time.Time
	runtimeUsage *usage
	review       *reviewResult
	prURL        string
	validation   []string
	scopeResult  scopeGuardResult
	githubAuth   string
}

type handoffWorkerResult struct {
	Summary  *handoffSummary
	Terminal bool
}

func (w handoffWorker) Execute(ctx context.Context) (handoffWorkerResult, error) {
	if w.prURL == "" {
		return handoffWorkerResult{}, nil
	}
	if err := ctx.Err(); err != nil {
		return handoffWorkerResult{Terminal: true}, err
	}

	logHandoffRunSummary(w.candidate.Identifier, w.prURL, w.review, w.validation)
	classificationRecord := runRecordFor(w.candidate, w.workspace, w.config.RuntimeImplementationCommand(), w.githubAuth, w.startedAt, time.Now(), w.runtimeUsage, w.review, w.prURL, runAttemptStatusSuccess, "", w.config.Budget.Active(), "")
	classification := classifyRunRecord(w.workspace, classificationRecord)
	summary := handoffSummary{
		IssueIdentifier:  w.candidate.Identifier,
		IssueTitle:       w.candidate.Title,
		IssueURL:         w.candidate.URL,
		IssueDescription: w.candidate.Description,
		PRURL:            w.prURL,
		RuntimeUsage:     w.runtimeUsage,
		Review:           w.review,
		Duration:         time.Since(w.startedAt),
		Validation:       w.validation,
		ScopeResult:      w.scopeResult,
		FollowUps:        followUpLines(w.review),
		Classification:   &classification,
	}
	if progress, err := readRunProgress(w.config.WorkspaceRoot, w.candidate.Identifier); err == nil {
		summary.Progress = &progress
	}
	if err := updatePRHandoffBodyForWorker(summary); err != nil {
		writeRunRecordWithCommandStateContext(ctx, w.stateStore, w.workspace, runRecordFor(w.candidate, w.workspace, w.config.RuntimeImplementationCommand(), w.githubAuth, w.startedAt, time.Now(), w.runtimeUsage, w.review, w.prURL, runAttemptStatusFailed, err.Error(), w.config.Budget.Active(), ""))
		return handoffWorkerResult{Summary: &summary, Terminal: true}, err
	}
	if err := ctx.Err(); err != nil {
		return handoffWorkerResult{Summary: &summary, Terminal: true}, err
	}
	linearStatus := linearStatusWorker{client: w.client, candidate: w.candidate, states: w.states}
	if stateID(w.states, w.config.HandoffState) != "" {
		if _, err := linearStatus.MoveToContext(ctx, w.config.HandoffState); err != nil {
			writeRunRecordWithCommandStateContext(ctx, w.stateStore, w.workspace, runRecordFor(w.candidate, w.workspace, w.config.RuntimeImplementationCommand(), w.githubAuth, w.startedAt, time.Now(), w.runtimeUsage, w.review, w.prURL, runAttemptStatusFailed, err.Error(), w.config.Budget.Active(), ""))
			return handoffWorkerResult{Summary: &summary, Terminal: true}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return handoffWorkerResult{Summary: &summary, Terminal: true}, err
	}
	comment := renderLinearHandoffComment(summary)
	if err := linearStatus.CommentContext(ctx, comment); err != nil {
		log("failed to comment on %s: %v", w.candidate.Identifier, err)
	}
	return handoffWorkerResult{Summary: &summary}, nil
}

var updatePRHandoffBodyForWorker = updatePRHandoffBody

func resetHandoffWorkerHooks() {
	updatePRHandoffBodyForWorker = updatePRHandoffBody
	readHandoffPendingPayloadForCompletion = readHandoffPendingPayload
	githubAppEnvFromEnvironmentForHandoffWorker = githubAppEnvFromEnvironment
	resetLinearStatusWorkerHooks()
}
