package attemptlifecycle

import (
	"testing"

	"github.com/weskor/agent-machine/internal/domain"
	"github.com/weskor/agent-machine/internal/reviewpolicy"
	"github.com/weskor/agent-machine/internal/scopeguard"
)

func TestDecideAttemptLifecycleCharacterizesCurrentOutcomes(t *testing.T) {
	tests := []struct {
		name             string
		input            Input
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
			input: Input{
				Phase:  PhaseSuccess,
				PRURL:  "https://github.com/acme/repo/pull/1",
				Review: &domain.ReviewResult{Status: "passed"},
			},
			wantStatus:       StatusSuccess,
			wantIntent:       "handoff_ready",
			wantNext:         "await_approval_and_green_checks",
			wantPRRequired:   true,
			wantReviewStatus: "passed",
		},
		{
			name: "missing PR handoff failure",
			input: Input{
				Phase: PhaseHandoff,
				Error: "missing PR URL",
			},
			wantStatus:     StatusFailed,
			wantIntent:     "operational_failure",
			wantNext:       "inspect_run_log_and_create_or_repair_pr",
			wantPRRequired: true,
			wantOperator:   true,
		},
		{
			name: "needs info",
			input: Input{
				Phase:              PhaseNeedsInfo,
				NeedsInfoQuestions: []string{"What is in scope?"},
			},
			wantStatus: StatusNeedsInfo,
			wantIntent: "needs_info",
			wantNext:   "answer_needs_info_questions",
		},
		{
			name: "needs info transition failure",
			input: Input{
				Phase:              PhaseNeedsInfo,
				RuntimeOutcome:     StatusNeedsInfoFail,
				Error:              "Linear transition failed",
				NeedsInfoQuestions: []string{"What is in scope?"},
			},
			wantStatus:   StatusNeedsInfoFail,
			wantIntent:   "needs_info",
			wantNext:     "answer_needs_info_questions",
			wantOperator: true,
		},
		{
			name: "review not ready",
			input: Input{
				Phase:          PhaseReviewReadiness,
				PRURL:          "https://github.com/acme/repo/pull/2",
				ReviewNotReady: true,
			},
			wantStatus:       StatusReviewNotReady,
			wantIntent:       "waiting_for_checks",
			wantNext:         "wait_for_github_checks_then_retry",
			wantPRRequired:   true,
			wantResumeReview: true,
		},
		{
			name: "review failed behavior blocker",
			input: Input{
				Phase:  PhaseReview,
				PRURL:  "https://github.com/acme/repo/pull/3",
				Review: &domain.ReviewResult{Status: "failed", Classification: reviewpolicy.BehaviorSpecBlocker, Findings: "behavior mismatch"},
			},
			wantStatus:       StatusReviewFailed,
			wantIntent:       "review_failed",
			wantNext:         "repair_review_findings_before_handoff",
			wantPRRequired:   true,
			wantOperator:     true,
			wantReviewStatus: "failed",
			wantReviewClass:  reviewpolicy.BehaviorSpecBlocker,
		},
		{
			name: "missing evidence human handoff",
			input: Input{
				Phase:  PhaseReview,
				PRURL:  "https://github.com/acme/repo/pull/4",
				Review: &domain.ReviewResult{Status: "failed", Classification: reviewpolicy.MissingEvidenceOnly, Findings: "missing behavior evidence"},
			},
			wantStatus:       StatusSuccess,
			wantIntent:       "human_review",
			wantNext:         "await_human_review_for_behavior_contract_evidence",
			wantPRRequired:   true,
			wantOperator:     true,
			wantReviewStatus: "failed",
			wantReviewClass:  reviewpolicy.MissingEvidenceOnly,
		},
		{
			name: "timeout",
			input: Input{
				Phase:            PhaseImplementation,
				RuntimeOutcome:   StatusTimeout,
				RuntimeErrorKind: StatusTimeout,
				Error:            "command timed out",
			},
			wantStatus:   StatusTimeout,
			wantIntent:   StatusTimeout,
			wantNext:     "split_or_reduce_issue_scope_then_retry",
			wantOperator: true,
		},
		{
			name: "implementation failure",
			input: Input{
				Phase:          PhaseImplementation,
				RuntimeOutcome: StatusFailed,
				Error:          "agent command failed",
			},
			wantStatus:   StatusFailed,
			wantIntent:   "operational_failure",
			wantNext:     "inspect_run_log_and_create_or_repair_pr",
			wantOperator: true,
		},
		{
			name: "budget exceeded",
			input: Input{
				Phase:          PhaseReview,
				BudgetExceeded: "token budget exceeded",
			},
			wantStatus:     StatusBudgetExceeded,
			wantIntent:     StatusBudgetExceeded,
			wantNext:       "split_or_reduce_issue_scope_then_retry",
			wantPRRequired: true,
			wantOperator:   true,
		},
		{
			name: "scope guard failure",
			input: Input{
				Phase:       PhaseScopeGuard,
				PRURL:       "https://github.com/acme/repo/pull/5",
				ScopeResult: scopeguard.Result{Checked: true, Violations: []string{"scope guard violation: x.go is outside Allowed paths"}},
			},
			wantStatus:       StatusReviewFailed,
			wantIntent:       "review_failed",
			wantNext:         "repair_scope_guard_findings_before_handoff",
			wantPRRequired:   true,
			wantOperator:     true,
			wantReviewStatus: "failed",
			wantReviewClass:  reviewpolicy.BehaviorSpecBlocker,
		},
		{
			name: "PR handoff failure",
			input: Input{
				Phase: PhaseHandoff,
				PRURL: "https://github.com/acme/repo/pull/6",
				Error: "PR base mismatch",
			},
			wantStatus:     StatusFailed,
			wantIntent:     "operational_failure",
			wantNext:       "inspect_run_log_and_create_or_repair_pr",
			wantPRRequired: true,
			wantOperator:   true,
		},
		{
			name: "preflight failure",
			input: Input{
				Phase:            PhasePreflight,
				RuntimeErrorKind: "configuration",
				Error:            "provider pi_cli preflight failed",
			},
			wantStatus:   StatusFailed,
			wantIntent:   "operational_failure",
			wantNext:     "fix_runtime_configuration_before_retry",
			wantOperator: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Decide(tt.input)
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
