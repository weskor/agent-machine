package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	sh "github.com/weskor/pi-symphony/internal/shell"
	"github.com/weskor/pi-symphony/internal/state"
)

type reviewWorker struct {
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
	prURL           string
	githubEnv       map[string]string
	githubAuth      string
	scopeResult     scopeGuardResult
	validation      []string
	resume          bool
}

type reviewWorkerResult struct {
	Review   *reviewResult
	Terminal bool
}

func (w reviewWorker) Execute(ctx context.Context) (reviewWorkerResult, error) {
	if w.prURL == "" || w.config.ReviewCommand == "" {
		return reviewWorkerResult{}, nil
	}
	readiness := newReviewReadinessModule(w.config.WorkspaceRoot)
	evidence, err := collectReviewEvidenceForWorker(w.config, w.candidate, w.workspace, w.prURL, w.scopeResult, w.validation)
	if err != nil {
		writeRunRecordWithCommandState(w.stateStore, w.workspace, runRecordFor(w.candidate, w.workspace, w.config.PiCommand, w.githubAuth, w.startedAt, time.Now(), w.piUsage, nil, w.prURL, runAttemptStatusFailed, err.Error(), w.config.Budget.Active(), err.Error()))
		return reviewWorkerResult{Terminal: true}, err
	}
	if err := reviewEvidenceNotReadyError(evidence); err != nil {
		decision := readiness.NotReadyDecision(w.prURL, evidence)
		notReady := readiness.NotReadyProgress(w.candidate, w.workspace, w.branch, w.prURL, w.progressStarted, evidence)
		if w.resume {
			notReady = readiness.ResumeNotReadyProgress(w.candidate, w.workspace, w.branch, w.prURL, w.progressStarted, evidence)
		}
		writeRunProgress(w.config.WorkspaceRoot, notReady)
		writeRunRecordWithCommandState(w.stateStore, w.workspace, runRecordFor(w.candidate, w.workspace, w.config.PiCommand, w.githubAuth, w.startedAt, time.Now(), w.piUsage, nil, w.prURL, decision.Status, err.Error(), w.config.Budget.Active(), err.Error()))
		return reviewWorkerResult{Terminal: true}, nil
	}

	reviewing := runProgressForIssue(w.candidate, w.workspace, "reviewing", w.progressStarted)
	reviewing.Branch = w.branch
	reviewing.PRURL = w.prURL
	reviewing.ChecksStatus = evidence.ChecksStatus
	writeRunProgress(w.config.WorkspaceRoot, reviewing)
	review, err := runReviewForWorker(w.config.ReviewCommand, w.workspace, w.candidate, w.prURL, w.githubEnv, w.config.Budget.ReviewTimeout, &evidence)
	if err != nil {
		status := runAttemptStatusReviewFailed
		if errors.Is(err, sh.ErrCommandTimeout) {
			status = runAttemptStatusTimeout
			if commentErr := w.client.createComment(w.candidate.ID, renderBudgetFailureComment(err.Error())); commentErr != nil {
				log("failed to comment on %s: %v", w.candidate.Identifier, commentErr)
			}
		}
		writeRunRecordWithCommandState(w.stateStore, w.workspace, runRecordFor(w.candidate, w.workspace, w.config.PiCommand, w.githubAuth, w.startedAt, time.Now(), w.piUsage, review, w.prURL, status, err.Error(), w.config.Budget.Active(), err.Error()))
		return reviewWorkerResult{Review: review, Terminal: true}, err
	}
	if exceeded := budgetExceeded(w.config.Budget, w.startedAt, w.piUsage, review.Usage); exceeded != "" {
		decision := budgetLifecycleDecision(attemptLifecyclePhaseReview, w.prURL, exceeded)
		if err := w.client.createComment(w.candidate.ID, renderBudgetFailureComment(exceeded)); err != nil {
			log("failed to comment on %s: %v", w.candidate.Identifier, err)
		}
		writeRunRecordWithCommandState(w.stateStore, w.workspace, runRecordFor(w.candidate, w.workspace, w.config.PiCommand, w.githubAuth, w.startedAt, time.Now(), w.piUsage, review, w.prURL, decision.Status, exceeded, w.config.Budget.Active(), exceeded))
		return reviewWorkerResult{Review: review, Terminal: true}, fmt.Errorf("%s", exceeded)
	}
	if review != nil && review.Status != "passed" && !reviewFailureRoutesToHumanHandoff(review, w.prURL) {
		if id := stateID(w.states, w.config.ReadyState); id != "" {
			if err := w.client.updateIssueState(w.candidate.ID, id); err != nil {
				writeRunRecordWithCommandState(w.stateStore, w.workspace, runRecordFor(w.candidate, w.workspace, w.config.PiCommand, w.githubAuth, w.startedAt, time.Now(), w.piUsage, review, w.prURL, runAttemptStatusReviewFailed, err.Error(), w.config.Budget.Active(), ""))
				return reviewWorkerResult{Review: review, Terminal: true}, err
			}
		}
		comment := fmt.Sprintf("Go/Pi review did not pass; moved back to %s.\n\nPR: %s\nReview status: %s\nFindings:\n%s", w.config.ReadyState, w.prURL, review.Status, review.Findings)
		if err := w.client.createComment(w.candidate.ID, comment); err != nil {
			log("failed to comment on %s: %v", w.candidate.Identifier, err)
		}
		if err := writeRunRecordWithCommandState(w.stateStore, w.workspace, runRecordFor(w.candidate, w.workspace, w.config.PiCommand, w.githubAuth, w.startedAt, time.Now(), w.piUsage, review, w.prURL, runAttemptStatusReviewFailed, "review did not pass", w.config.Budget.Active(), "")); err != nil {
			return reviewWorkerResult{Review: review, Terminal: true}, err
		}
		log("review did not pass for %s; moved back to %s", w.candidate.Identifier, w.config.ReadyState)
		return reviewWorkerResult{Review: review, Terminal: true}, nil
	}
	if reviewFailureRoutesToHumanHandoff(review, w.prURL) {
		log("review failed for %s with missing evidence only; routing to %s", w.candidate.Identifier, w.config.HandoffState)
	}
	return reviewWorkerResult{Review: review}, nil
}

var collectReviewEvidenceForWorker = collectReviewEvidence
var runReviewForWorker = runReview

func resetReviewWorkerHooks() {
	collectReviewEvidenceForWorker = collectReviewEvidence
	runReviewForWorker = runReview
}
