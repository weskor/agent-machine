package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEvaluationArtifactSuccessfulRun(t *testing.T) {
	workspace := t.TempDir()
	record := testRunRecord("success", "https://github.com/pennywise-investments/compound-web/pull/402")
	record.ReviewStatus = "passed"
	record.RuntimeUsage = &usage{TotalTokens: 1000, Cost: &usageCost{Total: 0.01}}
	record.ReviewUsage = &usage{TotalTokens: 200, Cost: &usageCost{Total: 0.02}}

	evaluation := evaluationForRun(workspace, record)

	if evaluation.FinalStatus != "success" || !evaluation.WorkspaceCleanupEligible {
		t.Fatalf("unexpected evaluation: %#v", evaluation)
	}
	if evaluation.TotalTokens != 1200 || evaluation.TotalCost != 0.03 {
		t.Fatalf("unexpected usage totals: %#v", evaluation)
	}
	if evaluation.ReviewPassed == nil || !*evaluation.ReviewPassed {
		t.Fatalf("expected passed review: %#v", evaluation.ReviewPassed)
	}
	if len(evaluation.FrictionSignals) != 0 {
		t.Fatalf("expected no friction signals, got %#v", evaluation.FrictionSignals)
	}
	if evaluation.Outcome != "handoff_ready" || !evaluation.MergeEligible || evaluation.NextAction != "await_approval_and_green_checks" {
		t.Fatalf("expected merge-ready outcome, got %#v", evaluation)
	}
}

func TestEvaluationArtifactRecordsBehaviorContractEvidence(t *testing.T) {
	workspace := t.TempDir()
	record := testRunRecord("review_failed", "https://github.com/pennywise-investments/compound-web/pull/438")
	record.ReviewStatus = "failed"
	record.ReviewFindings = "REVIEW_FAIL missing existing-behavior inventory and parity checklist"

	evaluation := evaluationForRun(workspace, record)
	joined := strings.Join(evaluation.BehaviorContractEvidence, ",")
	for _, expected := range []string{"implementation_prompt_required_behavior_contract_preflight", "review_prompt_required_behavior_contract_parity_check", "review_failed_behavior_contract_or_scope_gate", "findings_recorded_for_behavior_contract_audit"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("expected behavior-contract evidence %q in %#v", expected, evaluation.BehaviorContractEvidence)
		}
	}
}

func TestEvaluationArtifactRecordsTicketContractEvidence(t *testing.T) {
	workspace := t.TempDir()
	record := testRunRecord("review_failed", "https://github.com/pennywise-investments/compound-web/pull/440")
	record.ReviewStatus = "failed"
	record.ReviewFindings = "REVIEW_FAIL violated MUST use github.com/google/go-github/v66/github and MUST NOT add bespoke net/http wrappers"

	evaluation := evaluationForRun(workspace, record)
	joined := strings.Join(evaluation.TicketContractEvidence, ",")
	for _, expected := range []string{"implementation_prompt_required_five_section_ticket_contract", "review_prompt_enforced_ticket_contract_hard_gates", "findings_recorded_for_ticket_contract_audit"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("expected ticket-contract evidence %q in %#v", expected, evaluation.TicketContractEvidence)
		}
	}
	if !containsString(evaluation.FrictionSignals, "ticket_contract_findings") {
		t.Fatalf("expected ticket_contract_findings friction signal: %#v", evaluation.FrictionSignals)
	}
}

func TestEvaluationArtifactRecordsNeedsInfoForIncompleteTicketContract(t *testing.T) {
	evaluation := evaluationForRun(t.TempDir(), testRunRecord("needs_info", ""))

	if !containsString(evaluation.TicketContractEvidence, "needs_info_used_for_incomplete_ticket_contract") {
		t.Fatalf("expected needs-info ticket contract evidence: %#v", evaluation.TicketContractEvidence)
	}
}

func TestEvaluationArtifactReviewFailed(t *testing.T) {
	record := testRunRecord("review_failed", "https://github.com/pennywise-investments/compound-web/pull/402")
	record.ReviewStatus = "failed"
	record.ReviewFindings = "REVIEW_FAIL: scope drift and out-of-scope change"

	evaluation := evaluationForRun(t.TempDir(), record)

	for _, expected := range []string{"review_failed", "operational_failure", "out_of_scope_diff_findings"} {
		if !containsString(evaluation.FrictionSignals, expected) {
			t.Fatalf("expected %s in %#v", expected, evaluation.FrictionSignals)
		}
	}
	if evaluation.MergeBlockReason == "" {
		t.Fatalf("expected merge block reason")
	}
	if evaluation.Outcome != "review_failed" || evaluation.RootCause != "out_of_scope_diff" || !evaluation.ShouldRetry || !evaluation.OperatorAttentionRequired {
		t.Fatalf("expected structured review-failure evaluation, got %#v", evaluation)
	}
}

func TestEvaluationArtifactMissingEvidenceOnlyRoutesHumanReviewWithoutRetry(t *testing.T) {
	record := testRunRecord("success", "https://github.com/pennywise-investments/compound-web/pull/402")
	record.ReviewStatus = "failed"
	record.ReviewClassification = reviewClassificationMissingEvidenceOnly
	record.ReviewFindings = "REVIEW_FAIL\nREVIEW_CLASSIFICATION: missing_evidence_only\nBehavior Contract Evidence missing from PR body."

	evaluation := evaluationForRun(t.TempDir(), record)

	if evaluation.Outcome != "human_review" || evaluation.NextAction != "await_human_review_for_behavior_contract_evidence" || evaluation.ShouldRetry {
		t.Fatalf("expected human-review no-retry outcome, got %#v", evaluation)
	}
	if evaluation.MergeEligible {
		t.Fatal("review failure must remain merge-ineligible")
	}
	if evaluation.ReviewClassification != reviewClassificationMissingEvidenceOnly || !containsString(evaluation.FrictionSignals, "missing_behavior_contract_evidence") {
		t.Fatalf("expected retained classification evidence: %#v", evaluation)
	}
}

func TestEvaluationArtifactNeedsInfo(t *testing.T) {
	evaluation := evaluationForRun(t.TempDir(), testRunRecord("needs_info", ""))

	if !evaluation.NeedsInfoUsed || !containsString(evaluation.FrictionSignals, "needs_info") {
		t.Fatalf("expected needs_info signal: %#v", evaluation)
	}
	if containsString(evaluation.FrictionSignals, "missing_pr_url") {
		t.Fatalf("needs_info should not also report missing_pr_url: %#v", evaluation.FrictionSignals)
	}
}

func TestEvaluationArtifactFeedbackRequested(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, ".pi-symphony-feedback.md"), []byte("changes requested"), 0o600); err != nil {
		t.Fatal(err)
	}

	evaluation := evaluationForRun(workspace, testRunRecord("success", "https://github.com/pennywise-investments/compound-web/pull/402"))

	if evaluation.FeedbackRetryCount != 1 || !containsString(evaluation.FrictionSignals, "changes_requested") {
		t.Fatalf("expected feedback retry signal: %#v", evaluation)
	}
}

func TestEvaluationArtifactMergeBlocked(t *testing.T) {
	record := testRunRecord("failed", "https://github.com/pennywise-investments/compound-web/pull/402")
	record.Error = "check pending: preview deployment"

	evaluation := evaluationForRun(t.TempDir(), record)

	if evaluation.MergeBlockReason != record.Error {
		t.Fatalf("merge block reason = %q, want %q", evaluation.MergeBlockReason, record.Error)
	}
	if !containsString(evaluation.FrictionSignals, "check_failure_or_pending") {
		t.Fatalf("expected check friction: %#v", evaluation.FrictionSignals)
	}
}

func TestWriteEvaluationArtifactAlongsideRunRecord(t *testing.T) {
	workspace := t.TempDir()
	writeEvaluationArtifact(workspace, testRunRecord("success", "https://github.com/pennywise-investments/compound-web/pull/402"))

	data, err := os.ReadFile(filepath.Join(workspace, evaluationArtifactName))
	if err != nil {
		t.Fatal(err)
	}
	var evaluation evaluationArtifact
	if err := json.Unmarshal(data, &evaluation); err != nil {
		t.Fatal(err)
	}
	if evaluation.IssueIdentifier != "CAG-19" {
		t.Fatalf("unexpected evaluation artifact: %s", string(data))
	}
}

func testRunRecord(status, prURL string) runRecord {
	started := time.Date(2026, 5, 17, 1, 0, 0, 0, time.UTC)
	ended := started.Add(2 * time.Minute)
	return runRecord{IssueIdentifier: "CAG-19", IssueID: "issue-id", IssueTitle: "Add evaluations", IssueURL: "https://linear.app/example/issue/CAG-19", Workspace: "/tmp/CAG-19", StartedAt: started, EndedAt: ended, DurationMS: ended.Sub(started).Milliseconds(), PRURL: prURL, Status: status}
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if strings.EqualFold(value, needle) {
			return true
		}
	}
	return false
}
