package main

import "strings"

type attemptLifecyclePhase string

const (
	attemptLifecyclePhasePreflight       attemptLifecyclePhase = "preflight"
	attemptLifecyclePhaseWorkspace       attemptLifecyclePhase = "workspace"
	attemptLifecyclePhaseImplementation  attemptLifecyclePhase = "implementation"
	attemptLifecyclePhaseNeedsInfo       attemptLifecyclePhase = "needs_info"
	attemptLifecyclePhaseValidation      attemptLifecyclePhase = "validation"
	attemptLifecyclePhaseScopeGuard      attemptLifecyclePhase = "scope_guard"
	attemptLifecyclePhaseHandoff         attemptLifecyclePhase = "handoff"
	attemptLifecyclePhaseReviewReadiness attemptLifecyclePhase = "review_readiness"
	attemptLifecyclePhaseReview          attemptLifecyclePhase = "review"
	attemptLifecyclePhaseSuccess         attemptLifecyclePhase = "success"
)

// attemptLifecycleInput is the runner-owned typed fact packet for deciding a
// run attempt status. It deliberately has no Linear, GitHub, workspace, SQLite,
// or shell dependencies so lifecycle policy can be table-tested.
type attemptLifecycleInput struct {
	Phase              attemptLifecyclePhase
	PRURL              string
	Review             *reviewResult
	ScopeResult        scopeGuardResult
	ScopeError         string
	RuntimeOutcome     string
	RuntimeErrorKind   string
	Error              string
	BudgetExceeded     string
	NeedsInfoQuestions []string
	ReviewNotReady     bool
}

type attemptLifecycleDecision struct {
	Status                    string
	TerminalOutcomeIntent     string
	NextAction                string
	PRRequired                bool
	CanResumeReview           bool
	OperatorAttentionRequired bool
	ReviewStatus              string
	ReviewClassification      string
}

func decideAttemptLifecycle(input attemptLifecycleInput) attemptLifecycleDecision {
	record := runRecord{
		Status: lifecycleStatus(input),
		PRURL:  strings.TrimSpace(input.PRURL),
		Error:  lifecycleError(input),
	}
	if input.Review != nil {
		record.ReviewStatus = input.Review.Status
		record.ReviewClassification = input.Review.Classification
	}
	classification := classifyRun(runClassificationInput{
		Record:        record,
		NeedsInfoUsed: record.Status == runAttemptStatusNeedsInfo || record.Status == runAttemptStatusNeedsInfoFail || len(input.NeedsInfoQuestions) > 0,
	})
	decision := attemptLifecycleDecision{
		Status:                    record.Status,
		TerminalOutcomeIntent:     classification.Outcome,
		NextAction:                classification.NextAction,
		PRRequired:                lifecyclePRRequired(input, record.Status),
		CanResumeReview:           record.Status == runAttemptStatusReviewNotReady,
		OperatorAttentionRequired: classification.OperatorAttentionRequired,
		ReviewStatus:              record.ReviewStatus,
		ReviewClassification:      record.ReviewClassification,
	}
	if input.Phase == attemptLifecyclePhasePreflight {
		decision.NextAction = "fix_runtime_configuration_before_retry"
	}
	if record.Status == runAttemptStatusReviewFailed && input.ScopeResult.Blocks() {
		decision.NextAction = "repair_scope_guard_findings_before_handoff"
	}
	if input.Phase == attemptLifecyclePhaseHandoff && record.Status == runAttemptStatusFailed {
		decision.NextAction = "inspect_run_log_and_create_or_repair_pr"
	}
	return decision
}

func lifecycleStatus(input attemptLifecycleInput) string {
	if input.BudgetExceeded != "" || input.RuntimeOutcome == runAttemptStatusBudgetExceeded {
		return runAttemptStatusBudgetExceeded
	}
	if input.RuntimeOutcome == runAttemptStatusTimeout || input.RuntimeErrorKind == runAttemptStatusTimeout {
		return runAttemptStatusTimeout
	}
	if input.ReviewNotReady || input.Phase == attemptLifecyclePhaseReviewReadiness {
		return runAttemptStatusReviewNotReady
	}
	if input.RuntimeOutcome == runAttemptStatusNeedsInfoFail {
		return runAttemptStatusNeedsInfoFail
	}
	if len(input.NeedsInfoQuestions) > 0 || input.Phase == attemptLifecyclePhaseNeedsInfo {
		return runAttemptStatusNeedsInfo
	}
	if input.ScopeResult.Blocks() {
		return runAttemptStatusReviewFailed
	}
	if input.ScopeError != "" {
		return runAttemptStatusFailed
	}
	if input.Review != nil && input.Review.Status != "" && input.Review.Status != "passed" {
		if reviewFailureRoutesToHumanHandoff(input.Review, input.PRURL) {
			return runAttemptStatusSuccess
		}
		return runAttemptStatusReviewFailed
	}
	if input.Phase == attemptLifecyclePhaseSuccess && strings.TrimSpace(input.PRURL) != "" {
		return runAttemptStatusSuccess
	}
	if input.Phase == attemptLifecyclePhaseHandoff || input.Phase == attemptLifecyclePhasePreflight || input.Phase == attemptLifecyclePhaseValidation || input.Phase == attemptLifecyclePhaseImplementation || input.RuntimeOutcome == runAttemptStatusFailed {
		return runAttemptStatusFailed
	}
	if strings.TrimSpace(input.PRURL) == "" {
		return runAttemptStatusFailed
	}
	return runAttemptStatusSuccess
}

func lifecycleError(input attemptLifecycleInput) string {
	if input.Error != "" {
		return input.Error
	}
	if input.BudgetExceeded != "" {
		return input.BudgetExceeded
	}
	if input.ScopeError != "" {
		return input.ScopeError
	}
	if len(input.NeedsInfoQuestions) > 0 {
		return strings.Join(input.NeedsInfoQuestions, "\n")
	}
	if input.ReviewNotReady {
		return "review not ready"
	}
	if input.ScopeResult.Blocks() {
		return "scope guard failed"
	}
	if input.Review != nil && input.Review.Status != "" && input.Review.Status != "passed" && !reviewFailureRoutesToHumanHandoff(input.Review, input.PRURL) {
		return "review did not pass"
	}
	if input.Phase == attemptLifecyclePhaseHandoff && strings.TrimSpace(input.PRURL) == "" {
		return "missing PR URL"
	}
	return ""
}

func lifecyclePRRequired(input attemptLifecycleInput, status string) bool {
	if status == runAttemptStatusNeedsInfo || status == runAttemptStatusNeedsInfoFail {
		return false
	}
	return input.Phase == attemptLifecyclePhaseHandoff ||
		input.Phase == attemptLifecyclePhaseReviewReadiness ||
		input.Phase == attemptLifecyclePhaseReview ||
		input.Phase == attemptLifecyclePhaseSuccess ||
		strings.TrimSpace(input.PRURL) != ""
}
