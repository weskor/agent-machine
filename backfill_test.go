package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestBackfillStateFromArtifactsSeedsSQLiteIdempotently(t *testing.T) {
	root := t.TempDir()
	workspaceRoot := filepath.Join(root, ".am", "workspaces")
	workspace := filepath.Join(workspaceRoot, "CAG-66")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "am.yaml"), []byte("workspace:\n  base_branch: integration\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "am.agent.md"), []byte("# Test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	record := runRecord{
		IssueIdentifier:      "CAG-66",
		IssueID:              "issue-id",
		Workspace:            workspace,
		WorkspaceRoot:        workspaceRoot,
		Branch:               "am/CAG-66-workspace",
		StartedAt:            now,
		EndedAt:              now.Add(time.Minute),
		ReviewStatus:         "passed",
		ReviewClassification: "ready",
		ReviewFindings:       "REVIEW_PASS",
		PRURL:                "https://github.com/acme/repo/pull/66",
		FeedbackHash:         "feedback-hash",
		Status:               "success",
	}
	writeJSON(t, filepath.Join(workspace, ".am-run.json"), record)
	writeJSON(t, filepath.Join(workspace, evaluationArtifactName), evaluationForRun(workspace, record))

	for i := 0; i < 2; i++ {
		summary, err := backfillStateFromArtifacts(workspaceRoot)
		if err != nil {
			t.Fatalf("backfill run %d error = %v", i+1, err)
		}
		if summary.Scanned != 1 || summary.Seeded != 1 || len(summary.Skipped) != 0 {
			t.Fatalf("summary run %d = %#v", i+1, summary)
		}
	}

	db := openBackfillDB(t, root)
	defer db.Close()
	assertCount(t, db, "issue_attempts", 1)
	assertCount(t, db, "pr_mappings", 1)
	assertCount(t, db, "review_states", 1)
	assertCount(t, db, "feedback_states", 1)
	assertCount(t, db, "terminal_outcomes", 1)
	assertCount(t, db, "external_fact_snapshots", 2)
}

func TestBackfillStateFromArtifactsSkipsMalformedAndHiddenWorkspaces(t *testing.T) {
	root := t.TempDir()
	workspaceRoot := filepath.Join(root, ".am", "workspaces")
	valid := filepath.Join(workspaceRoot, "CAG-67")
	malformed := filepath.Join(workspaceRoot, "CAG-68")
	hidden := filepath.Join(workspaceRoot, ".am-locks")
	for _, dir := range []string{valid, malformed, hidden} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeJSON(t, filepath.Join(valid, ".am-run.json"), runRecord{IssueIdentifier: "CAG-67", Workspace: valid, WorkspaceRoot: workspaceRoot, Status: "review_failed", PRURL: "https://github.com/acme/repo/pull/67"})
	if err := os.WriteFile(filepath.Join(malformed, ".am-run.json"), []byte(`{"issue_identifier":`), 0o600); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(hidden, ".am-run.json"), runRecord{IssueIdentifier: "CAG-hidden", Status: "success"})

	summary, err := backfillStateFromArtifacts(workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Scanned != 2 || summary.Seeded != 1 || len(summary.Skipped) != 1 {
		t.Fatalf("summary = %#v", summary)
	}
	if !strings.Contains(summary.Skipped[0].Reason, "malformed .am-run.json") {
		t.Fatalf("skip reason = %q", summary.Skipped[0].Reason)
	}

	db := openBackfillDB(t, root)
	defer db.Close()
	assertCount(t, db, "issue_attempts", 1)
}

func TestBackfillStateFromArtifactsMarksConflictingIssueArtifactsForReconciliation(t *testing.T) {
	root := t.TempDir()
	workspaceRoot := filepath.Join(root, ".am", "workspaces")
	newer := filepath.Join(workspaceRoot, "CAG-69-newer")
	older := filepath.Join(workspaceRoot, "CAG-69-older")
	for _, dir := range []string{newer, older} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeJSON(t, filepath.Join(newer, ".am-run.json"), runRecord{IssueIdentifier: "CAG-69", Workspace: newer, WorkspaceRoot: workspaceRoot, Status: "review_failed", PRURL: "https://github.com/acme/repo/pull/69", EndedAt: time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)})
	writeJSON(t, filepath.Join(older, ".am-run.json"), runRecord{IssueIdentifier: "CAG-69", Workspace: older, WorkspaceRoot: workspaceRoot, Status: "success", PRURL: "https://github.com/acme/repo/pull/69", EndedAt: time.Date(2026, 5, 19, 11, 0, 0, 0, time.UTC)})

	summary, err := backfillStateFromArtifacts(workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Scanned != 2 || summary.Seeded != 0 || summary.ReconciliationNeeded != 1 || len(summary.Skipped) != 0 {
		t.Fatalf("summary = %#v", summary)
	}

	db := openBackfillDB(t, root)
	defer db.Close()
	assertCount(t, db, "issue_attempts", 1)
	var status, terminalReason string
	if err := db.QueryRow(`SELECT status FROM issue_attempts WHERE issue_key = 'CAG-69'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "reconciliation-needed" {
		t.Fatalf("status = %q, want reconciliation-needed", status)
	}
	if err := db.QueryRow(`SELECT reason FROM terminal_outcomes JOIN issue_attempts ON issue_attempts.id = terminal_outcomes.attempt_id WHERE issue_key = 'CAG-69'`).Scan(&terminalReason); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(terminalReason, "conflicting artifacts") {
		t.Fatalf("terminal reason = %q", terminalReason)
	}
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func openBackfillDB(t *testing.T, root string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(root, ".am", "state", "am.db"))
	if err != nil {
		t.Fatal(err)
	}
	return db
}
