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
	workspaceRoot := filepath.Join(root, ".symphony", "workspaces")
	workspace := filepath.Join(workspaceRoot, "CAG-66")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "WORKFLOW.md"), []byte("---\nworkspace:\n  base_branch: integration\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	record := runRecord{
		IssueIdentifier:      "CAG-66",
		IssueID:              "issue-id",
		Workspace:            workspace,
		WorkspaceRoot:        workspaceRoot,
		Branch:               "symphony/CAG-66-workspace",
		StartedAt:            now,
		EndedAt:              now.Add(time.Minute),
		ReviewStatus:         "passed",
		ReviewClassification: "ready",
		ReviewFindings:       "REVIEW_PASS",
		PRURL:                "https://github.com/acme/repo/pull/66",
		FeedbackHash:         "feedback-hash",
		Status:               "success",
	}
	writeJSON(t, filepath.Join(workspace, ".pi-symphony-run.json"), record)
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
	workspaceRoot := filepath.Join(root, ".symphony", "workspaces")
	valid := filepath.Join(workspaceRoot, "CAG-67")
	malformed := filepath.Join(workspaceRoot, "CAG-68")
	hidden := filepath.Join(workspaceRoot, ".pi-symphony-locks")
	for _, dir := range []string{valid, malformed, hidden} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeJSON(t, filepath.Join(valid, ".pi-symphony-run.json"), runRecord{IssueIdentifier: "CAG-67", Workspace: valid, WorkspaceRoot: workspaceRoot, Status: "review_failed", PRURL: "https://github.com/acme/repo/pull/67"})
	if err := os.WriteFile(filepath.Join(malformed, ".pi-symphony-run.json"), []byte(`{"issue_identifier":`), 0o600); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(hidden, ".pi-symphony-run.json"), runRecord{IssueIdentifier: "CAG-hidden", Status: "success"})

	summary, err := backfillStateFromArtifacts(workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Scanned != 2 || summary.Seeded != 1 || len(summary.Skipped) != 1 {
		t.Fatalf("summary = %#v", summary)
	}
	if !strings.Contains(summary.Skipped[0].Reason, "malformed .pi-symphony-run.json") {
		t.Fatalf("skip reason = %q", summary.Skipped[0].Reason)
	}

	db := openBackfillDB(t, root)
	defer db.Close()
	assertCount(t, db, "issue_attempts", 1)
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
	db, err := sql.Open("sqlite", filepath.Join(root, ".symphony", "state", "pi-symphony.db"))
	if err != nil {
		t.Fatal(err)
	}
	return db
}
