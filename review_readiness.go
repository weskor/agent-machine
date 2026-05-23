package main

import (
	"errors"
	"fmt"
	"strings"
	"time"

	sh "github.com/weskor/pi-symphony/internal/shell"
	"github.com/weskor/pi-symphony/internal/state"
)

type reviewReadinessModule struct {
	workspaceRoot string
}

func newReviewReadinessModule(workspaceRoot string) reviewReadinessModule {
	return reviewReadinessModule{workspaceRoot: workspaceRoot}
}

func shouldResumeReviewReadiness(workspaceRoot, issueIdentifier string, pr pullRequestSummary) bool {
	return newReviewReadinessModule(workspaceRoot).ShouldResume(issueIdentifier, pr)
}

func (m reviewReadinessModule) ShouldResume(issueIdentifier string, pr pullRequestSummary) bool {
	snapshot, err := readRunProgress(m.workspaceRoot, issueIdentifier)
	if err != nil {
		return false
	}
	if snapshot.Phase != "review_not_ready" {
		return false
	}
	if strings.TrimSpace(snapshot.PRURL) != "" && snapshot.PRURL != pr.URL {
		return false
	}
	status, _ := reviewChecksStatus(pr.StatusCheckRollup)
	return status == "success"
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
	prURL := selectedPR.URL
	reviewReadiness := newReviewReadinessModule(config.WorkspaceRoot)
	scopeResult, err := checkScopeGuard(candidate.Description, workspace, config.BaseBranch)
	if err != nil {
		writeRunRecordWithCommandState(stateStore, workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, runStarted, time.Now(), nil, nil, prURL, runAttemptStatusFailed, err.Error(), config.Budget.Active(), err.Error()))
		return true, err
	}
	validation := []string{"Implementation was preserved from prior runner-owned PR handoff; semantic review resumed after GitHub checks became terminal."}
	if strings.TrimSpace(scopeResult.Summary()) != "" {
		validation = append(validation, "Scope guard: "+scopeResult.Summary())
	} else if scopeResult.Checked {
		validation = append(validation, "Scope guard: changed files matched the Linear ticket path contract.")
	}
	evidence, err := collectReviewEvidence(config, candidate, workspace, prURL, scopeResult, validation)
	if err != nil {
		writeRunRecordWithCommandState(stateStore, workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, runStarted, time.Now(), nil, nil, prURL, runAttemptStatusFailed, err.Error(), config.Budget.Active(), err.Error()))
		return true, err
	}
	if err := reviewEvidenceNotReadyError(evidence); err != nil {
		decision := reviewReadiness.NotReadyDecision(prURL, evidence)
		notReady := reviewReadiness.ResumeNotReadyProgress(candidate, workspace, branch, prURL, progressStarted, evidence)
		writeRunProgress(config.WorkspaceRoot, notReady)
		writeRunRecordWithCommandState(stateStore, workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, runStarted, time.Now(), nil, nil, prURL, decision.Status, err.Error(), config.Budget.Active(), err.Error()))
		return true, nil
	}
	reviewing := runProgressForIssue(candidate, workspace, "reviewing", progressStarted)
	reviewing.Branch = branch
	reviewing.PRURL = prURL
	reviewing.ChecksStatus = evidence.ChecksStatus
	writeRunProgress(config.WorkspaceRoot, reviewing)
	review, err := runReview(config.ReviewCommand, workspace, candidate, prURL, githubEnv, config.Budget.ReviewTimeout, &evidence)
	if err != nil {
		status := runAttemptStatusReviewFailed
		if errors.Is(err, sh.ErrCommandTimeout) {
			status = runAttemptStatusTimeout
			_ = client.createComment(candidate.ID, renderBudgetFailureComment(err.Error()))
		}
		writeRunRecordWithCommandState(stateStore, workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, runStarted, time.Now(), nil, review, prURL, status, err.Error(), config.Budget.Active(), err.Error()))
		return true, err
	}
	if review != nil && review.Status != "passed" && !reviewFailureRoutesToHumanHandoff(review, prURL) {
		if id := stateID(states, config.ReadyState); id != "" {
			if err := client.updateIssueState(candidate.ID, id); err != nil {
				writeRunRecordWithCommandState(stateStore, workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, runStarted, time.Now(), nil, review, prURL, runAttemptStatusReviewFailed, err.Error(), config.Budget.Active(), ""))
				return true, err
			}
		}
		_ = client.createComment(candidate.ID, fmt.Sprintf("Go/Pi review did not pass; moved back to %s.\n\nPR: %s\nReview status: %s\nFindings:\n%s", config.ReadyState, prURL, review.Status, review.Findings))
		writeRunRecordWithCommandState(stateStore, workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, runStarted, time.Now(), nil, review, prURL, runAttemptStatusReviewFailed, "review did not pass", config.Budget.Active(), ""))
		return true, nil
	}
	classificationRecord := runRecordFor(candidate, workspace, config.PiCommand, githubAuth, runStarted, time.Now(), nil, review, prURL, runAttemptStatusSuccess, "", config.Budget.Active(), "")
	classification := classifyRunRecord(workspace, classificationRecord)
	summary := handoffSummary{IssueIdentifier: candidate.Identifier, IssueTitle: candidate.Title, IssueURL: candidate.URL, PRURL: prURL, Review: review, Duration: time.Since(runStarted), Validation: validation, FollowUps: followUpLines(review), Classification: &classification}
	if err := postOrUpdatePRHandoffComment(summary); err != nil {
		log("failed to post GitHub handoff comment for %s: %v", prURL, err)
	}
	if id := stateID(states, config.HandoffState); id != "" {
		if err := client.updateIssueState(candidate.ID, id); err != nil {
			writeRunRecordWithCommandState(stateStore, workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, runStarted, time.Now(), nil, review, prURL, runAttemptStatusFailed, err.Error(), config.Budget.Active(), ""))
			return true, err
		}
	}
	_ = client.createComment(candidate.ID, renderLinearHandoffComment(summary))
	if err := writeRunRecordWithCommandState(stateStore, workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, runStarted, time.Now(), nil, review, prURL, runAttemptStatusSuccess, "", config.Budget.Active(), "")); err != nil {
		return true, err
	}
	return true, nil
}
