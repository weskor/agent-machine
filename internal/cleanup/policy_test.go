package cleanup

import (
	"testing"

	gatepkg "github.com/weskor/agent-machine/internal/gate"
)

func TestCleanupGateResultUsesDeterministicStatuses(t *testing.T) {
	deleteDecision := Decision{Delete: true, IssueIdentifier: "CAG-1", Category: "completed", Reason: "SQLite issue CAG-1 is Done and durable status is success"}
	deleteGate := GateResult(deleteDecision)
	if !deleteGate.Passed() || deleteGate.Status != gatepkg.StatusPassed || deleteGate.NextAction != "delete_workspace" {
		t.Fatalf("delete gate = %+v, want passed delete gate", deleteGate)
	}
	if deleteGate.Reason() != deleteDecision.Reason {
		t.Fatalf("delete gate reason = %q, want %q", deleteGate.Reason(), deleteDecision.Reason)
	}

	reconcileDecision := Decision{IssueIdentifier: "CAG-2", Category: "reconciliation-needed", Reason: "SQLite has no issue attempt row for workspace CAG-2"}
	reconcileGate := GateResult(reconcileDecision)
	if reconcileGate.Status != gatepkg.StatusReconciliationNeeded || reconcileGate.Passed() {
		t.Fatalf("reconcile gate = %+v, want reconciliation_needed", reconcileGate)
	}
	if !hasString(reconcileGate.Codes(), "cleanup_reconciliation_needed") {
		t.Fatalf("reconcile codes = %+v, want cleanup_reconciliation_needed", reconcileGate.Codes())
	}
	if reconcileGate.NextAction != "repair_or_reconcile_cleanup_state" {
		t.Fatalf("reconcile next action = %q, want repair_or_reconcile_cleanup_state", reconcileGate.NextAction)
	}
}

func TestTerminalRunStatusIncludesArtifactPolicyStatuses(t *testing.T) {
	for _, status := range []string{"success", "review_failed", "failed", "github_app_error", "canceled", "cancelled", "needs_info", "timeout", "budget_exceeded", "merged", "superseded", "manual_repair"} {
		if !TerminalRunStatus(status) {
			t.Fatalf("expected %s to be terminal", status)
		}
	}
}

func TestCleanupDecisionCategoriesTerminalStatuses(t *testing.T) {
	statuses := map[string]string{"success": "completed", "canceled": "canceled", "cancelled": "canceled", "failed": "failed", "github_app_error": "failed", "needs_info": "needs-info", "timeout": "timeout", "budget_exceeded": "budget-exceeded", "merged": "merged", "superseded": "superseded", "manual_repair": "manual-repair"}
	for status, category := range statuses {
		if got := CategoryForTerminalStatus(status); got != category {
			t.Fatalf("category for %s = %s, want %s", status, got, category)
		}
	}
}

func hasString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}
