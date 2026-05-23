package main

import "testing"

func TestDecideAttemptLifecycleCharacterizesCurrentOutcomes(t *testing.T) {
	tests := []struct {
		name             string
		input            attemptLifecycleInput
		wantStatus       string
		wantIntent       string
		wantNext         string
		wantPRRequired   bool
		wantResumeReview bool
		wantOperator     bool
		wantReviewStatus string
		wantReviewClass  string
	}{
		{
			name: "success with PR",
			input: attemptLifecycleInput{
				Phase:  attemptLifecyclePhaseSuccess,
				PRURL:  "https://github.com/acme/repo/pull/1",
				Review: &reviewResult{Status: "passed"},
			},
			wantStatus:       runAttemptStatusSuccess,
			wantIntent:       "handoff_ready",
			wantNext:         "await_approval_and_green_checks",
			wantPRRequired:   true,
			wantReviewStatus: "passed",
		},
		{
			name: "missing PR handoff failure",
			input: attemptLifecycleInput{
				Phase: attemptLifecyclePhaseHandoff,
				Error: "missing PR URL",
			},
			wantStatus:     runAttemptStatusFailed,
			wantIntent:     "operational_failure",
			wantNext:       "inspect_run_log_and_create_or_repair_pr",
			wantPRRequired: true,
			wantOperator:   true,
		},
		{
			name: "needs info",
			input: attemptLifecycleInput{
				Phase:              attemptLifecyclePhaseNeedsInfo,
				NeedsInfoQuestions: []string{"What is in scope?"},
			},
			wantStatus: runAttemptStatusNeedsInfo,
			wantIntent: "needs_info",
			wantNext:   "answer_needs_info_questions",
		},
		{
			name: "review not ready",
			input: attemptLifecycleInput{
				Phase:          attemptLifecyclePhaseReviewReadiness,
				PRURL:          "https://github.com/acme/repo/pull/2",
				ReviewNotReady: true,
			},
			wantStatus:       runAttemptStatusReviewNotReady,
			wantIntent:       "waiting_for_checks",
			wantNext:         "wait_for_github_checks_then_retry",
			wantPRRequired:   true,
			wantResumeReview: true,
		},
		{
			name: "review failed behavior blocker",
			input: attemptLifecycleInput{
				Phase:  attemptLifecyclePhaseReview,
				PRURL:  "https://github.com/acme/repo/pull/3",
				Review: &reviewResult{Status: "failed", Classification: reviewClassificationBehaviorSpecBlocker, Findings: "behavior mismatch"},
			},
			wantStatus:       runAttemptStatusReviewFailed,
			wantIntent:       "review_failed",
			wantNext:         "repair_review_findings_before_handoff",
			wantPRRequired:   true,
			wantOperator:     true,
			wantReviewStatus: "failed",
			wantReviewClass:  reviewClassificationBehaviorSpecBlocker,
		},
		{
			name: "missing evidence human handoff",
			input: attemptLifecycleInput{
				Phase:  attemptLifecyclePhaseReview,
				PRURL:  "https://github.com/acme/repo/pull/4",
				Review: &reviewResult{Status: "failed", Classification: reviewClassificationMissingEvidenceOnly, Findings: "missing behavior evidence"},
			},
			wantStatus:       runAttemptStatusSuccess,
			wantIntent:       "human_review",
			wantNext:         "await_human_review_for_behavior_contract_evidence",
			wantPRRequired:   true,
			wantOperator:     true,
			wantReviewStatus: "failed",
			wantReviewClass:  reviewClassificationMissingEvidenceOnly,
		},
		{
			name: "timeout",
			input: attemptLifecycleInput{
				Phase:            attemptLifecyclePhaseImplementation,
				RuntimeOutcome:   runAttemptStatusTimeout,
				RuntimeErrorKind: runAttemptStatusTimeout,
				Error:            "command timed out",
			},
			wantStatus:   runAttemptStatusTimeout,
			wantIntent:   runAttemptStatusTimeout,
			wantNext:     "split_or_reduce_issue_scope_then_retry",
			wantOperator: true,
		},
		{
			name: "budget exceeded",
			input: attemptLifecycleInput{
				Phase:          attemptLifecyclePhaseReview,
				BudgetExceeded: "token budget exceeded",
			},
			wantStatus:     runAttemptStatusBudgetExceeded,
			wantIntent:     runAttemptStatusBudgetExceeded,
			wantNext:       "split_or_reduce_issue_scope_then_retry",
			wantPRRequired: true,
			wantOperator:   true,
		},
		{
			name: "scope guard failure",
			input: attemptLifecycleInput{
				Phase:       attemptLifecyclePhaseScopeGuard,
				PRURL:       "https://github.com/acme/repo/pull/5",
				ScopeResult: scopeGuardResult{Checked: true, Violations: []string{"scope guard violation: x.go is outside Allowed paths"}},
			},
			wantStatus:     runAttemptStatusReviewFailed,
			wantIntent:     "review_failed",
			wantNext:       "repair_scope_guard_findings_before_handoff",
			wantPRRequired: true,
			wantOperator:   true,
		},
		{
			name: "PR handoff failure",
			input: attemptLifecycleInput{
				Phase: attemptLifecyclePhaseHandoff,
				PRURL: "https://github.com/acme/repo/pull/6",
				Error: "PR base mismatch",
			},
			wantStatus:     runAttemptStatusFailed,
			wantIntent:     "operational_failure",
			wantNext:       "inspect_run_log_and_create_or_repair_pr",
			wantPRRequired: true,
			wantOperator:   true,
		},
		{
			name: "preflight failure",
			input: attemptLifecycleInput{
				Phase:            attemptLifecyclePhasePreflight,
				RuntimeErrorKind: "configuration",
				Error:            "provider pi_cli preflight failed",
			},
			wantStatus:   runAttemptStatusFailed,
			wantIntent:   "operational_failure",
			wantNext:     "fix_runtime_configuration_before_retry",
			wantOperator: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decideAttemptLifecycle(tt.input)
			if got.Status != tt.wantStatus || got.TerminalOutcomeIntent != tt.wantIntent || got.NextAction != tt.wantNext {
				t.Fatalf("decision status/intent/next = %q/%q/%q, want %q/%q/%q", got.Status, got.TerminalOutcomeIntent, got.NextAction, tt.wantStatus, tt.wantIntent, tt.wantNext)
			}
			if got.PRRequired != tt.wantPRRequired || got.CanResumeReview != tt.wantResumeReview || got.OperatorAttentionRequired != tt.wantOperator {
				t.Fatalf("decision flags = pr:%t resume:%t operator:%t, want pr:%t resume:%t operator:%t", got.PRRequired, got.CanResumeReview, got.OperatorAttentionRequired, tt.wantPRRequired, tt.wantResumeReview, tt.wantOperator)
			}
			if got.ReviewStatus != tt.wantReviewStatus || got.ReviewClassification != tt.wantReviewClass {
				t.Fatalf("decision review = %q/%q, want %q/%q", got.ReviewStatus, got.ReviewClassification, tt.wantReviewStatus, tt.wantReviewClass)
			}
		})
	}
}
