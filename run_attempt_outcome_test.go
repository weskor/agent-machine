package main

import (
	"testing"
	"time"
)

func TestRunAttemptOutcomeRecordCharacterizesTerminalCases(t *testing.T) {
	now := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	candidate := &issue{ID: "issue-id", Identifier: "CAG-68", Title: "Extract outcomes", URL: "https://linear.app/acme/issue/CAG-68/extract-outcomes"}
	usage := &usage{TotalTokens: 42}
	budget := (&runBudget{PiText: "1s", PiTimeout: time.Second}).Active()

	tests := []struct {
		name       string
		outcome    runAttemptOutcome
		wantStatus string
		wantIntent string
		wantPR     string
		wantReview string
		wantErr    string
		wantBudget string
	}{
		{
			name:       "timeout",
			outcome:    runAttemptOutcome{Status: runAttemptStatusTimeout, Error: "command timed out", BudgetExceeded: "command timed out", Budget: budget},
			wantStatus: runAttemptStatusTimeout,
			wantIntent: runAttemptStatusTimeout,
			wantErr:    "command timed out",
			wantBudget: "command timed out",
		},
		{
			name:       "budget exceeded",
			outcome:    runAttemptOutcome{Status: runAttemptStatusBudgetExceeded, Error: "pi token budget exceeded", BudgetExceeded: "pi token budget exceeded", Budget: budget},
			wantStatus: runAttemptStatusBudgetExceeded,
			wantIntent: runAttemptStatusBudgetExceeded,
			wantErr:    "pi token budget exceeded",
			wantBudget: "pi token budget exceeded",
		},
		{
			name:       "GitHub App error",
			outcome:    runAttemptOutcome{Status: runAttemptStatusFailed, GitHubAuth: runAttemptStatusGitHubAppError, Error: "missing app installation"},
			wantStatus: runAttemptStatusFailed,
			wantIntent: "operational_failure",
			wantErr:    "missing app installation",
		},
		{
			name:       "Pi failure",
			outcome:    runAttemptOutcome{Status: runAttemptStatusFailed, Error: "exit status 1", PiUsage: usage, PRURL: "https://github.com/acme/repo/pull/1"},
			wantStatus: runAttemptStatusFailed,
			wantIntent: "operational_failure",
			wantPR:     "https://github.com/acme/repo/pull/1",
			wantErr:    "exit status 1",
		},
		{
			name:       "NEEDS_INFO",
			outcome:    runAttemptOutcome{Status: runAttemptStatusNeedsInfo, Error: "What is in scope?"},
			wantStatus: runAttemptStatusNeedsInfo,
			wantIntent: "needs_info",
			wantErr:    "What is in scope?",
		},
		{
			name:       "validation handoff failure",
			outcome:    runAttemptOutcome{Status: runAttemptStatusFailed, Error: "PR base mismatch", PRURL: "https://github.com/acme/repo/pull/2"},
			wantStatus: runAttemptStatusFailed,
			wantIntent: "operational_failure",
			wantPR:     "https://github.com/acme/repo/pull/2",
			wantErr:    "PR base mismatch",
		},
		{
			name:       "review failed behavior blocker",
			outcome:    runAttemptOutcome{Status: runAttemptStatusReviewFailed, Review: &reviewResult{Status: "failed", Classification: "behavior_spec_blocker", Findings: "behavior mismatch"}, PRURL: "https://github.com/acme/repo/pull/3", Error: "review did not pass"},
			wantStatus: runAttemptStatusReviewFailed,
			wantIntent: "review_failed",
			wantPR:     "https://github.com/acme/repo/pull/3",
			wantReview: "failed",
			wantErr:    "review did not pass",
		},
		{
			name:       "missing evidence human handoff",
			outcome:    runAttemptOutcome{Status: runAttemptStatusSuccess, Review: &reviewResult{Status: "failed", Classification: reviewClassificationMissingEvidenceOnly, Findings: "missing behavior evidence"}, PRURL: "https://github.com/acme/repo/pull/4"},
			wantStatus: runAttemptStatusSuccess,
			wantIntent: "human_review",
			wantPR:     "https://github.com/acme/repo/pull/4",
			wantReview: "failed",
		},
		{
			name:       "success",
			outcome:    runAttemptOutcome{Status: runAttemptStatusSuccess, Review: &reviewResult{Status: "passed"}, PRURL: "https://github.com/acme/repo/pull/5", PiUsage: usage},
			wantStatus: runAttemptStatusSuccess,
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
			if !terminalRunStatus(record.Status) {
				t.Fatalf("expected %q to be terminal", record.Status)
			}
		})
	}
}
