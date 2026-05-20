package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

const evaluationArtifactName = ".pi-symphony-evaluation.json"

type evaluationArtifact struct {
	IssueIdentifier              string   `json:"issue_identifier"`
	IssueID                      string   `json:"issue_id,omitempty"`
	PRURL                        string   `json:"pr_url,omitempty"`
	FinalStatus                  string   `json:"final_status"`
	DurationMS                   int64    `json:"duration_ms"`
	ImplementationTotalTokens    float64  `json:"implementation_total_tokens,omitempty"`
	ImplementationTotalCost      float64  `json:"implementation_total_cost,omitempty"`
	ReviewTotalTokens            float64  `json:"review_total_tokens,omitempty"`
	ReviewTotalCost              float64  `json:"review_total_cost,omitempty"`
	TotalTokens                  float64  `json:"total_tokens,omitempty"`
	TotalCost                    float64  `json:"total_cost,omitempty"`
	ChecksStatus                 string   `json:"checks_status"`
	ReviewStatus                 string   `json:"review_status,omitempty"`
	ReviewClassification         string   `json:"review_classification,omitempty"`
	ReviewPassed                 *bool    `json:"review_passed,omitempty"`
	Outcome                      string   `json:"outcome"`
	MergeEligible                bool     `json:"merge_eligible"`
	BlockedBy                    []string `json:"blocked_by,omitempty"`
	RootCause                    string   `json:"root_cause,omitempty"`
	NextAction                   string   `json:"next_action,omitempty"`
	ShouldRetry                  bool     `json:"should_retry"`
	OperatorAttentionRequired    bool     `json:"operator_attention_required"`
	FeedbackRetryCount           int      `json:"feedback_retry_count,omitempty"`
	NeedsInfoUsed                bool     `json:"needs_info_used"`
	MergeBlockReason             string   `json:"merge_block_reason,omitempty"`
	MergeBlockerCodes            []string `json:"merge_blocker_codes,omitempty"`
	WorkspaceCleanupEligible     bool     `json:"workspace_cleanup_eligible"`
	FrictionSignals              []string `json:"friction_signals,omitempty"`
	CandidateHarnessImprovements []string `json:"candidate_harness_improvements,omitempty"`
	BehaviorContractEvidence     []string `json:"behavior_contract_evidence,omitempty"`
	TicketContractEvidence       []string `json:"ticket_contract_evidence,omitempty"`
}

func writeEvaluationArtifact(workspace string, record runRecord) (string, evaluationArtifact) {
	evaluation := evaluationForRun(workspace, record)
	data, err := json.MarshalIndent(evaluation, "", "  ")
	if err != nil {
		log("failed to encode evaluation artifact: %v", err)
		return "", evaluation
	}
	path := filepath.Join(workspace, evaluationArtifactName)
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		log("failed to write evaluation artifact: %v", err)
		return "", evaluation
	}
	log("wrote evaluation artifact: %s", path)
	return path, evaluation
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
	classification := classifyRun(runClassificationInput{Record: record, FeedbackRetryCount: evaluation.FeedbackRetryCount, NeedsInfoUsed: evaluation.NeedsInfoUsed, MergeBlockReason: evaluation.MergeBlockReason, TotalTokens: evaluation.TotalTokens})
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
