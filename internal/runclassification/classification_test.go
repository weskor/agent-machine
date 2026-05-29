package runclassification

import (
	"testing"
	"time"

	"github.com/weskor/agent-machine/internal/domain"
	"github.com/weskor/agent-machine/internal/reviewpolicy"
)

func TestClassifyRunMissingEvidenceOnlyRoutesHumanReviewWithoutRetry(t *testing.T) {
	record := testRunRecord("success", "https://github.com/weskor/agent-machine/pull/71")
	record.ReviewStatus = "failed"
	record.ReviewClassification = reviewpolicy.MissingEvidenceOnly
	record.ReviewFindings = "REVIEW_FAIL\nBehavior Contract Evidence missing."

	classification := Classify(Input{Record: record})

	if classification.Outcome != "human_review" || classification.RootCause != "missing_behavior_contract_evidence" || classification.NextAction != "await_human_review_for_behavior_contract_evidence" || classification.ShouldRetry {
		t.Fatalf("unexpected classification: %#v", classification)
	}
	if !containsString(classification.FrictionSignals, "missing_behavior_contract_evidence") {
		t.Fatalf("expected missing-evidence friction: %#v", classification.FrictionSignals)
	}
}

func TestClassifyRunCentralizesOperationalFriction(t *testing.T) {
	record := testRunRecord("failed", "")
	record.Error = "validation failed while checking preview"

	classification := Classify(Input{Record: record, MergeBlockReason: record.Error})

	for _, signal := range []string{"missing_pr_url", "operational_failure", "validation_failure", "check_failure_or_pending"} {
		if !containsString(classification.FrictionSignals, signal) {
			t.Fatalf("expected %s in %#v", signal, classification.FrictionSignals)
		}
	}
	if classification.Outcome != "operational_failure" || classification.RootCause != "runner_operational_failure" || !classification.ShouldRetry || !classification.OperatorAttentionRequired {
		t.Fatalf("unexpected operational classification: %#v", classification)
	}
}

func TestClassifyRunReviewNotReadyWaitsWithoutOperatorAttention(t *testing.T) {
	record := testRunRecord(statusReviewNotReady, "https://github.com/weskor/agent-machine/pull/122")
	record.Error = `review not ready: GitHub checks unavailable: check run "GitHub check runs" is status=UNKNOWN conclusion=UNKNOWN`

	classification := Classify(Input{Record: record, MergeBlockReason: record.Error})

	if classification.Outcome != "waiting_for_checks" || classification.RootCause != "waiting_for_checks" || classification.NextAction != "wait_for_github_checks_then_retry" {
		t.Fatalf("expected waiting-for-checks classification, got %#v", classification)
	}
	if !classification.ShouldRetry || classification.OperatorAttentionRequired {
		t.Fatalf("expected retry without operator attention, got %#v", classification)
	}
	if containsString(classification.BlockedBy, "merge_blocked") || !containsString(classification.BlockedBy, "waiting_for_checks") {
		t.Fatalf("expected waiting_for_checks blocker without merge_blocked, got %#v", classification.BlockedBy)
	}
}

func testRunRecord(status, prURL string) domain.RunRecord {
	started := time.Date(2026, 5, 17, 1, 0, 0, 0, time.UTC)
	ended := started.Add(2 * time.Minute)
	return domain.RunRecord{IssueIdentifier: "CAG-19", IssueID: "issue-id", IssueTitle: "Add evaluations", IssueURL: "https://linear.app/example/issue/CAG-19", Workspace: "/tmp/CAG-19", StartedAt: started, EndedAt: ended, DurationMS: ended.Sub(started).Milliseconds(), PRURL: prURL, Status: status}
}
