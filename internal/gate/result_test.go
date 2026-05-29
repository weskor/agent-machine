package gate

import "testing"

func TestResultReasonCodesAndPayload(t *testing.T) {
	result := NewResult("merge", "https://example.test/pr/1")
	result.Block("status_checks", "checks are pending")
	result.Block("status_checks", "checks are still pending")
	result.NextAction = "wait_for_checks"

	if result.Passed() {
		t.Fatal("blocked result should not pass")
	}
	if result.Status != StatusBlocked {
		t.Fatalf("status = %q, want %q", result.Status, StatusBlocked)
	}
	if got := result.Reason(); got != "checks are pending; checks are still pending" {
		t.Fatalf("reason = %q", got)
	}
	codes := result.Codes()
	if len(codes) != 1 || codes[0] != "status_checks" {
		t.Fatalf("codes = %#v, want one status_checks code", codes)
	}
	payload := result.Payload()
	if payload["domain"] != "merge" || payload["status"] != StatusBlocked || payload["subject"] != "https://example.test/pr/1" || payload["next_action"] != "wait_for_checks" {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestResultReconciliationNeeded(t *testing.T) {
	result := NewResult("cleanup", "CAG-1")
	result.ReconciliationNeeded("cleanup_reconciliation_needed", "missing SQLite row", "repair_cleanup")

	if result.Passed() {
		t.Fatal("reconciliation-needed result should not pass")
	}
	if result.Status != StatusReconciliationNeeded {
		t.Fatalf("status = %q, want %q", result.Status, StatusReconciliationNeeded)
	}
	if result.NextAction != "repair_cleanup" {
		t.Fatalf("next action = %q", result.NextAction)
	}
}
