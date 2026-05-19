package main

import (
	"path/filepath"
	"time"
)

const (
	runAttemptStatusSuccess        = "success"
	runAttemptStatusFailed         = "failed"
	runAttemptStatusReviewFailed   = "review_failed"
	runAttemptStatusGitHubAppError = "github_app_error"
	runAttemptStatusNeedsInfo      = "needs_info"
	runAttemptStatusNeedsInfoFail  = "needs_info_failed"
	runAttemptStatusTimeout        = "timeout"
	runAttemptStatusBudgetExceeded = "budget_exceeded"
)

// runAttemptOutcome owns the status naming and run-record projection for a
// single runOne terminal path. Orchestration still decides side effects and
// transitions; this type only names the outcome and builds the attempt record.
type runAttemptOutcome struct {
	GitHubAuth     string
	StartedAt      time.Time
	EndedAt        time.Time
	PiUsage        *usage
	Review         *reviewResult
	PRURL          string
	Status         string
	Error          string
	Budget         *runBudget
	BudgetExceeded string
}

func (o runAttemptOutcome) Record(candidate *issue, workspace, piCommand string) runRecord {
	reviewStatus := ""
	reviewClassification := ""
	reviewFindings := ""
	var reviewUsage *usage
	if o.Review != nil {
		reviewStatus = o.Review.Status
		reviewClassification = o.Review.Classification
		reviewFindings = o.Review.Findings
		reviewUsage = o.Review.Usage
	}
	branch, _ := currentGitBranch(workspace)
	root := filepath.Dir(workspace)
	feedback, _ := readPRFeedback(workspace)
	record := runRecord{IssueIdentifier: candidate.Identifier, IssueID: candidate.ID, IssueTitle: candidate.Title, IssueURL: candidate.URL, Workspace: workspace, WorkspaceRoot: root, Branch: branch, ExpectedBranch: expectedWorkspaceBranch(candidate.Identifier), PiCommand: piCommand, GitHubAuth: o.GitHubAuth, StartedAt: o.StartedAt, EndedAt: o.EndedAt, DurationMS: o.EndedAt.Sub(o.StartedAt).Milliseconds(), PiUsage: o.PiUsage, ReviewStatus: reviewStatus, ReviewClassification: reviewClassification, ReviewFindings: reviewFindings, ReviewUsage: reviewUsage, PRURL: o.PRURL, FeedbackHash: feedbackHash(feedback), Status: o.Status, Error: o.Error, Budget: o.Budget, BudgetExceeded: o.BudgetExceeded}
	record.BehaviorContractEvidence = behaviorContractEvidenceForRun(record)
	record.BehaviorContractEvidence = append(record.BehaviorContractEvidence, ticketContractEvidenceForRun(record)...)
	return record
}

func (o runAttemptOutcome) TerminalOutcomeIntent() string {
	if o.Status == runAttemptStatusSuccess && o.Review != nil && o.Review.Status == "passed" {
		return "handoff_ready"
	}
	if o.Status == runAttemptStatusNeedsInfo || o.Status == runAttemptStatusNeedsInfoFail {
		return "needs_info"
	}
	if o.Review != nil && o.Review.Status == "failed" && o.Review.Classification == reviewClassificationMissingEvidenceOnly {
		return "human_review"
	}
	if o.Status == runAttemptStatusReviewFailed || (o.Review != nil && o.Review.Status == "failed") {
		return "review_failed"
	}
	if o.Status == runAttemptStatusTimeout || o.Status == runAttemptStatusBudgetExceeded {
		return o.Status
	}
	if o.Error != "" || o.Status == runAttemptStatusFailed || o.Status == runAttemptStatusGitHubAppError || o.Status == runAttemptStatusNeedsInfoFail {
		return "operational_failure"
	}
	return o.Status
}

func runRecordFor(candidate *issue, workspace, piCommand, githubAuth string, startedAt, endedAt time.Time, piUsage *usage, review *reviewResult, prURL, status, errorMessage string, budget *runBudget, budgetExceeded string) runRecord {
	return runAttemptOutcome{GitHubAuth: githubAuth, StartedAt: startedAt, EndedAt: endedAt, PiUsage: piUsage, Review: review, PRURL: prURL, Status: status, Error: errorMessage, Budget: budget, BudgetExceeded: budgetExceeded}.Record(candidate, workspace, piCommand)
}
