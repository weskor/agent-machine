package attemptlifecycle

import (
	"strings"

	"github.com/weskor/agent-machine/internal/domain"
	"github.com/weskor/agent-machine/internal/reviewpolicy"
	"github.com/weskor/agent-machine/internal/runclassification"
	"github.com/weskor/agent-machine/internal/scopeguard"
)

type Phase string

const (
	PhasePreflight       Phase = "preflight"
	PhaseWorkspace       Phase = "workspace"
	PhaseImplementation  Phase = "implementation"
	PhaseNeedsInfo       Phase = "needs_info"
	PhaseValidation      Phase = "validation"
	PhaseScopeGuard      Phase = "scope_guard"
	PhaseHandoff         Phase = "handoff"
	PhaseReviewReadiness Phase = "review_readiness"
	PhaseReview          Phase = "review"
	PhaseSuccess         Phase = "success"
)

const (
	StatusSuccess        = "success"
	StatusFailed         = "failed"
	StatusReviewFailed   = "review_failed"
	StatusReviewNotReady = "review_not_ready"
	StatusNeedsInfo      = "needs_info"
	StatusNeedsInfoFail  = "needs_info_failed"
	StatusTimeout        = "timeout"
	StatusBudgetExceeded = "budget_exceeded"
)

// Input is the runner-owned typed fact packet for deciding a
// run attempt status. It deliberately has no Linear, GitHub, workspace, SQLite,
// or shell dependencies so lifecycle policy can be table-tested.
type Input struct {
	Phase              Phase
	PRURL              string
	Review             *domain.ReviewResult
	ScopeResult        scopeguard.Result
	ScopeError         string
	RuntimeOutcome     string
	RuntimeErrorKind   string
	Error              string
	BudgetExceeded     string
	NeedsInfoQuestions []string
	ReviewNotReady     bool
}

type Decision struct {
	Status                    string
	TerminalOutcomeIntent     string
	NextAction                string
	PRRequired                bool
	CanResumeReview           bool
	OperatorAttentionRequired bool
	ReviewStatus              string
	ReviewClassification      string
}

func Decide(input Input) Decision {
	record := domain.RunRecord{
		Status: lifecycleStatus(input),
		PRURL:  strings.TrimSpace(input.PRURL),
		Error:  lifecycleError(input),
	}
	if input.Review != nil {
		record.ReviewStatus = input.Review.Status
		record.ReviewClassification = input.Review.Classification
	}
	classification := runclassification.Classify(runclassification.Input{
		Record:        record,
		NeedsInfoUsed: record.Status == StatusNeedsInfo || record.Status == StatusNeedsInfoFail || len(input.NeedsInfoQuestions) > 0,
	})
	decision := Decision{
		Status:                    record.Status,
		TerminalOutcomeIntent:     classification.Outcome,
		NextAction:                classification.NextAction,
		PRRequired:                lifecyclePRRequired(input, record.Status),
		CanResumeReview:           record.Status == StatusReviewNotReady,
		OperatorAttentionRequired: classification.OperatorAttentionRequired,
		ReviewStatus:              record.ReviewStatus,
		ReviewClassification:      record.ReviewClassification,
	}
	if input.Phase == PhasePreflight {
		decision.NextAction = "fix_runtime_configuration_before_retry"
	}
	if record.Status == StatusReviewFailed && input.ScopeResult.Blocks() {
		decision.NextAction = "repair_scope_guard_findings_before_handoff"
		decision.ReviewStatus = "failed"
		decision.ReviewClassification = reviewpolicy.BehaviorSpecBlocker
	}
	if input.Phase == PhaseHandoff && record.Status == StatusFailed {
		decision.NextAction = "inspect_run_log_and_create_or_repair_pr"
	}
	return decision
}

func lifecycleStatus(input Input) string {
	if input.BudgetExceeded != "" || input.RuntimeOutcome == StatusBudgetExceeded {
		return StatusBudgetExceeded
	}
	if input.RuntimeOutcome == StatusTimeout || input.RuntimeErrorKind == StatusTimeout {
		return StatusTimeout
	}
	if input.ReviewNotReady || input.Phase == PhaseReviewReadiness {
		return StatusReviewNotReady
	}
	if input.RuntimeOutcome == StatusNeedsInfoFail {
		return StatusNeedsInfoFail
	}
	if len(input.NeedsInfoQuestions) > 0 || input.Phase == PhaseNeedsInfo {
		return StatusNeedsInfo
	}
	if input.ScopeResult.Blocks() {
		return StatusReviewFailed
	}
	if input.ScopeError != "" {
		return StatusFailed
	}
	if input.Review != nil && input.Review.Status != "" && input.Review.Status != "passed" {
		if ReviewFailureRoutesToHumanHandoff(input.Review, input.PRURL) {
			return StatusSuccess
		}
		return StatusReviewFailed
	}
	if input.Phase == PhaseSuccess && strings.TrimSpace(input.PRURL) != "" {
		return StatusSuccess
	}
	if input.Phase == PhaseHandoff || input.Phase == PhasePreflight || input.Phase == PhaseValidation || input.Phase == PhaseImplementation || input.RuntimeOutcome == StatusFailed {
		return StatusFailed
	}
	if strings.TrimSpace(input.PRURL) == "" {
		return StatusFailed
	}
	return StatusSuccess
}

func lifecycleError(input Input) string {
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
	if input.Review != nil && input.Review.Status != "" && input.Review.Status != "passed" && !ReviewFailureRoutesToHumanHandoff(input.Review, input.PRURL) {
		return "review did not pass"
	}
	if input.Phase == PhaseHandoff && strings.TrimSpace(input.PRURL) == "" {
		return "missing PR URL"
	}
	return ""
}

func lifecyclePRRequired(input Input, status string) bool {
	if status == StatusNeedsInfo || status == StatusNeedsInfoFail {
		return false
	}
	return input.Phase == PhaseHandoff ||
		input.Phase == PhaseReviewReadiness ||
		input.Phase == PhaseReview ||
		input.Phase == PhaseSuccess ||
		strings.TrimSpace(input.PRURL) != ""
}

func ReviewFailureRoutesToHumanHandoff(review *domain.ReviewResult, prURL string) bool {
	return review != nil && review.Status == "failed" && review.Classification == reviewpolicy.MissingEvidenceOnly && strings.TrimSpace(prURL) != ""
}
