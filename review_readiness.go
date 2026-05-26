package main

import (
	"context"
	"strings"
	"time"

	"github.com/weskor/agent-machine/internal/state"
)

type reviewReadinessModule struct {
	workspaceRoot string
}

func newReviewReadinessModule(workspaceRoot string) reviewReadinessModule {
	return reviewReadinessModule{workspaceRoot: workspaceRoot}
}

func (m reviewReadinessModule) NotReadyProgress(candidate *issue, workspace, branch, prURL string, startedAt time.Time, evidence reviewEvidence) runProgressSnapshot {
	decision := m.NotReadyDecision(prURL, evidence)
	notReady := runProgressForIssue(candidate, workspace, "review_not_ready", startedAt)
	notReady.Branch = branch
	notReady.PRURL = prURL
	notReady.Status = decision.TerminalOutcomeIntent
	notReady.ChecksStatus = evidence.ChecksStatus
	notReady.NextAction = decision.NextAction
	if evidence.ChecksStatus == "failed" {
		notReady.NextAction = "fix_failing_github_checks_before_review"
	}
	notReady.Error = evidence.ChecksSummary
	return notReady
}

func (m reviewReadinessModule) NotReadyDecision(prURL string, evidence reviewEvidence) attemptLifecycleDecision {
	return decideAttemptLifecycle(attemptLifecycleInput{
		Phase:          attemptLifecyclePhaseReviewReadiness,
		PRURL:          prURL,
		ReviewNotReady: true,
		Error:          reviewNotReadyErrorText(evidence),
	})
}

func reviewNotReadyErrorText(evidence reviewEvidence) string {
	if strings.TrimSpace(evidence.ChecksSummary) != "" {
		return evidence.ChecksSummary
	}
	return "review not ready"
}

func (m reviewReadinessModule) ResumeNotReadyProgress(candidate *issue, workspace, branch, prURL string, startedAt time.Time, evidence reviewEvidence) runProgressSnapshot {
	notReady := m.NotReadyProgress(candidate, workspace, branch, prURL, startedAt, evidence)
	notReady.NextAction = "wait_for_github_checks_then_retry"
	return notReady
}

func resumeReviewReadyRun(client linearClient, stateStore *state.Store, config runnerConfig, candidate *issue, states []workflowState, workspace, branch string, githubEnv map[string]string, githubAuth string, progressStarted, runStarted time.Time, selectedPR *pullRequestSummary) (bool, error) {
	return resumeReviewReadyRunContext(context.Background(), client, stateStore, config, candidate, states, workspace, branch, githubEnv, githubAuth, progressStarted, runStarted, selectedPR)
}

func resumeReviewReadyRunContext(ctx context.Context, client linearClient, stateStore *state.Store, config runnerConfig, candidate *issue, states []workflowState, workspace, branch string, githubEnv map[string]string, githubAuth string, progressStarted, runStarted time.Time, selectedPR *pullRequestSummary) (bool, error) {
	prURL := selectedPR.URL
	scopeResult, err := checkScopeGuardForReviewResume(ctx, candidate.Description, workspace, config.BaseBranch)
	if err != nil {
		writeRunRecordWithCommandStateContext(ctx, stateStore, workspace, runRecordFor(candidate, workspace, config.RuntimeImplementationCommand(), githubAuth, runStarted, time.Now(), nil, nil, prURL, runAttemptStatusFailed, err.Error(), config.Budget.Active(), err.Error()))
		return true, err
	}
	validation := []string{"Implementation was preserved from prior runner-owned PR/MR handoff; semantic review resumed after code-host checks became terminal."}
	if strings.TrimSpace(scopeResult.Summary()) != "" {
		validation = append(validation, "Scope guard: "+scopeResult.Summary())
	} else if scopeResult.Checked {
		validation = append(validation, "Scope guard: changed files matched the Linear ticket path contract.")
	}
	result, err := reviewWorker{client: client, config: config, stateStore: stateStore, candidate: candidate, states: states, workspace: workspace, branch: branch, progressStarted: progressStarted, startedAt: runStarted, prURL: prURL, githubEnv: githubEnv, githubAuth: githubAuth, scopeResult: scopeResult, validation: validation, resume: true}.Execute(ctx)
	if err != nil || result.Terminal {
		return true, err
	}
	review := result.Review
	return completeAttemptHandoff(ctx, handoffCompletion{client: client, config: config, stateStore: stateStore, candidate: candidate, states: states, workspace: workspace, branch: branch, progressStarted: progressStarted, startedAt: runStarted, review: review, prURL: prURL, validation: validation, githubAuth: githubAuth})
}

var checkScopeGuardForReviewResume = checkScopeGuardContext
