package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weskor/agent-machine/internal/state"
)

func TestExplainCandidateSelectionReportsOrderedSkipsAndSelection(t *testing.T) {
	root := t.TempDir()
	blocked := testIssue("CAG-1", "Ready for Agent")
	blocked.Priority = 1
	addLabels(&blocked, "blocked")
	selected := testIssue("CAG-2", "Ready for Agent")
	selected.Priority = 2

	report := explainCandidateSelection(testRunnerConfig(root), []issue{selected, blocked}, nil, nil)

	if report.Selected != "CAG-2" {
		t.Fatalf("Selected = %q, want CAG-2", report.Selected)
	}
	if len(report.Candidates) != 2 || report.Candidates[0].Identifier != "CAG-1" || report.Candidates[1].Identifier != "CAG-2" {
		t.Fatalf("candidate order = %+v", report.Candidates)
	}
	if report.Candidates[0].Runnable || !strings.Contains(report.Candidates[0].Reason, "blocked label") {
		t.Fatalf("blocked candidate explanation = %+v", report.Candidates[0])
	}
	if !report.Candidates[1].Selected || !report.Candidates[1].Runnable {
		t.Fatalf("selected candidate explanation = %+v", report.Candidates[1])
	}
}

func TestExplainCandidateSelectionReportsSQLiteMissingPRReconciliation(t *testing.T) {
	root := t.TempDir()
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertRunArtifact(context.Background(), state.RunArtifactSnapshot{IssueKey: "CAG-115", Attempt: 1, BranchName: expectedWorkspaceBranch("CAG-115"), Status: "success", Repository: "weskor/agent-machine", PRNumber: 115, PRURL: "https://github.com/weskor/agent-machine/pull/115", TerminalOutcome: "handoff_ready"}); err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	report := explainCandidateSelection(testRunnerConfig(root), []issue{testIssue("CAG-115", "Ready for Agent")}, nil, store)

	if report.Selected != "" || len(report.Candidates) != 1 || !strings.Contains(report.Candidates[0].Reason, "SQLite PR mapping") || !strings.Contains(report.Candidates[0].Reason, "reconciliation_needed") {
		t.Fatalf("expected missing PR reconciliation in explain output, got %+v", report)
	}
}

func TestExplainCandidateSelectionReportsRepairableReviewFailedPRRunnable(t *testing.T) {
	root := t.TempDir()
	config := testRunnerConfig(root)
	config.BaseBranch = "develop"
	candidate := testIssue("CAG-141", "Ready for Agent")
	pr := seedRepairableReviewFailedPR(t, root, candidate.Identifier, "https://github.com/weskor/agent-machine/pull/93")

	report := explainCandidateSelection(config, []issue{candidate}, map[string]*pullRequestSummary{candidate.Identifier: &pr}, nil)

	if report.Selected != candidate.Identifier || len(report.Candidates) != 1 || !report.Candidates[0].Runnable || report.Candidates[0].NextAction != repairReviewFindingsNextAction {
		t.Fatalf("expected repair-runnable explain candidate, got %+v", report)
	}
	if strings.Contains(report.Candidates[0].Reason, "reconciliation_needed") || strings.Contains(report.Candidates[0].Reason, "PR exists while Linear state") {
		t.Fatalf("expected explain to avoid existing-PR reconciliation blocker, got %+v", report.Candidates[0])
	}
}

func TestExplainMergeUsesMergeGateBlockers(t *testing.T) {
	root := t.TempDir()
	config := testRunnerConfig(root)
	candidate := testIssue("CAG-7", config.HandoffState)
	pr := pullRequestSummary{URL: "https://github.com/acme/repo/pull/7", HeadRefName: "am/CAG-7-workspace", ReviewDecision: "APPROVED", Mergeable: "CONFLICTING", MergeStateStatus: "DIRTY"}

	decisions := explainMergeDecisions(config, map[string]*pullRequestSummary{"CAG-7": &pr}, []issue{candidate}, nil)

	if len(decisions) != 1 || decisions[0].CanMerge {
		t.Fatalf("merge decisions = %+v", decisions)
	}
	if !strings.Contains(decisions[0].Reason, "conflicts") {
		t.Fatalf("merge reason = %q, want conflict blocker", decisions[0].Reason)
	}
}

func TestExplainMergeKeepsRepairableReviewFailedPRMergeIneligible(t *testing.T) {
	root := t.TempDir()
	config := testRunnerConfig(root)
	config.BaseBranch = "develop"
	candidate := testIssue("CAG-141", "Ready for Agent")
	pr := seedRepairableReviewFailedPR(t, root, candidate.Identifier, "https://github.com/weskor/agent-machine/pull/93")

	decisions := explainMergeDecisions(config, map[string]*pullRequestSummary{candidate.Identifier: &pr}, []issue{candidate}, nil)

	if len(decisions) != 1 || decisions[0].CanMerge || decisions[0].NextAction != repairReviewFindingsNextAction {
		t.Fatalf("expected merge-ineligible repair decision, got %+v", decisions)
	}
}

func TestExplainDoesNotCreateSQLiteStateOnFreshWorkspace(t *testing.T) {
	root := t.TempDir()
	config := testRunnerConfig(root)

	if _, err := explain(config, []issue{testIssue("CAG-7", "Ready for Agent")}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(state.DefaultDBPath(root)); !os.IsNotExist(err) {
		t.Fatalf("explain created SQLite state on fresh workspace: %v", err)
	}
}

func TestExplainCleanupDoesNotDeleteEligibleWorkspace(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-8")
	writeCleanRunArtifact(t, workspace, "success")

	var decisions []explainCleanupDecision
	var err error
	output := captureStdout(t, func() {
		decisions, err = explainCleanup(root, map[string]bool{"CAG-8": true})
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(output) != "" {
		t.Fatalf("explain cleanup wrote non-JSON log output: %q", output)
	}
	if len(decisions) != 1 || !decisions[0].Eligible {
		t.Fatalf("cleanup decisions = %+v", decisions)
	}
	if _, err := os.Stat(workspace); err != nil {
		t.Fatalf("explain cleanup mutated workspace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".am", "state", "am.db")); !os.IsNotExist(err) {
		t.Fatalf("explain cleanup wrote SQLite state: %v", err)
	}
	if data, err := json.Marshal(explainReport{Mode: "explain", Cleanup: decisions}); err != nil || !json.Valid(data) {
		t.Fatalf("cleanup explanation is not structured JSON: valid=%t err=%v data=%q", json.Valid(data), err, data)
	}
}

func TestExplainCleanupUsesSQLiteFactsDespiteStaleArtifactIdentity(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".am", "workspaces")
	workspace := filepath.Join(root, "CAG-138")
	writeCleanRunArtifact(t, workspace, "success")
	artifactPath := filepath.Join(workspace, ".am-run.json")
	artifact := strings.Replace(runArtifactJSON(workspace, "success"), `"issue_identifier":"CAG-138"`, `"issue_identifier":"CAG-other"`, 1)
	if err := os.WriteFile(artifactPath, []byte(artifact), 0o600); err != nil {
		t.Fatal(err)
	}
	seedCleanupAttempt(t, root, workspace, "CAG-138", "success")

	artifactDecision, err := cleanupDecisionForRoot(root, workspace, map[string]bool{"CAG-138": true})
	if err != nil {
		t.Fatal(err)
	}
	if !artifactDecision.Delete {
		t.Fatalf("artifact-only cleanup decision = %+v, want eligible", artifactDecision)
	}

	decisions, err := explainCleanup(root, map[string]bool{"CAG-138": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 1 || !decisions[0].Eligible || decisions[0].Category != "completed" || !strings.Contains(decisions[0].Reason, "SQLite issue CAG-138 is Done") {
		t.Fatalf("cleanup decisions = %+v, want SQLite-backed completed decision despite stale artifact identity", decisions)
	}
	if _, err := os.Stat(workspace); err != nil {
		t.Fatalf("explain cleanup mutated workspace: %v", err)
	}
	if countCleanupStateRows(t, root) != 0 {
		t.Fatalf("explain cleanup wrote cleanup state rows")
	}
}

func TestExplainCleanupIgnoresZeroByteFalseMarker(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-126")
	writeCleanRunArtifact(t, workspace, "success")
	if err := os.WriteFile(filepath.Join(workspace, "false"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	decisions, err := explainCleanup(root, map[string]bool{"CAG-126": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 1 || !decisions[0].Eligible || decisions[0].Category == "dirty" {
		t.Fatalf("cleanup decisions = %+v, want false marker ignored", decisions)
	}
}

func countCleanupStateRows(t *testing.T, workspaceRoot string) int {
	t.Helper()
	db, err := sql.Open("sqlite", state.DefaultDBPath(workspaceRoot))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM cleanup_states`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
