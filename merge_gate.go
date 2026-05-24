package main

import (
	"fmt"
	"strings"
)

type mergeGateDecision struct {
	Eligible bool
	deterministicGateResult
}

func evaluatePullRequestMergeGate(pr pullRequestSummary) mergeGateDecision {
	decision := newMergeGateDecision(firstNonEmpty(pr.URL, pr.HeadRefName))
	if reason := mergeConflictReason(pr); reason != "" {
		decision.block("merge_conflict", reason)
		return decision
	}
	if !strings.EqualFold(pr.Mergeable, "MERGEABLE") {
		decision.block("mergeable_unknown", fmt.Sprintf("GitHub reports mergeable=%s; waiting for a fresh mergeable result before merging %s.", emptyAsUnknown(pr.Mergeable), pr.HeadRefName))
		return decision
	}
	if reason := checksBlockReason(pr.StatusCheckRollup); reason != "" {
		decision.block("status_checks", reason)
		return decision
	}
	return decision
}

func evaluateRunRecordMergeGate(record runRecord) mergeGateDecision {
	decision := newMergeGateDecision(firstNonEmpty(record.PRURL, record.IssueIdentifier))
	if record.Status == "review_failed" {
		decision.block("review_decision", "review did not pass")
		return decision
	}
	if strings.Contains(strings.ToLower(record.Error), "check") {
		decision.block("status_checks", record.Error)
		return decision
	}
	return decision
}

func newMergeGateDecision(subject string) mergeGateDecision {
	return mergeGateDecision{Eligible: true, deterministicGateResult: newDeterministicGateResult("merge", subject)}
}

func (d *mergeGateDecision) block(code, reason string) {
	d.Eligible = false
	d.Block(code, reason)
}
