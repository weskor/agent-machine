package main

import (
	"context"
	"time"

	"github.com/weskor/pi-symphony/internal/state"
)

const runProgressPhaseHandoffPending = "handoff_pending"

type handoffCompletion struct {
	client          linearClient
	config          runnerConfig
	stateStore      *state.Store
	candidate       *issue
	states          []workflowState
	workspace       string
	branch          string
	progressStarted time.Time
	startedAt       time.Time
	piUsage         *usage
	review          *reviewResult
	prURL           string
	validation      []string
	githubAuth      string
}

func completeAttemptHandoff(ctx context.Context, input handoffCompletion) (bool, error) {
	writeHandoffPendingProgress(input)
	handoffResult, err := handoffWorker{
		client:     input.client,
		config:     input.config,
		stateStore: input.stateStore,
		candidate:  input.candidate,
		states:     input.states,
		workspace:  input.workspace,
		startedAt:  input.startedAt,
		piUsage:    input.piUsage,
		review:     input.review,
		prURL:      input.prURL,
		validation: input.validation,
		githubAuth: input.githubAuth,
	}.Execute(ctx)
	if err != nil || handoffResult.Terminal {
		return true, err
	}
	if err := writeRunRecordWithCommandState(input.stateStore, input.workspace, runRecordFor(input.candidate, input.workspace, input.config.PiCommand, input.githubAuth, input.startedAt, time.Now(), input.piUsage, input.review, input.prURL, runAttemptStatusSuccess, "", input.config.Budget.Active(), "")); err != nil {
		return true, err
	}
	return true, nil
}

func writeHandoffPendingProgress(input handoffCompletion) {
	if input.candidate == nil {
		return
	}
	progress := runProgressForIssue(input.candidate, input.workspace, runProgressPhaseHandoffPending, input.progressStarted)
	progress.Branch = input.branch
	progress.PRURL = input.prURL
	progress.Status = runProgressPhaseHandoffPending
	progress.NextAction = "complete_runner_handoff"
	if input.review != nil {
		progress.ReviewStatus = input.review.Status
		progress.ReviewClassification = input.review.Classification
	}
	writeRunProgress(input.config.WorkspaceRoot, progress)
}
