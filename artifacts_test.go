package main

import (
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	artifactio "github.com/weskor/agent-machine/internal/artifacts"
	_ "modernc.org/sqlite"
)

func TestCorrectedPRURLFromReviewFindings(t *testing.T) {
	got := artifactio.CorrectedPRURL(
		"https://github.com/weskor/agent-machine/pull/2",
		"REVIEW_PASS\n\nFindings:\n- Actual CAG-11 branch PR is #400, not prompt-listed #2.",
	)
	want := "https://github.com/weskor/agent-machine/pull/400"
	if got != want {
		t.Fatalf("correctedPRURL() = %q, want %q", got, want)
	}
}

func TestCorrectedPRURLNoFinding(t *testing.T) {
	if got := artifactio.CorrectedPRURL("https://github.com/example/repo/pull/2", "REVIEW_PASS"); got != "" {
		t.Fatalf("correctedPRURL() = %q, want empty", got)
	}
}

func TestWriteRunRecordPersistsBudgetTerminalStatus(t *testing.T) {
	workspace := t.TempDir()
	now := time.Now()
	record := runRecordFor(&issue{ID: "issue-id", Identifier: "CAG-1", Title: "title"}, workspace, "pi", "", now, now, nil, nil, "", "timeout", "command timed out", (&runBudget{RuntimeText: "1s", RuntimeTimeout: time.Second}).Active(), "command timed out")
	writeRunRecord(workspace, record)

	data, err := os.ReadFile(filepath.Join(workspace, ".am-run.json"))
	if err != nil {
		t.Fatal(err)
	}
	var persisted runRecord
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Status != "timeout" || persisted.BudgetExceeded == "" || persisted.Budget == nil || persisted.Budget.RuntimeText != "1s" || persisted.Budget.PiText != "1s" {
		t.Fatalf("unexpected persisted run record: %#v", persisted)
	}
	if !terminalRunStatus(persisted.Status) {
		t.Fatalf("expected timeout to be terminal")
	}
}

func TestWriteRunRecordLogsConciseFinalSummary(t *testing.T) {
	workspace := t.TempDir()
	record := runRecord{
		IssueIdentifier: "CAG-86",
		Workspace:       workspace,
		WorkspaceRoot:   workspace,
		Status:          "success",
		PRURL:           "https://github.com/weskor/agent-machine/pull/25",
		ReviewStatus:    "passed",
		DurationMS:      1234,
	}

	stdout := captureStdout(t, func() {
		writeRunRecord(workspace, record)
	})

	for _, expected := range []string{"run summary:", "issue=CAG-86", "status=success", "pr=https://github.com/weskor/agent-machine/pull/25", "review=passed", "duration_ms=1234", ".am-run.json", ".am-evaluation.json"} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("expected %q in concise run summary %q", expected, stdout)
		}
	}
}

func TestWriteRunRecordMirrorsSQLiteStateIdempotently(t *testing.T) {
	root := t.TempDir()
	workspaceRoot := filepath.Join(root, ".am", "workspaces")
	workspace := filepath.Join(workspaceRoot, "CAG-61")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "am.yaml"), []byte("workspace:\n  base_branch: integration\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "am.agent.md"), []byte("# Test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	record := runRecord{
		IssueIdentifier:      "CAG-61",
		IssueID:              "issue-id",
		IssueTitle:           "Mirror artifacts",
		Workspace:            workspace,
		WorkspaceRoot:        workspaceRoot,
		Branch:               "am/CAG-61-workspace",
		ExpectedBranch:       "am/CAG-61-workspace",
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

	for _, name := range []string{".am-run.json", evaluationArtifactName} {
		if _, err := os.Stat(filepath.Join(workspace, name)); err != nil {
			t.Fatalf("expected artifact %s: %v", name, err)
		}
	}
	db, err := sql.Open("sqlite", filepath.Join(root, ".am", "state", "am.db"))
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

func TestCompatibilityArtifactReadersRejectUnsupportedSchemaVersion(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, ".am-run.json"), []byte(`{"schema_version":99,"issue_identifier":"CAG-201","status":"success"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if record, ok := readRunArtifact(workspace); ok {
		t.Fatalf("readRunArtifact() = %+v, true; want unsupported schema rejected", record)
	}
	if err := os.WriteFile(filepath.Join(workspace, evaluationArtifactName), []byte(`{"schema_version":99,"issue_identifier":"CAG-201","outcome":"handoff_ready"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if evaluation, ok := readEvaluationArtifact(workspace); ok {
		t.Fatalf("readEvaluationArtifact() = %+v, true; want unsupported schema rejected", evaluation)
	}
}

func TestWriteRunRecordWithoutWorkspaceRootSkipsSQLiteMirror(t *testing.T) {
	workspace := t.TempDir()
	record := runRecord{IssueIdentifier: "CAG-legacy", Workspace: workspace, Status: "success", StartedAt: time.Now(), EndedAt: time.Now()}
	writeRunRecord(workspace, record)
	if _, err := os.Stat(filepath.Join(workspace, ".am-run.json")); err != nil {
		t.Fatalf("expected run artifact: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, evaluationArtifactName)); err != nil {
		t.Fatalf("expected evaluation artifact: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "state", "am.db")); !os.IsNotExist(err) {
		t.Fatalf("unexpected sqlite state for legacy helper path: %v", err)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stdout: %v", err)
	}
	os.Stdout = w
	fn()
	if err := w.Close(); err != nil {
		t.Fatalf("close stdout pipe: %v", err)
	}
	os.Stdout = old
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read stdout pipe: %v", err)
	}
	return string(data)
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
	path := filepath.Join(workspace, ".am-run.json")
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
	path := filepath.Join(workspace, ".am-run.json")
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
