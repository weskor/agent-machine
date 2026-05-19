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
	WorkspaceCleanupEligible     bool     `json:"workspace_cleanup_eligible"`
	FrictionSignals              []string `json:"friction_signals,omitempty"`
	CandidateHarnessImprovements []string `json:"candidate_harness_improvements,omitempty"`
	BehaviorContractEvidence     []string `json:"behavior_contract_evidence,omitempty"`
	TicketContractEvidence       []string `json:"ticket_contract_evidence,omitempty"`
}

func writeEvaluationArtifact(workspace string, record runRecord) {
	evaluation := evaluationForRun(workspace, record)
	data, err := json.MarshalIndent(evaluation, "", "  ")
	if err != nil {
		log("failed to encode evaluation artifact: %v", err)
		return
	}
	path := filepath.Join(workspace, evaluationArtifactName)
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		log("failed to write evaluation artifact: %v", err)
		return
	}
	log("wrote evaluation artifact: %s", path)
}

func evaluationForRun(workspace string, record runRecord) evaluationArtifact {
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
		MergeBlockReason:         mergeBlockReason(record),
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
	evaluation.BehaviorContractEvidence = behaviorContractEvidenceForRun(record)
	evaluation.TicketContractEvidence = ticketContractEvidenceForRun(record)
	evaluation.FrictionSignals = frictionSignals(record, evaluation)
	evaluation.CandidateHarnessImprovements = harnessImprovements(evaluation.FrictionSignals)
	evaluation.Outcome = outcomeForRun(record, evaluation)
	evaluation.MergeEligible = record.Status == "success" && record.ReviewStatus == "passed" && record.PRURL != ""
	evaluation.BlockedBy = blockedByForRun(record, evaluation)
	evaluation.RootCause = rootCauseForRun(record, evaluation)
	evaluation.NextAction = nextActionForRun(record, evaluation)
	evaluation.ShouldRetry = shouldRetryRun(record, evaluation)
	evaluation.OperatorAttentionRequired = operatorAttentionRequired(record, evaluation)
	return evaluation
}

func outcomeForRun(record runRecord, evaluation evaluationArtifact) string {
	if record.Status == "success" && record.ReviewStatus == "passed" {
		return "handoff_ready"
	}
	if evaluation.NeedsInfoUsed {
		return "needs_info"
	}
	if record.ReviewStatus == "failed" && record.ReviewClassification == reviewClassificationMissingEvidenceOnly {
		return "human_review"
	}
	if record.Status == "review_failed" || record.ReviewStatus == "failed" {
		return "review_failed"
	}
	if record.Status == "timeout" || record.Status == "budget_exceeded" {
		return record.Status
	}
	if strings.TrimSpace(record.Error) != "" || strings.HasSuffix(record.Status, "_failed") || record.Status == "failed" {
		return "operational_failure"
	}
	return record.Status
}

func blockedByForRun(record runRecord, evaluation evaluationArtifact) []string {
	var blockers []string
	if record.PRURL == "" && !evaluation.NeedsInfoUsed {
		blockers = append(blockers, "missing_pr_url")
	}
	if record.Status != "success" {
		blockers = append(blockers, record.Status)
	}
	if record.ReviewStatus != "" && record.ReviewStatus != "passed" {
		blockers = append(blockers, "review_"+record.ReviewStatus)
	}
	if evaluation.MergeBlockReason != "" {
		blockers = append(blockers, "merge_blocked")
	}
	return uniqueStrings(blockers)
}

func rootCauseForRun(record runRecord, evaluation evaluationArtifact) string {
	if hasString(evaluation.FrictionSignals, "out_of_scope_diff_findings") {
		return "out_of_scope_diff"
	}
	if record.Status == "review_failed" || record.ReviewStatus == "failed" {
		if record.ReviewClassification == reviewClassificationMissingEvidenceOnly {
			return "missing_behavior_contract_evidence"
		}
		return "pre_handoff_review_failed"
	}
	if record.Status == "timeout" || record.Status == "budget_exceeded" {
		return record.Status
	}
	if hasString(evaluation.FrictionSignals, "operational_failure") {
		return "runner_operational_failure"
	}
	if evaluation.MergeBlockReason != "" {
		return "merge_gate_blocked"
	}
	return ""
}

func nextActionForRun(record runRecord, evaluation evaluationArtifact) string {
	if evaluation.MergeEligible {
		return "await_approval_and_green_checks"
	}
	if evaluation.NeedsInfoUsed {
		return "answer_needs_info_questions"
	}
	if record.Status == "review_failed" || record.ReviewStatus == "failed" {
		if record.ReviewClassification == reviewClassificationMissingEvidenceOnly {
			return "await_human_review_for_behavior_contract_evidence"
		}
		return "repair_review_findings_before_handoff"
	}
	if record.Status == "timeout" || record.Status == "budget_exceeded" {
		return "split_or_reduce_issue_scope_then_retry"
	}
	if record.PRURL == "" {
		return "inspect_run_log_and_create_or_repair_pr"
	}
	if evaluation.MergeBlockReason != "" {
		return "resolve_merge_gate_blocker"
	}
	return "inspect_run_artifact"
}

func shouldRetryRun(record runRecord, evaluation evaluationArtifact) bool {
	if evaluation.NeedsInfoUsed || evaluation.MergeEligible {
		return false
	}
	if record.ReviewClassification == reviewClassificationMissingEvidenceOnly {
		return false
	}
	return record.Status == "review_failed" || record.Status == "timeout" || record.Status == "budget_exceeded" || hasString(evaluation.FrictionSignals, "operational_failure")
}

func operatorAttentionRequired(record runRecord, evaluation evaluationArtifact) bool {
	return evaluation.MergeBlockReason != "" || hasString(evaluation.FrictionSignals, "operational_failure") || hasString(evaluation.FrictionSignals, "out_of_scope_diff_findings") || (record.Status == "success" && record.ReviewStatus != "passed")
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
	if record.Status == "review_failed" {
		return "review did not pass"
	}
	if strings.Contains(strings.ToLower(record.Error), "check") {
		return record.Error
	}
	return ""
}

func frictionSignals(record runRecord, evaluation evaluationArtifact) []string {
	var signals []string
	add := func(signal string) {
		for _, existing := range signals {
			if existing == signal {
				return
			}
		}
		signals = append(signals, signal)
	}
	if record.Status == "review_failed" {
		add("review_failed")
	}
	if record.ReviewClassification == reviewClassificationMissingEvidenceOnly {
		add("missing_behavior_contract_evidence")
	}
	if evaluation.FeedbackRetryCount > 0 {
		add("changes_requested")
	}
	if evaluation.NeedsInfoUsed {
		add("needs_info")
	}
	if record.PRURL == "" && !evaluation.NeedsInfoUsed {
		add("missing_pr_url")
	}
	if record.Status == "failed" || record.Status == "timeout" || record.Status == "budget_exceeded" || strings.HasSuffix(record.Status, "_failed") {
		add("operational_failure")
	}
	if record.Status == "timeout" || record.Status == "budget_exceeded" {
		add(record.Status)
	}
	if strings.Contains(strings.ToLower(record.Error), "validation") {
		add("validation_failure")
	}
	if strings.Contains(strings.ToLower(record.Error), "check") {
		add("check_failure_or_pending")
	}
	if evaluation.TotalTokens >= 200000 || record.DurationMS >= 45*60*1000 {
		add("high_token_or_time_use")
	}
	if strings.Contains(strings.ToLower(record.ReviewFindings), "out-of-scope") || strings.Contains(strings.ToLower(record.ReviewFindings), "scope drift") {
		add("out_of_scope_diff_findings")
	}
	findings := strings.ToLower(record.ReviewFindings + "\n" + record.Error)
	if strings.Contains(findings, "must") || strings.Contains(findings, "acceptance criteria") || strings.Contains(findings, "go-github") || strings.Contains(findings, "bespoke net/http") {
		add("ticket_contract_findings")
	}
	return signals
}

func harnessImprovements(signals []string) []string {
	improvementsBySignal := map[string]string{
		"review_failed":              "tighten implementation prompt or pre-handoff self-review checks",
		"changes_requested":          "surface captured PR feedback prominently on retry",
		"needs_info":                 "clarify issue requirements before agent pickup",
		"missing_pr_url":             "make PR URL extraction or creation failure more deterministic",
		"validation_failure":         "promote validation command failures into the handoff summary",
		"check_failure_or_pending":   "summarize blocking checks before merge attempts",
		"high_token_or_time_use":     "consider smaller issue slices or higher-signal prompts",
		"out_of_scope_diff_findings": "add stronger scoped-diff guardrails",
		"operational_failure":        "inspect runner logs and external service availability",
		"ticket_contract_findings":   "clarify or enforce the five-section ticket contract before retry",
	}
	var improvements []string
	for _, signal := range signals {
		if improvement := improvementsBySignal[signal]; improvement != "" {
			improvements = append(improvements, improvement)
		}
	}
	return improvements
}
