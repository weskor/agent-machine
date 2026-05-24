package main

import (
	"strings"
	"testing"
)

func TestMergeGateUsesDeterministicGateResult(t *testing.T) {
	pr := pullRequestSummary{URL: "https://github.com/acme/repo/pull/7", HeadRefName: "symphony/CAG-7-workspace", Mergeable: "CONFLICTING", MergeStateStatus: "DIRTY"}

	gate := evaluatePullRequestMergeGate(pr)

	if gate.Eligible || gate.Status != deterministicGateStatusBlocked {
		t.Fatalf("gate status = eligible %t status %q, want blocked", gate.Eligible, gate.Status)
	}
	if !hasString(gate.Codes(), "merge_conflict") {
		t.Fatalf("gate codes = %+v, want merge_conflict", gate.Codes())
	}
	if !strings.Contains(gate.Reason(), "conflicts with the base branch") {
		t.Fatalf("gate reason = %q, want conflict reason", gate.Reason())
	}
	payload := gate.Payload()
	if payload["domain"] != "merge" || payload["status"] != deterministicGateStatusBlocked || payload["subject"] != pr.URL {
		t.Fatalf("gate payload = %+v, want merge blocked payload for PR", payload)
	}
}

func TestCleanupGateResultUsesDeterministicStatuses(t *testing.T) {
	deleteDecision := cleanupResult{Delete: true, IssueIdentifier: "CAG-1", Category: "completed", Reason: "SQLite issue CAG-1 is Done and durable status is success"}
	deleteGate := cleanupGateResult(deleteDecision)
	if !deleteGate.Passed() || deleteGate.Status != deterministicGateStatusPassed || deleteGate.NextAction != "delete_workspace" {
		t.Fatalf("delete gate = %+v, want passed delete gate", deleteGate)
	}
	if deleteGate.Reason() != deleteDecision.Reason {
		t.Fatalf("delete gate reason = %q, want %q", deleteGate.Reason(), deleteDecision.Reason)
	}

	reconcileDecision := cleanupResult{IssueIdentifier: "CAG-2", Category: "reconciliation-needed", Reason: "SQLite has no issue attempt row for workspace CAG-2"}
	reconcileGate := cleanupGateResult(reconcileDecision)
	if reconcileGate.Status != deterministicGateStatusReconciliationNeeded || reconcileGate.Passed() {
		t.Fatalf("reconcile gate = %+v, want reconciliation_needed", reconcileGate)
	}
	if !hasString(reconcileGate.Codes(), "cleanup_reconciliation_needed") {
		t.Fatalf("reconcile codes = %+v, want cleanup_reconciliation_needed", reconcileGate.Codes())
	}
	if reconcileGate.NextAction != "repair_or_reconcile_cleanup_state" {
		t.Fatalf("reconcile next action = %q, want repair_or_reconcile_cleanup_state", reconcileGate.NextAction)
	}
}
