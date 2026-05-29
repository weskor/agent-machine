package main

import (
	"strings"

	"github.com/weskor/agent-machine/internal/behaviorcontract"
	"github.com/weskor/agent-machine/internal/ticketcontract"
)

// runClassification is the shared classification result consumed by evaluation
// artifacts, PR body handoff evidence, Linear handoff comments, status summaries, and terminal-outcome mirrors.
// It deliberately contains derived policy only; callers still own I/O and
// external state transitions.
type runClassification struct {
	Outcome                      string
	MergeEligible                bool
	BlockedBy                    []string
	RootCause                    string
	NextAction                   string
	ShouldRetry                  bool
	OperatorAttentionRequired    bool
	FrictionSignals              []string
	CandidateHarnessImprovements []string
	BehaviorContractEvidence     []string
	TicketContractEvidence       []string
}

type runClassificationInput struct {
	Record             runRecord
	FeedbackRetryCount int
	NeedsInfoUsed      bool
	MergeBlockReason   string
	TotalTokens        float64
}

func classifyRun(input runClassificationInput) runClassification {
	classification := runClassification{
		BehaviorContractEvidence: behaviorcontract.EvidenceForRun(input.Record),
		TicketContractEvidence:   ticketcontract.EvidenceForRun(input.Record),
	}
	classification.FrictionSignals = classifyFrictionSignals(input)
	classification.CandidateHarnessImprovements = harnessImprovements(classification.FrictionSignals)
	classification.Outcome = classifyOutcome(input, classification)
	classification.MergeEligible = input.Record.Status == "success" && input.Record.ReviewStatus == "passed" && input.Record.PRURL != ""
	classification.BlockedBy = classifyBlockedBy(input)
	classification.RootCause = classifyRootCause(input, classification)
	classification.NextAction = classifyNextAction(input, classification)
	classification.ShouldRetry = classifyShouldRetry(input, classification)
	classification.OperatorAttentionRequired = classifyOperatorAttention(input, classification)
	return classification
}

func classifyRunRecord(workspace string, record runRecord) runClassification {
	return classifyRun(runClassificationInput{
		Record:             record,
		FeedbackRetryCount: feedbackRetryCount(workspace),
		NeedsInfoUsed:      strings.HasPrefix(record.Status, "needs_info"),
		MergeBlockReason:   mergeBlockReason(record),
		TotalTokens:        runRecordTotalTokens(record),
	})
}

func runRecordTotalTokens(record runRecord) float64 {
	var total float64
	if usage := recordRuntimeUsage(record); usage != nil {
		total += usage.TotalTokens
	}
	if record.ReviewUsage != nil {
		total += record.ReviewUsage.TotalTokens
	}
	return total
}

func classifyOutcome(input runClassificationInput, classification runClassification) string {
	record := input.Record
	if record.Status == "success" && record.ReviewStatus == "passed" {
		return "handoff_ready"
	}
	if record.Status == runAttemptStatusReviewNotReady {
		return "waiting_for_checks"
	}
	if input.NeedsInfoUsed {
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

func classifyBlockedBy(input runClassificationInput) []string {
	record := input.Record
	var blockers []string
	if record.PRURL == "" && !input.NeedsInfoUsed {
		blockers = append(blockers, "missing_pr_url")
	}
	if record.Status != "success" {
		if record.Status == runAttemptStatusReviewNotReady {
			blockers = append(blockers, "waiting_for_checks")
		} else {
			blockers = append(blockers, record.Status)
		}
	}
	if record.ReviewStatus != "" && record.ReviewStatus != "passed" {
		blockers = append(blockers, "review_"+record.ReviewStatus)
	}
	if input.MergeBlockReason != "" && record.Status != runAttemptStatusReviewNotReady {
		blockers = append(blockers, "merge_blocked")
	}
	return uniqueStrings(blockers)
}

func classifyRootCause(input runClassificationInput, classification runClassification) string {
	record := input.Record
	if hasString(classification.FrictionSignals, "out_of_scope_diff_findings") {
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
	if record.Status == runAttemptStatusReviewNotReady {
		return "waiting_for_checks"
	}
	if hasString(classification.FrictionSignals, "operational_failure") {
		return "runner_operational_failure"
	}
	if input.MergeBlockReason != "" {
		return "merge_gate_blocked"
	}
	return ""
}

func classifyNextAction(input runClassificationInput, classification runClassification) string {
	record := input.Record
	if classification.MergeEligible {
		return "await_approval_and_green_checks"
	}
	if input.NeedsInfoUsed {
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
	if record.Status == runAttemptStatusReviewNotReady {
		return "wait_for_github_checks_then_retry"
	}
	if record.PRURL == "" {
		return "inspect_run_log_and_create_or_repair_pr"
	}
	if input.MergeBlockReason != "" {
		return "resolve_merge_gate_blocker"
	}
	return "inspect_run_artifact"
}

func classifyShouldRetry(input runClassificationInput, classification runClassification) bool {
	record := input.Record
	if input.NeedsInfoUsed || classification.MergeEligible {
		return false
	}
	if record.ReviewClassification == reviewClassificationMissingEvidenceOnly {
		return false
	}
	if record.Status == runAttemptStatusReviewNotReady {
		return true
	}
	return record.Status == "review_failed" || record.Status == "timeout" || record.Status == "budget_exceeded" || hasString(classification.FrictionSignals, "operational_failure")
}

func classifyOperatorAttention(input runClassificationInput, classification runClassification) bool {
	record := input.Record
	if record.Status == runAttemptStatusReviewNotReady {
		return false
	}
	return input.MergeBlockReason != "" || hasString(classification.FrictionSignals, "operational_failure") || hasString(classification.FrictionSignals, "out_of_scope_diff_findings") || (record.Status == "success" && record.ReviewStatus != "passed")
}

func classifyFrictionSignals(input runClassificationInput) []string {
	record := input.Record
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
	if input.FeedbackRetryCount > 0 {
		add("changes_requested")
	}
	if input.NeedsInfoUsed {
		add("needs_info")
	}
	if record.PRURL == "" && !input.NeedsInfoUsed {
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
	if input.TotalTokens >= 200000 || record.DurationMS >= 45*60*1000 {
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
