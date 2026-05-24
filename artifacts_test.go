package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestCorrectedPRURLFromReviewFindings(t *testing.T) {
	got := correctedPRURL(
		"https://github.com/pennywise-investments/compound-web/pull/2",
		"REVIEW_PASS\n\nFindings:\n- Actual CAG-11 branch PR is #400, not prompt-listed #2.",
	)
	want := "https://github.com/pennywise-investments/compound-web/pull/400"
	if got != want {
		t.Fatalf("correctedPRURL() = %q, want %q", got, want)
	}
}

func TestCorrectedPRURLNoFinding(t *testing.T) {
	if got := correctedPRURL("https://github.com/example/repo/pull/2", "REVIEW_PASS"); got != "" {
		t.Fatalf("correctedPRURL() = %q, want empty", got)
	}
}

func TestWriteRunRecordPersistsBudgetTerminalStatus(t *testing.T) {
	workspace := t.TempDir()
	now := time.Now()
	record := runRecordFor(&issue{ID: "issue-id", Identifier: "CAG-1", Title: "title"}, workspace, "pi", "", now, now, nil, nil, "", "timeout", "command timed out", (&runBudget{PiText: "1s", PiTimeout: time.Second}).Active(), "command timed out")
	writeRunRecord(workspace, record)

	data, err := os.ReadFile(filepath.Join(workspace, ".pi-symphony-run.json"))
	if err != nil {
		t.Fatal(err)
	}
	var persisted runRecord
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Status != "timeout" || persisted.BudgetExceeded == "" || persisted.Budget == nil || persisted.Budget.PiText != "1s" {
		t.Fatalf("unexpected persisted run record: %#v", persisted)
	}
	if !terminalRunStatus(persisted.Status) {
		t.Fatalf("expected timeout to be terminal")
	}
}

func TestWriteRunRecordMirrorsSQLiteStateIdempotently(t *testing.T) {
	root := t.TempDir()
	workspaceRoot := filepath.Join(root, ".symphony", "workspaces")
	workspace := filepath.Join(workspaceRoot, "CAG-61")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "WORKFLOW.md"), []byte("---\nworkspace:\n  base_branch: integration\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	record := runRecord{
		IssueIdentifier:      "CAG-61",
		IssueID:              "issue-id",
		IssueTitle:           "Mirror artifacts",
		Workspace:            workspace,
		WorkspaceRoot:        workspaceRoot,
		Branch:               "symphony/CAG-61-workspace",
		ExpectedBranch:       "symphony/CAG-61-workspace",
		StartedAt:            now,
		EndedAt:              now.Add(time.Second),
		ReviewStatus:         "passed",
		ReviewClassification: "ready",
		ReviewFindings:       "REVIEW_PASS",
		PRURL:                "https://github.com/acme/repo/pull/61",
		FeedbackHash:         "feedback-hash",
		Status:               "success",
	}

	writeRunRecord(workspace, record)
	writeRunRecord(workspace, record)

	for _, name := range []string{".pi-symphony-run.json", evaluationArtifactName} {
		if _, err := os.Stat(filepath.Join(workspace, name)); err != nil {
			t.Fatalf("expected artifact %s: %v", name, err)
		}
	}
	db, err := sql.Open("sqlite", filepath.Join(root, ".symphony", "state", "pi-symphony.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	assertCount(t, db, "issue_attempts", 1)
	assertCount(t, db, "pr_mappings", 1)
	assertCount(t, db, "review_states", 1)
	assertCount(t, db, "feedback_states", 1)
	assertCount(t, db, "terminal_outcomes", 1)
	var prURL, baseBranch, reviewStatus, feedbackHash, outcome string
	if err := db.QueryRow(`SELECT pr_url FROM pr_mappings`).Scan(&prURL); err != nil || prURL != record.PRURL {
		t.Fatalf("pr mapping = %q, %v", prURL, err)
	}
	if err := db.QueryRow(`SELECT base_branch FROM pr_mappings`).Scan(&baseBranch); err != nil || baseBranch != "integration" {
		t.Fatalf("base branch = %q, %v", baseBranch, err)
	}
	if err := db.QueryRow(`SELECT command_status FROM review_states`).Scan(&reviewStatus); err != nil || reviewStatus != "passed" {
		t.Fatalf("review status = %q, %v", reviewStatus, err)
	}
	if err := db.QueryRow(`SELECT feedback_hash FROM feedback_states`).Scan(&feedbackHash); err != nil || feedbackHash != "feedback-hash" {
		t.Fatalf("feedback hash = %q, %v", feedbackHash, err)
	}
	if err := db.QueryRow(`SELECT outcome FROM terminal_outcomes`).Scan(&outcome); err != nil || outcome != "handoff_ready" {
		t.Fatalf("terminal outcome = %q, %v", outcome, err)
	}
}

func TestWriteRunRecordWithoutWorkspaceRootSkipsSQLiteMirror(t *testing.T) {
	workspace := t.TempDir()
	record := runRecord{IssueIdentifier: "CAG-legacy", Workspace: workspace, Status: "success", StartedAt: time.Now(), EndedAt: time.Now()}
	writeRunRecord(workspace, record)
	if _, err := os.Stat(filepath.Join(workspace, ".pi-symphony-run.json")); err != nil {
		t.Fatalf("expected run artifact: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, evaluationArtifactName)); err != nil {
		t.Fatalf("expected evaluation artifact: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "state", "pi-symphony.db")); !os.IsNotExist(err) {
		t.Fatalf("unexpected sqlite state for legacy helper path: %v", err)
	}
}

func assertCount(t *testing.T, db *sql.DB, table string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s count = %d, want %d", table, got, want)
	}
}

func TestRepairArtifactMarksManuallyMergedPRWithoutDroppingUsage(t *testing.T) {
	old := prStateForURL
	prStateForURL = func(prURL string) (string, bool, error) { return "MERGED", true, nil }
	t.Cleanup(func() { prStateForURL = old })

	workspace := t.TempDir()
	path := filepath.Join(workspace, ".pi-symphony-run.json")
	record := runRecord{IssueIdentifier: "CAG-1", IssueURL: "https://linear.app/acme/issue/CAG-1/title", Workspace: workspace, Status: "success", PRURL: "https://github.com/acme/repo/pull/1", RuntimeUsage: &usage{TotalTokens: 123, Cost: &usageCost{Total: 0.45}}}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	changed, err := repairArtifact(path)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected repair")
	}
	var repaired runRecord
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &repaired); err != nil {
		t.Fatal(err)
	}
	if repaired.Status != "merged" || repaired.OriginalStatus != "success" || repaired.ManualRepair != "pr_manually_merged" || repaired.RuntimeUsage.TotalTokens != 123 {
		t.Fatalf("unexpected repaired record: %#v", repaired)
	}
}

func TestRepairArtifactMarksClosedPRSuperseded(t *testing.T) {
	old := prStateForURL
	prStateForURL = func(prURL string) (string, bool, error) { return "CLOSED", false, nil }
	t.Cleanup(func() { prStateForURL = old })

	workspace := t.TempDir()
	path := filepath.Join(workspace, ".pi-symphony-run.json")
	record := runRecord{IssueIdentifier: "CAG-2", IssueURL: "https://linear.app/acme/issue/CAG-2/title", Workspace: workspace, Status: "review_failed", PRURL: "https://github.com/acme/repo/pull/2"}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	changed, err := repairArtifact(path)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected repair")
	}
	var repaired runRecord
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &repaired); err != nil {
		t.Fatal(err)
	}
	if repaired.Status != "superseded" || repaired.OriginalStatus != "review_failed" || repaired.ManualRepair != "pr_closed_unmerged" {
		t.Fatalf("unexpected repaired record: %#v", repaired)
	}
}

func TestParseGitHubPRStateUsesSupportedMergedAtField(t *testing.T) {
	state, merged, err := parseGitHubPRState(`{"state":"MERGED","mergedAt":"2026-05-18T12:00:00Z"}`)
	if err != nil {
		t.Fatal(err)
	}
	if state != "MERGED" || !merged {
		t.Fatalf("parseGitHubPRState() = %q, %v", state, merged)
	}

	state, merged, err = parseGitHubPRState(`{"state":"CLOSED","mergedAt":null}`)
	if err != nil {
		t.Fatal(err)
	}
	if state != "CLOSED" || merged {
		t.Fatalf("parseGitHubPRState() = %q, %v", state, merged)
	}
}
