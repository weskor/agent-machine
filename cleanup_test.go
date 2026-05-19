package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	sh "github.com/weskor/pi-symphony/internal/shell"
)

func TestCleanupDecisionDeletesDoneIssueWorkspace(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-1")
	writeCleanRunArtifact(t, workspace, "success")

	decision, err := cleanupDecision(workspace, map[string]bool{"CAG-1": true})
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Delete || decision.Category != "completed" {
		t.Fatalf("expected Done issue workspace to be deleted, got %+v", decision)
	}
}

func TestCleanupDecisionKeepsTerminalArtifactsUntilIssueIsDone(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-2")
	writeCleanRunArtifact(t, workspace, "success")

	decision, err := cleanupDecision(workspace, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Delete || decision.Category != "not-done" {
		t.Fatalf("expected non-Done terminal workspace to be kept, got %+v", decision)
	}
}

func TestCleanupWorkspacesSkipsHiddenLockDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".pi-symphony-locks"), 0o755); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "CAG-1")
	writeCleanRunArtifact(t, workspace, "success")

	if err := cleanupWorkspaces(root, cleanupOptions{DoneIssues: map[string]bool{"CAG-1": true}}); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupDecisionPreservesDirtyDoneWorkspaces(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-3")
	writeCleanRunArtifact(t, workspace, "success")
	if err := os.WriteFile(filepath.Join(workspace, "tracked.txt"), []byte("dirty"), 0o600); err != nil {
		t.Fatal(err)
	}

	decision, err := cleanupDecision(workspace, map[string]bool{"CAG-3": true})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Delete || decision.Category != "dirty" {
		t.Fatalf("expected dirty Done workspace to be preserved, got %+v", decision)
	}
}

func TestCleanupDecisionKeepsMissingAndInsufficientArtifacts(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-4")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := sh.Run("git init -q", workspace); err != nil {
		t.Fatal(err)
	}

	decision, err := cleanupDecision(workspace, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Delete || decision.Reason != "missing run artifact" {
		t.Fatalf("expected missing artifact keep, got %+v", decision)
	}

	if err := os.WriteFile(filepath.Join(workspace, ".pi-symphony-run.json"), []byte(`{"status":"success","ended_at":"2026-05-01T00:00:00Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	decision, err = cleanupDecision(workspace, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Delete || !strings.Contains(decision.Reason, "missing required") {
		t.Fatalf("expected insufficient artifact keep, got %+v", decision)
	}
}

func TestCleanupDecisionRequiresReviewEvidenceForFailedReview(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-5")
	writeCleanRunArtifact(t, workspace, "review_failed")

	decision, err := cleanupDecision(workspace, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Delete || !strings.Contains(decision.Reason, "review evidence") {
		t.Fatalf("expected missing review evidence keep, got %+v", decision)
	}

	record := runArtifactJSON(workspace, "review_failed")
	record = strings.TrimSuffix(record, "}") + `,"pr_url":"https://github.com/acme/repo/pull/1","review_status":"failed","review_findings":"needs changes"}`
	if err := os.WriteFile(filepath.Join(workspace, ".pi-symphony-run.json"), []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
	decision, err = cleanupDecision(workspace, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Delete || decision.Category != "not-done" {
		t.Fatalf("expected complete failed-review artifact to wait for Done, got %+v", decision)
	}
}

func TestCleanupRefusesUnsafeDeletePaths(t *testing.T) {
	root := t.TempDir()
	if _, err := safeWorkspacePath(root, "../outside"); err == nil {
		t.Fatal("expected path traversal workspace name to be refused")
	}
	if err := assertSafeDeletePath(root, root); err == nil {
		t.Fatal("expected root delete path to be refused")
	}
	if err := assertSafeDeletePath(root, filepath.Dir(root)); err == nil {
		t.Fatal("expected parent delete path to be refused")
	}
	workspace, err := safeWorkspacePath(root, "CAG-6")
	if err != nil {
		t.Fatal(err)
	}
	if workspace != filepath.Join(root, "CAG-6") {
		t.Fatalf("unexpected safe workspace path %s", workspace)
	}
}

func TestTerminalRunStatusIncludesArtifactPolicyStatuses(t *testing.T) {
	for _, status := range []string{"success", "review_failed", "failed", "github_app_error", "canceled", "cancelled", "needs_info", "timeout", "budget_exceeded", "merged", "superseded", "manual_repair"} {
		if !terminalRunStatus(status) {
			t.Fatalf("expected %s to be terminal", status)
		}
	}
}

func TestCleanupDecisionCategoriesTerminalStatuses(t *testing.T) {
	statuses := map[string]string{"success": "completed", "canceled": "canceled", "cancelled": "canceled", "failed": "failed", "github_app_error": "failed", "needs_info": "needs-info", "timeout": "timeout", "budget_exceeded": "budget-exceeded", "merged": "merged", "superseded": "superseded", "manual_repair": "manual-repair"}
	for status, category := range statuses {
		if got := cleanupCategoryForTerminalStatus(status); got != category {
			t.Fatalf("category for %s = %s, want %s", status, got, category)
		}
	}
}

func TestCleanupDecisionRequiresTerminalDoneArtifact(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-41")
	writeCleanRunArtifact(t, workspace, "handoff_failed")
	decision, err := cleanupDecision(workspace, map[string]bool{"CAG-41": true})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Delete || decision.Category != "non-terminal" {
		t.Fatalf("expected non-terminal Done workspace to be preserved, got %+v", decision)
	}
}

func TestCleanupDecisionRequiresMatchingWorkspaceRootAndBranch(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-40")
	writeCleanRunArtifact(t, workspace, "success")
	record := runArtifactJSON(workspace, "success")
	record = strings.TrimSuffix(record, "}") + `,"workspace_root":"/tmp/not-this-root","expected_branch":"symphony/CAG-40-workspace","branch":"symphony/other"}`
	if err := os.WriteFile(filepath.Join(workspace, ".pi-symphony-run.json"), []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
	decision, err := cleanupDecision(workspace, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Delete || !strings.Contains(decision.Reason, "workspace root mismatch") {
		t.Fatalf("expected workspace root mismatch keep, got %+v", decision)
	}
}

func writeCleanRunArtifact(t *testing.T, workspace, status string) {
	t.Helper()
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := sh.Run("git init -q", workspace); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".pi-symphony-run.json"), []byte(runArtifactJSON(workspace, status)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runArtifactJSON(workspace, status string) string {
	return `{"issue_identifier":"` + filepath.Base(workspace) + `","issue_id":"issue-id","issue_title":"Title","issue_url":"https://linear.app/acme/issue/` + filepath.Base(workspace) + `/title","workspace":"` + filepath.ToSlash(workspace) + `","branch":"symphony/` + filepath.Base(workspace) + `","status":"` + status + `","ended_at":"2026-05-01T00:00:00Z"}`
}
