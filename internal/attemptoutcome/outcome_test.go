package attemptoutcome

import (
	"testing"
	"time"

	"github.com/weskor/agent-machine/internal/domain"
	"github.com/weskor/agent-machine/internal/reviewpolicy"
)

func TestOutcomeRecordCharacterizesTerminalCases(t *testing.T) {
	now := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	candidate := &domain.Issue{ID: "issue-id", Identifier: "CAG-68", Title: "Extract outcomes", URL: "https://linear.app/acme/issue/CAG-68/extract-outcomes"}
	usage := &domain.Usage{TotalTokens: 42}
	budget := &domain.Budget{}

	tests := []struct {
		name       string
		outcome    Outcome
		wantStatus string
		wantIntent string
		wantPR     string
		wantReview string
		wantErr    string
		wantBudget string
	}{
		{
			name:       "timeout",
			outcome:    Outcome{Status: StatusTimeout, Error: "command timed out", BudgetExceeded: "command timed out", Budget: budget},
			wantStatus: StatusTimeout,
			wantIntent: StatusTimeout,
			wantErr:    "command timed out",
			wantBudget: "command timed out",
		},
		{
			name:       "budget exceeded",
			outcome:    Outcome{Status: StatusBudgetExceeded, Error: "pi token budget exceeded", BudgetExceeded: "pi token budget exceeded", Budget: budget},
			wantStatus: StatusBudgetExceeded,
			wantIntent: StatusBudgetExceeded,
			wantErr:    "pi token budget exceeded",
			wantBudget: "pi token budget exceeded",
		},
		{
			name:       "GitHub App error",
			outcome:    Outcome{Status: StatusFailed, GitHubAuth: StatusGitHubAppError, Error: "missing app installation"},
			wantStatus: StatusFailed,
			wantIntent: "operational_failure",
			wantErr:    "missing app installation",
		},
		{
			name:       "runtime failure",
			outcome:    Outcome{Status: StatusFailed, Error: "exit status 1", RuntimeUsage: usage, PRURL: "https://github.com/acme/repo/pull/1"},
			wantStatus: StatusFailed,
			wantIntent: "operational_failure",
			wantPR:     "https://github.com/acme/repo/pull/1",
			wantErr:    "exit status 1",
		},
		{
			name:       "NEEDS_INFO",
			outcome:    Outcome{Status: StatusNeedsInfo, Error: "What is in scope?"},
			wantStatus: StatusNeedsInfo,
			wantIntent: "needs_info",
			wantErr:    "What is in scope?",
		},
		{
			name:       "review failed behavior blocker",
			outcome:    Outcome{Status: StatusReviewFailed, Review: &domain.ReviewResult{Status: "failed", Classification: reviewpolicy.BehaviorSpecBlocker, Findings: "behavior mismatch"}, PRURL: "https://github.com/acme/repo/pull/3", Error: "review did not pass"},
			wantStatus: StatusReviewFailed,
			wantIntent: "review_failed",
			wantPR:     "https://github.com/acme/repo/pull/3",
			wantReview: "failed",
			wantErr:    "review did not pass",
		},
		{
			name:       "missing evidence human handoff",
			outcome:    Outcome{Status: StatusSuccess, Review: &domain.ReviewResult{Status: "failed", Classification: reviewpolicy.MissingEvidenceOnly, Findings: "missing behavior evidence"}, PRURL: "https://github.com/acme/repo/pull/4"},
			wantStatus: StatusSuccess,
			wantIntent: "human_review",
			wantPR:     "https://github.com/acme/repo/pull/4",
			wantReview: "failed",
		},
		{
			name:       "success",
			outcome:    Outcome{Status: StatusSuccess, Review: &domain.ReviewResult{Status: "passed"}, PRURL: "https://github.com/acme/repo/pull/5", RuntimeUsage: usage},
			wantStatus: StatusSuccess,
			wantIntent: "handoff_ready",
			wantPR:     "https://github.com/acme/repo/pull/5",
			wantReview: "passed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspace := t.TempDir()
			tt.outcome.StartedAt = now
			tt.outcome.EndedAt = now.Add(2 * time.Second)
			record := tt.outcome.Record(candidate, workspace, "pi")

			if record.Status != tt.wantStatus || tt.outcome.TerminalOutcomeIntent() != tt.wantIntent {
				t.Fatalf("status/intent = %q/%q, want %q/%q", record.Status, tt.outcome.TerminalOutcomeIntent(), tt.wantStatus, tt.wantIntent)
			}
			if record.PRURL != tt.wantPR || record.ReviewStatus != tt.wantReview || record.Error != tt.wantErr || record.BudgetExceeded != tt.wantBudget {
				t.Fatalf("record fields = pr %q review %q err %q budget %q", record.PRURL, record.ReviewStatus, record.Error, record.BudgetExceeded)
			}
		})
	}
}

func TestRecordForProjectsExistingOutcomeShape(t *testing.T) {
	now := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	candidate := &domain.Issue{ID: "issue-id", Identifier: "CAG-68", Title: "Extract outcomes", URL: "https://linear.app/acme/issue/CAG-68/extract-outcomes"}
	sampleUsage := &domain.Usage{Input: 10, Output: 5, TotalTokens: 15}
	budget := &domain.Budget{}

	record := RecordFor(candidate, t.TempDir(), "pi", "github_app_error", now, now.Add(2*time.Second), sampleUsage, &domain.ReviewResult{Status: "passed"}, "https://github.com/acme/repo/pull/5", StatusSuccess, "", budget, "")

	if record.Status != StatusSuccess || record.PRURL == "" || record.ReviewStatus != "passed" || record.GitHubAuth != "github_app_error" {
		t.Fatalf("unexpected run record: %#v", record)
	}
	if record.IssueIdentifier != candidate.Identifier || record.WorkspaceRoot == "" || record.DurationMS != 2000 {
		t.Fatalf("unexpected identity/timing fields: %#v", record)
	}
	if len(record.BehaviorContractEvidence) == 0 {
		t.Fatal("expected behavior-contract evidence to be projected")
	}
}
