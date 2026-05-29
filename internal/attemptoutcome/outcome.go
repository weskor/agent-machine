package attemptoutcome

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/weskor/agent-machine/internal/artifacts"
	"github.com/weskor/agent-machine/internal/behaviorcontract"
	"github.com/weskor/agent-machine/internal/domain"
	"github.com/weskor/agent-machine/internal/runclassification"
	"github.com/weskor/agent-machine/internal/ticketcontract"
	ws "github.com/weskor/agent-machine/internal/workspace"
)

const (
	StatusSuccess        = "success"
	StatusFailed         = "failed"
	StatusReviewFailed   = "review_failed"
	StatusReviewNotReady = "review_not_ready"
	StatusGitHubAppError = "github_app_error"
	StatusNeedsInfo      = "needs_info"
	StatusNeedsInfoFail  = "needs_info_failed"
	StatusTimeout        = "timeout"
	StatusBudgetExceeded = "budget_exceeded"
)

// Outcome owns the status naming and run-record projection for a single
// terminal attempt path. Orchestration still decides side effects and
// transitions; this type only names the outcome and builds the attempt record.
type Outcome struct {
	GitHubAuth     string
	StartedAt      time.Time
	EndedAt        time.Time
	RuntimeUsage   *domain.Usage
	Review         *domain.ReviewResult
	PRURL          string
	Status         string
	Error          string
	Budget         *domain.Budget
	BudgetExceeded string
}

func (o Outcome) Record(candidate *domain.Issue, workspace, runtimeCommand string) domain.RunRecord {
	reviewStatus := ""
	reviewClassification := ""
	reviewFindings := ""
	var reviewUsage *domain.Usage
	if o.Review != nil {
		reviewStatus = o.Review.Status
		reviewClassification = o.Review.Classification
		reviewFindings = o.Review.Findings
		reviewUsage = o.Review.Usage
	}
	branch, _ := ws.CurrentGitBranch(workspace)
	root := filepath.Dir(workspace)
	feedback, _ := artifacts.ReadPRFeedback(workspace)
	record := domain.RunRecord{IssueIdentifier: candidate.Identifier, IssueID: candidate.ID, IssueTitle: candidate.Title, IssueURL: candidate.URL, Workspace: workspace, WorkspaceRoot: root, Branch: branch, ExpectedBranch: ExpectedWorkspaceBranch(candidate.Identifier), RuntimeCommand: runtimeCommand, PiCommand: runtimeCommand, GitHubAuth: o.GitHubAuth, StartedAt: o.StartedAt, EndedAt: o.EndedAt, DurationMS: o.EndedAt.Sub(o.StartedAt).Milliseconds(), RuntimeUsage: o.RuntimeUsage, PiUsage: o.RuntimeUsage, ReviewStatus: reviewStatus, ReviewClassification: reviewClassification, ReviewFindings: reviewFindings, ReviewUsage: reviewUsage, PRURL: o.PRURL, FeedbackHash: artifacts.FeedbackHash(feedback), Status: o.Status, Error: o.Error, Budget: o.Budget, BudgetExceeded: o.BudgetExceeded}
	record.BehaviorContractEvidence = behaviorcontract.EvidenceForRun(record)
	record.BehaviorContractEvidence = append(record.BehaviorContractEvidence, ticketcontract.EvidenceForRun(record)...)
	return record
}

func (o Outcome) TerminalOutcomeIntent() string {
	record := domain.RunRecord{Status: o.Status, Error: o.Error, PRURL: o.PRURL}
	if o.Review != nil {
		record.ReviewStatus = o.Review.Status
		record.ReviewClassification = o.Review.Classification
	}
	classification := runclassification.Classify(runclassification.Input{Record: record, NeedsInfoUsed: o.Status == StatusNeedsInfo || o.Status == StatusNeedsInfoFail})
	return classification.Outcome
}

func RecordFor(candidate *domain.Issue, workspace, runtimeCommand, githubAuth string, startedAt, endedAt time.Time, runtimeUsage *domain.Usage, review *domain.ReviewResult, prURL, status, errorMessage string, budget *domain.Budget, budgetExceeded string) domain.RunRecord {
	return Outcome{GitHubAuth: githubAuth, StartedAt: startedAt, EndedAt: endedAt, RuntimeUsage: runtimeUsage, Review: review, PRURL: prURL, Status: status, Error: errorMessage, Budget: budget, BudgetExceeded: budgetExceeded}.Record(candidate, workspace, runtimeCommand)
}

func ExpectedWorkspaceBranch(identifier string) string {
	return "am/" + strings.TrimSpace(identifier) + "-workspace"
}
