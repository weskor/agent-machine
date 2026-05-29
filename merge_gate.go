package main

import mergegate "github.com/weskor/agent-machine/internal/mergegate"

type mergeGateDecision = mergegate.Decision

func evaluatePullRequestMergeGate(pr pullRequestSummary) mergeGateDecision {
	return mergegate.EvaluatePullRequest(mergegate.PullRequest{
		Subject:          firstNonEmpty(pr.URL, pr.HeadRefName),
		HeadRefName:      pr.HeadRefName,
		Mergeable:        pr.Mergeable,
		MergeStateStatus: pr.MergeStateStatus,
		StatusChecks:     mergegateStatusChecks(pr.StatusCheckRollup),
	})
}

func evaluateRunRecordMergeGate(record runRecord) mergeGateDecision {
	return mergegate.EvaluateRunRecord(mergegate.RunRecord{
		Subject: firstNonEmpty(record.PRURL, record.IssueIdentifier),
		Status:  record.Status,
		Error:   record.Error,
	})
}

func checksPassed(checks []statusCheck) bool {
	return mergegate.ChecksPassed(mergegateStatusChecks(checks))
}

func checksBlockReason(checks []statusCheck) string {
	return mergegate.ChecksBlockReason(mergegateStatusChecks(checks))
}

func checkLabel(check statusCheck) string {
	return mergegate.CheckLabel(mergegateStatusCheck(check))
}

func mergeConflictReason(pr pullRequestSummary) string {
	return mergegate.ConflictReason(mergegate.PullRequest{
		HeadRefName:      pr.HeadRefName,
		Mergeable:        pr.Mergeable,
		MergeStateStatus: pr.MergeStateStatus,
	})
}

func mergegateStatusChecks(checks []statusCheck) []mergegate.StatusCheck {
	out := make([]mergegate.StatusCheck, 0, len(checks))
	for _, check := range checks {
		out = append(out, mergegateStatusCheck(check))
	}
	return out
}

func mergegateStatusCheck(check statusCheck) mergegate.StatusCheck {
	return mergegate.StatusCheck{
		Typename:   check.Typename,
		Status:     check.Status,
		Conclusion: check.Conclusion,
		State:      check.State,
		Name:       check.Name,
		Context:    check.Context,
	}
}
