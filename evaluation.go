package main

import (
	"strings"

	artifactio "github.com/weskor/pi-symphony/internal/artifacts"
)

const evaluationArtifactName = artifactio.EvaluationName

type evaluationArtifact = artifactio.EvaluationArtifact

func writeEvaluationArtifact(workspace string, record runRecord) (string, evaluationArtifact) {
	path, evaluation, _ := writeEvaluationArtifactResult(workspace, record)
	return path, evaluation
}

func writeEvaluationArtifactResult(workspace string, record runRecord) (string, evaluationArtifact, error) {
	path, evaluation, err := artifactManager().WriteEvaluation(workspace, record)
	if err != nil {
		log("failed to write evaluation artifact: %v", err)
		return "", evaluation, err
	}
	log("wrote evaluation artifact: %s", path)
	return path, evaluation, nil
}

func evaluationForRun(workspace string, record runRecord) evaluationArtifact {
	mergeGate := evaluateRunRecordMergeGate(record)
	evaluation := evaluationArtifact{
		IssueIdentifier:          record.IssueIdentifier,
		IssueID:                  record.IssueID,
		PRURL:                    record.PRURL,
		FinalStatus:              record.Status,
		DurationMS:               record.DurationMS,
		ChecksStatus:             checksStatusForRun(record),
		ReviewStatus:             record.ReviewStatus,
		ReviewClassification:     record.ReviewClassification,
		FeedbackRetryCount:       feedbackRetryCount(workspace),
		NeedsInfoUsed:            strings.HasPrefix(record.Status, "needs_info"),
		MergeBlockReason:         mergeGate.Reason(),
		MergeBlockerCodes:        mergeGate.Codes(),
		WorkspaceCleanupEligible: terminalRunStatus(record.Status),
	}
	if record.PiUsage != nil {
		evaluation.ImplementationTotalTokens = record.PiUsage.TotalTokens
		evaluation.ImplementationTotalCost = record.PiUsage.TotalCost()
		evaluation.TotalTokens += record.PiUsage.TotalTokens
		evaluation.TotalCost += record.PiUsage.TotalCost()
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
	classification := classifyRunRecord(workspace, record)
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

func hasString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func checksStatusForRun(record runRecord) string {
	if record.PRURL == "" {
		return "not_applicable"
	}
	// Post-run handoff happens before GitHub/Vercel checks are expected to settle.
	// Merge-time blocking remains recorded separately when available.
	return "unknown_post_run"
}

func feedbackRetryCount(workspace string) int {
	feedback, err := readPRFeedback(workspace)
	if err != nil || strings.TrimSpace(feedback) == "" {
		return 0
	}
	return 1
}

func mergeBlockReason(record runRecord) string {
	return evaluateRunRecordMergeGate(record).Reason()
}
