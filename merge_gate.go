package main

import (
	"fmt"
	"strings"
)

type mergeGateBlocker struct {
	Code   string
	Reason string
}

type mergeGateDecision struct {
	Eligible bool
	Blockers []mergeGateBlocker
}

func (d mergeGateDecision) Reason() string {
	reasons := make([]string, 0, len(d.Blockers))
	for _, blocker := range d.Blockers {
		if strings.TrimSpace(blocker.Reason) != "" {
			reasons = append(reasons, blocker.Reason)
		}
	}
	return strings.Join(reasons, "; ")
}

func (d mergeGateDecision) Codes() []string {
	codes := make([]string, 0, len(d.Blockers))
	for _, blocker := range d.Blockers {
		codes = append(codes, blocker.Code)
	}
	return uniqueStrings(codes)
}

func evaluateMergeGate(config runnerConfig, candidate issue, pr pullRequestSummary) mergeGateDecision {
	var decision mergeGateDecision
	if reason := prInvariantBlockReason(config, candidate, pr); reason != "" {
		decision.block("pr_invariant", reason)
		return decision
	}
	if pr.ReviewDecision != "APPROVED" {
		decision.block("review_decision", fmt.Sprintf("waiting for approval; reviewDecision=%s", emptyAsUnknown(pr.ReviewDecision)))
		return decision
	}
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
	if reason := runArtifactMergeBlockReason(config.WorkspaceRoot, candidate.Identifier, pr.URL); reason != "" {
		decision.block("run_artifact", reason)
		return decision
	}
	if locked, reason := workspaceLockedOrModified(config.WorkspaceRoot, candidate.Identifier, pr.HeadRefName); locked {
		decision.block("workspace_state", reason)
		return decision
	}
	decision.Eligible = true
	return decision
}

func evaluatePullRequestMergeGate(pr pullRequestSummary) mergeGateDecision {
	var decision mergeGateDecision
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
	decision.Eligible = true
	return decision
}

func evaluateRunRecordMergeGate(record runRecord) mergeGateDecision {
	var decision mergeGateDecision
	if record.Status == "review_failed" {
		decision.block("review_decision", "review did not pass")
		return decision
	}
	if strings.Contains(strings.ToLower(record.Error), "check") {
		decision.block("status_checks", record.Error)
		return decision
	}
	decision.Eligible = true
	return decision
}

func (d *mergeGateDecision) block(code, reason string) {
	d.Eligible = false
	d.Blockers = append(d.Blockers, mergeGateBlocker{Code: code, Reason: reason})
}
