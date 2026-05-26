package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weskor/agent-machine/internal/runledger"
)

func TestRunProgressPathUsesRunnerStateOutsideIssueWorkspace(t *testing.T) {
	repoRoot := t.TempDir()
	workspaceRoot := filepath.Join(repoRoot, ".am", "workspaces")
	path, err := runProgressPath(workspaceRoot, "CAG-119")
	if err != nil {
		t.Fatalf("runProgressPath() error = %v", err)
	}
	want := filepath.Join(repoRoot, ".am", "state", "run-progress", "CAG-119", runProgressArtifactName)
	if path != want {
		t.Fatalf("runProgressPath() = %q, want %q", path, want)
	}
	if strings.Contains(path, filepath.Join("workspaces", "CAG-119")) {
		t.Fatalf("progress path is inside issue workspace: %s", path)
	}
}

func TestWriteReadAndFormatRunProgress(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), ".am", "workspaces")
	started := time.Date(2026, 5, 20, 21, 0, 0, 0, time.UTC)
	snapshot := runProgressSnapshot{
		IssueIdentifier:      "CAG-119",
		IssueTitle:           "Add progress",
		Phase:                "reviewing",
		Status:               "review_failed",
		Outcome:              "review_failed",
		ChecksStatus:         "unknown_post_run",
		ReviewStatus:         "failed",
		ReviewClassification: "behavior_spec_blocker",
		PRURL:                "https://github.com/weskor/agent-machine/pull/119",
		StartedAt:            started,
		UpdatedAt:            started.Add(2 * time.Minute),
		NextAction:           "repair_review_findings_before_handoff",
		RunRecordPath:        filepath.Join(workspaceRoot, "CAG-119", ".am-run.json"),
		EvaluationPath:       filepath.Join(workspaceRoot, "CAG-119", evaluationArtifactName),
	}
	if err := writeRunProgressResult(workspaceRoot, snapshot); err != nil {
		t.Fatalf("writeRunProgressResult() error = %v", err)
	}
	path, _ := runProgressPath(workspaceRoot, "CAG-119")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read progress file: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("progress is not JSON: %v", err)
	}
	read, err := readRunProgress(workspaceRoot, "CAG-119")
	if err != nil {
		t.Fatalf("readRunProgress() error = %v", err)
	}
	formatted := formatRunProgress(read)
	for _, expected := range []string{"issue=CAG-119", "phase=reviewing", "status=review_failed", "checks=unknown_post_run", "review=failed", "classification=behavior_spec_blocker", "next=repair_review_findings_before_handoff", "run_record=", "evaluation=", "progress="} {
		if !strings.Contains(formatted, expected) {
			t.Fatalf("formatted progress missing %q: %s", expected, formatted)
		}
	}
	events, ledgerPath, err := runledger.Read(workspaceRoot, "CAG-119")
	if err != nil {
		t.Fatalf("read run ledger: %v", err)
	}
	if len(events) != 1 || events[0].Phase != "reviewing" || events[0].ReviewClassification != "behavior_spec_blocker" || events[0].ProgressPath != path {
		t.Fatalf("ledger events from progress = %#v", events)
	}
	ledger := runledger.Format("CAG-119", events, ledgerPath)
	for _, expected := range []string{"issue=CAG-119", "events=1", "phase=reviewing", "classification=behavior_spec_blocker", "progress="} {
		if !strings.Contains(ledger, expected) {
			t.Fatalf("formatted ledger missing %q: %s", expected, ledger)
		}
	}
}

func TestRunProgressForRecordSummarizesTerminalOutcome(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "CAG-119")
	record := runRecord{
		IssueIdentifier:      "CAG-119",
		IssueTitle:           "Add progress",
		Workspace:            workspace,
		WorkspaceRoot:        filepath.Dir(workspace),
		Branch:               "am/CAG-119-workspace",
		ExpectedBranch:       "am/CAG-119-workspace",
		StartedAt:            time.Date(2026, 5, 20, 21, 0, 0, 0, time.UTC),
		EndedAt:              time.Date(2026, 5, 20, 21, 5, 0, 0, time.UTC),
		DurationMS:           300000,
		Status:               runAttemptStatusSuccess,
		PRURL:                "https://github.com/weskor/agent-machine/pull/119",
		ReviewStatus:         "passed",
		ReviewClassification: "",
	}
	evaluation := evaluationArtifact{Outcome: "handoff_ready", ChecksStatus: "success", NextAction: "await_approval_and_green_checks"}
	snapshot := runProgressForRecord(workspace, record, evaluation)
	if snapshot.Phase != "completed" || snapshot.Outcome != "handoff_ready" || snapshot.ChecksStatus != "success" || snapshot.NextAction != "await_approval_and_green_checks" {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
}

func TestRunProgressForRecordPreservesReviewNotReadyRetryAction(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "CAG-122")
	record := runRecord{IssueIdentifier: "CAG-122", Workspace: workspace, Status: runAttemptStatusReviewNotReady, PRURL: "https://github.com/weskor/agent-machine/pull/122", Error: "review not ready", StartedAt: time.Date(2026, 5, 20, 21, 0, 0, 0, time.UTC), EndedAt: time.Date(2026, 5, 20, 21, 5, 0, 0, time.UTC), DurationMS: 300000}
	evaluation := evaluationArtifact{Outcome: "waiting_for_checks", ChecksStatus: "waiting_for_checks", NextAction: "wait_for_github_checks_then_retry"}

	snapshot := runProgressForRecord(workspace, record, evaluation)

	if snapshot.Phase != "review_not_ready" || snapshot.NextAction != "wait_for_github_checks_then_retry" || snapshot.Outcome != "waiting_for_checks" {
		t.Fatalf("unexpected review-not-ready snapshot: %#v", snapshot)
	}
}

func TestPrintRunLedgerFallsBackToProgressSnapshot(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), ".am", "workspaces")
	snapshot := runProgressSnapshot{
		IssueIdentifier: "CAG-191",
		Phase:           "completed",
		Status:          runAttemptStatusSuccess,
		PRURL:           "https://github.com/weskor/agent-machine/pull/191",
		StartedAt:       time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC),
		UpdatedAt:       time.Date(2026, 5, 26, 12, 5, 0, 0, time.UTC),
	}
	path, err := runProgressPath(workspaceRoot, "CAG-191")
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	output := captureStdout(t, func() {
		if err := printRunLedger(workspaceRoot, "CAG-191"); err != nil {
			t.Fatalf("printRunLedger() error = %v", err)
		}
	})
	for _, expected := range []string{"issue=CAG-191", "events=1", "phase=completed", "status=success", "pr=https://github.com/weskor/agent-machine/pull/191"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("ledger output missing %q:\n%s", expected, output)
		}
	}
}
