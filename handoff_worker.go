package main

import (
	"context"
	"time"

	"github.com/weskor/pi-symphony/internal/state"
)

type handoffWorker struct {
	client     linearClient
	config     runnerConfig
	stateStore *state.Store
	candidate  *issue
	states     []workflowState
	workspace  string
	startedAt  time.Time
	piUsage    *usage
	review     *reviewResult
	prURL      string
	validation []string
	githubAuth string
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
	classificationRecord := runRecordFor(w.candidate, w.workspace, w.config.PiCommand, w.githubAuth, w.startedAt, time.Now(), w.piUsage, w.review, w.prURL, runAttemptStatusSuccess, "", w.config.Budget.Active(), "")
	classification := classifyRunRecord(w.workspace, classificationRecord)
	summary := handoffSummary{
		IssueIdentifier: w.candidate.Identifier,
		IssueTitle:      w.candidate.Title,
		IssueURL:        w.candidate.URL,
		PRURL:           w.prURL,
		PiUsage:         w.piUsage,
		Review:          w.review,
		Duration:        time.Since(w.startedAt),
		Validation:      w.validation,
		FollowUps:       followUpLines(w.review),
		Classification:  &classification,
	}
	if err := postOrUpdatePRHandoffCommentForWorker(summary); err != nil {
		log("failed to post GitHub handoff comment for %s: %v", w.prURL, err)
	}
	if id := stateID(w.states, w.config.HandoffState); id != "" {
		if err := updateIssueStateForHandoffWorker(w.client, w.candidate.ID, id); err != nil {
			writeRunRecordWithCommandState(w.stateStore, w.workspace, runRecordFor(w.candidate, w.workspace, w.config.PiCommand, w.githubAuth, w.startedAt, time.Now(), w.piUsage, w.review, w.prURL, runAttemptStatusFailed, err.Error(), w.config.Budget.Active(), ""))
			return handoffWorkerResult{Summary: &summary, Terminal: true}, err
		}
		log("moved %s to %s", w.candidate.Identifier, w.config.HandoffState)
	}
	comment := renderLinearHandoffComment(summary)
	if err := createCommentForHandoffWorker(w.client, w.candidate.ID, comment); err != nil {
		log("failed to comment on %s: %v", w.candidate.Identifier, err)
	}
	return handoffWorkerResult{Summary: &summary}, nil
}

var postOrUpdatePRHandoffCommentForWorker = postOrUpdatePRHandoffComment
var updateIssueStateForHandoffWorker = func(client linearClient, issueID, stateID string) error {
	return client.updateIssueState(issueID, stateID)
}
var createCommentForHandoffWorker = func(client linearClient, issueID, body string) error {
	return client.createComment(issueID, body)
}

func resetHandoffWorkerHooks() {
	postOrUpdatePRHandoffCommentForWorker = postOrUpdatePRHandoffComment
	updateIssueStateForHandoffWorker = func(client linearClient, issueID, stateID string) error {
		return client.updateIssueState(issueID, stateID)
	}
	createCommentForHandoffWorker = func(client linearClient, issueID, body string) error {
		return client.createComment(issueID, body)
	}
}
