package main

import (
	"path/filepath"
	"time"
)

const (
	runAttemptStatusSuccess        = "success"
	runAttemptStatusFailed         = "failed"
	runAttemptStatusReviewFailed   = "review_failed"
	runAttemptStatusReviewNotReady = "review_not_ready"
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
	RuntimeUsage   *usage
	Review         *reviewResult
	PRURL          string
	Status         string
	Error          string
	Budget         *runBudget
	BudgetExceeded string
}

func (o runAttemptOutcome) Record(candidate *issue, workspace, runtimeCommand string) runRecord {
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
	record := runRecord{IssueIdentifier: candidate.Identifier, IssueID: candidate.ID, IssueTitle: candidate.Title, IssueURL: candidate.URL, Workspace: workspace, WorkspaceRoot: root, Branch: branch, ExpectedBranch: expectedWorkspaceBranch(candidate.Identifier), RuntimeCommand: runtimeCommand, PiCommand: runtimeCommand, GitHubAuth: o.GitHubAuth, StartedAt: o.StartedAt, EndedAt: o.EndedAt, DurationMS: o.EndedAt.Sub(o.StartedAt).Milliseconds(), RuntimeUsage: o.RuntimeUsage, PiUsage: o.RuntimeUsage, ReviewStatus: reviewStatus, ReviewClassification: reviewClassification, ReviewFindings: reviewFindings, ReviewUsage: reviewUsage, PRURL: o.PRURL, FeedbackHash: feedbackHash(feedback), Status: o.Status, Error: o.Error, Budget: o.Budget, BudgetExceeded: o.BudgetExceeded}
	record.BehaviorContractEvidence = behaviorContractEvidenceForRun(record)
	record.BehaviorContractEvidence = append(record.BehaviorContractEvidence, ticketContractEvidenceForRun(record)...)
	return record
}

func (o runAttemptOutcome) TerminalOutcomeIntent() string {
	record := runRecord{Status: o.Status, Error: o.Error, PRURL: o.PRURL}
	if o.Review != nil {
		record.ReviewStatus = o.Review.Status
		record.ReviewClassification = o.Review.Classification
	}
	classification := classifyRun(runClassificationInput{Record: record, NeedsInfoUsed: o.Status == runAttemptStatusNeedsInfo || o.Status == runAttemptStatusNeedsInfoFail})
	return classification.Outcome
}

func runRecordFor(candidate *issue, workspace, runtimeCommand, githubAuth string, startedAt, endedAt time.Time, runtimeUsage *usage, review *reviewResult, prURL, status, errorMessage string, budget *runBudget, budgetExceeded string) runRecord {
	return runAttemptOutcome{GitHubAuth: githubAuth, StartedAt: startedAt, EndedAt: endedAt, RuntimeUsage: runtimeUsage, Review: review, PRURL: prURL, Status: status, Error: errorMessage, Budget: budget, BudgetExceeded: budgetExceeded}.Record(candidate, workspace, runtimeCommand)
}
