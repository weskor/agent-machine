package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExplainCandidateSelectionReportsOrderedSkipsAndSelection(t *testing.T) {
	root := t.TempDir()
	blocked := testIssue("CAG-1", "Ready for Agent")
	blocked.Priority = 1
	addLabels(&blocked, "blocked")
	selected := testIssue("CAG-2", "Ready for Agent")
	selected.Priority = 2

	report := explainCandidateSelection(testRunnerConfig(root), []issue{selected, blocked}, nil)

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

func TestExplainMergeUsesMergeGateBlockers(t *testing.T) {
	root := t.TempDir()
	config := testRunnerConfig(root)
	candidate := testIssue("CAG-7", config.HandoffState)
	pr := pullRequestSummary{URL: "https://github.com/acme/repo/pull/7", HeadRefName: "symphony/CAG-7-workspace", ReviewDecision: "APPROVED", Mergeable: "CONFLICTING", MergeStateStatus: "DIRTY"}

	decisions := explainMergeDecisions(config, map[string]*pullRequestSummary{"CAG-7": &pr}, []issue{candidate})

	if len(decisions) != 1 || decisions[0].CanMerge {
		t.Fatalf("merge decisions = %+v", decisions)
	}
	if !strings.Contains(decisions[0].Reason, "conflicts") {
		t.Fatalf("merge reason = %q, want conflict blocker", decisions[0].Reason)
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
	if _, err := os.Stat(filepath.Join(root, ".symphony", "state", "pi-symphony.db")); !os.IsNotExist(err) {
		t.Fatalf("explain cleanup wrote SQLite state: %v", err)
	}
	if data, err := json.Marshal(explainReport{Mode: "explain", Cleanup: decisions}); err != nil || !json.Valid(data) {
		t.Fatalf("cleanup explanation is not structured JSON: valid=%t err=%v data=%q", json.Valid(data), err, data)
	}
}
