package evaluation

import (
	"strings"

	artifactio "github.com/weskor/agent-machine/internal/artifacts"
	"github.com/weskor/agent-machine/internal/domain"
	"github.com/weskor/agent-machine/internal/runclassification"
)

type MergeGate struct {
	ReasonText string
	CodeValues []string
}

type Builder struct {
	MergeGate          func(domain.RunRecord) MergeGate
	FeedbackRetryCount func(string) int
	TerminalStatus     func(string) bool
	RuntimeUsage       func(domain.RunRecord) *domain.Usage
}

func (b Builder) ForRun(workspace string, record domain.RunRecord) artifactio.EvaluationArtifact {
	mergeGate := b.mergeGate(record)
	feedbackRetryCount := b.feedbackRetryCount(workspace)
	evaluation := artifactio.EvaluationArtifact{
		IssueIdentifier:          record.IssueIdentifier,
		IssueID:                  record.IssueID,
		PRURL:                    record.PRURL,
		FinalStatus:              record.Status,
		DurationMS:               record.DurationMS,
		ChecksStatus:             checksStatusForRun(record),
		ReviewStatus:             record.ReviewStatus,
		ReviewClassification:     record.ReviewClassification,
		FeedbackRetryCount:       feedbackRetryCount,
		NeedsInfoUsed:            strings.HasPrefix(record.Status, "needs_info"),
		MergeBlockReason:         mergeGate.ReasonText,
		MergeBlockerCodes:        mergeGate.CodeValues,
		WorkspaceCleanupEligible: b.terminalStatus(record.Status),
	}
	if usage := b.runtimeUsage(record); usage != nil {
		evaluation.ImplementationTotalTokens = usage.TotalTokens
		evaluation.ImplementationTotalCost = usage.TotalCost()
		evaluation.TotalTokens += usage.TotalTokens
		evaluation.TotalCost += usage.TotalCost()
	}
	if record.ReviewUsage != nil {
		evaluation.ReviewTotalTokens = record.ReviewUsage.TotalTokens
		evaluation.ReviewTotalCost = record.ReviewUsage.TotalCost()
		evaluation.TotalTokens += record.ReviewUsage.TotalTokens
		evaluation.TotalCost += record.ReviewUsage.TotalCost()
	}
	if record.ReviewStatus != "" {
		passed := record.ReviewStatus == "passed"
		evaluation.ReviewPassed = &passed
	}
	classification := b.Classify(workspace, record)
	evaluation.BehaviorContractEvidence = classification.BehaviorContractEvidence
	evaluation.TicketContractEvidence = classification.TicketContractEvidence
	evaluation.FrictionSignals = classification.FrictionSignals
	evaluation.CandidateHarnessImprovements = classification.CandidateHarnessImprovements
	evaluation.Outcome = classification.Outcome
	evaluation.MergeEligible = classification.MergeEligible
	evaluation.BlockedBy = classification.BlockedBy
	evaluation.RootCause = classification.RootCause
	evaluation.NextAction = classification.NextAction
	evaluation.ShouldRetry = classification.ShouldRetry
	evaluation.OperatorAttentionRequired = classification.OperatorAttentionRequired
	return evaluation
}

func (b Builder) Classify(workspace string, record domain.RunRecord) runclassification.Classification {
	return runclassification.Classify(runclassification.Input{
		Record:             record,
		FeedbackRetryCount: b.feedbackRetryCount(workspace),
		NeedsInfoUsed:      strings.HasPrefix(record.Status, "needs_info"),
		MergeBlockReason:   b.mergeGate(record).ReasonText,
		TotalTokens:        b.totalTokens(record),
	})
}

func (b Builder) totalTokens(record domain.RunRecord) float64 {
	var total float64
	if usage := b.runtimeUsage(record); usage != nil {
		total += usage.TotalTokens
	}
	if record.ReviewUsage != nil {
		total += record.ReviewUsage.TotalTokens
	}
	return total
}

func (b Builder) mergeGate(record domain.RunRecord) MergeGate {
	if b.MergeGate == nil {
		return MergeGate{}
	}
	return b.MergeGate(record)
}

func (b Builder) feedbackRetryCount(workspace string) int {
	if b.FeedbackRetryCount == nil {
		return 0
	}
	return b.FeedbackRetryCount(workspace)
}

func (b Builder) terminalStatus(status string) bool {
	return b.TerminalStatus != nil && b.TerminalStatus(status)
}

func (b Builder) runtimeUsage(record domain.RunRecord) *domain.Usage {
	if b.RuntimeUsage == nil {
		return nil
	}
	return b.RuntimeUsage(record)
}

func checksStatusForRun(record domain.RunRecord) string {
	if record.PRURL == "" {
		return "not_applicable"
	}
	// Post-run handoff happens before GitHub/Vercel checks are expected to settle.
	// Merge-time blocking remains recorded separately when available.
	return "unknown_post_run"
}
