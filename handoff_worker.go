package main

import (
	"context"
	"time"

	"github.com/weskor/pi-symphony/internal/state"
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
	githubAuth   string
}

type handoffWorkerResult struct {
	Summary  *handoffSummary
	Terminal bool
}

func (w handoffWorker) Execute(ctx context.Context) (handoffWorkerResult, error) {
	_ = ctx
	if w.prURL == "" {
		return handoffWorkerResult{}, nil
	}

	logHandoffRunSummary(w.candidate.Identifier, w.prURL, w.review, w.validation)
	classificationRecord := runRecordFor(w.candidate, w.workspace, configuredRuntimeCommand(w.config), w.githubAuth, w.startedAt, time.Now(), w.runtimeUsage, w.review, w.prURL, runAttemptStatusSuccess, "", w.config.Budget.Active(), "")
	classification := classifyRunRecord(w.workspace, classificationRecord)
	summary := handoffSummary{
		IssueIdentifier: w.candidate.Identifier,
		IssueTitle:      w.candidate.Title,
		IssueURL:        w.candidate.URL,
		PRURL:           w.prURL,
		RuntimeUsage:    w.runtimeUsage,
		Review:          w.review,
		Duration:        time.Since(w.startedAt),
		Validation:      w.validation,
		FollowUps:       followUpLines(w.review),
		Classification:  &classification,
	}
	if err := postOrUpdatePRHandoffCommentForWorker(summary); err != nil {
		log("failed to post GitHub handoff comment for %s: %v", w.prURL, err)
	}
	linearStatus := linearStatusWorker{client: w.client, candidate: w.candidate, states: w.states}
	if stateID(w.states, w.config.HandoffState) != "" {
		if _, err := linearStatus.MoveTo(w.config.HandoffState); err != nil {
			writeRunRecordWithCommandState(w.stateStore, w.workspace, runRecordFor(w.candidate, w.workspace, configuredRuntimeCommand(w.config), w.githubAuth, w.startedAt, time.Now(), w.runtimeUsage, w.review, w.prURL, runAttemptStatusFailed, err.Error(), w.config.Budget.Active(), ""))
			return handoffWorkerResult{Summary: &summary, Terminal: true}, err
		}
	}
	comment := renderLinearHandoffComment(summary)
	if err := linearStatus.Comment(comment); err != nil {
		log("failed to comment on %s: %v", w.candidate.Identifier, err)
	}
	return handoffWorkerResult{Summary: &summary}, nil
}

var postOrUpdatePRHandoffCommentForWorker = postOrUpdatePRHandoffComment

func resetHandoffWorkerHooks() {
	postOrUpdatePRHandoffCommentForWorker = postOrUpdatePRHandoffComment
	readHandoffPendingPayloadForCompletion = readHandoffPendingPayload
	githubAppEnvFromEnvironmentForHandoffWorker = githubAppEnvFromEnvironment
	resetLinearStatusWorkerHooks()
}
