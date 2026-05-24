package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunProgressPathUsesRunnerStateOutsideIssueWorkspace(t *testing.T) {
	repoRoot := t.TempDir()
	workspaceRoot := filepath.Join(repoRoot, ".symphony", "workspaces")
	path, err := runProgressPath(workspaceRoot, "CAG-119")
	if err != nil {
		t.Fatalf("runProgressPath() error = %v", err)
	}
	want := filepath.Join(repoRoot, ".symphony", "state", "run-progress", "CAG-119", runProgressArtifactName)
	if path != want {
		t.Fatalf("runProgressPath() = %q, want %q", path, want)
	}
	if strings.Contains(path, filepath.Join("workspaces", "CAG-119")) {
		t.Fatalf("progress path is inside issue workspace: %s", path)
	}
}

func TestWriteReadAndFormatRunProgress(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), ".symphony", "workspaces")
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
		PRURL:                "https://github.com/weskor/pi-symphony/pull/119",
		StartedAt:            started,
		UpdatedAt:            started.Add(2 * time.Minute),
		NextAction:           "repair_review_findings_before_handoff",
		RunRecordPath:        filepath.Join(workspaceRoot, "CAG-119", ".pi-symphony-run.json"),
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
}

func TestRunProgressForRecordSummarizesTerminalOutcome(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "CAG-119")
	record := runRecord{
		IssueIdentifier:      "CAG-119",
		IssueTitle:           "Add progress",
		Workspace:            workspace,
		WorkspaceRoot:        filepath.Dir(workspace),
		Branch:               "symphony/CAG-119-workspace",
		ExpectedBranch:       "symphony/CAG-119-workspace",
		StartedAt:            time.Date(2026, 5, 20, 21, 0, 0, 0, time.UTC),
		EndedAt:              time.Date(2026, 5, 20, 21, 5, 0, 0, time.UTC),
		DurationMS:           300000,
		Status:               runAttemptStatusSuccess,
		PRURL:                "https://github.com/weskor/pi-symphony/pull/119",
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
	record := runRecord{IssueIdentifier: "CAG-122", Workspace: workspace, Status: runAttemptStatusReviewNotReady, PRURL: "https://github.com/weskor/pi-symphony/pull/122", Error: "review not ready", StartedAt: time.Date(2026, 5, 20, 21, 0, 0, 0, time.UTC), EndedAt: time.Date(2026, 5, 20, 21, 5, 0, 0, time.UTC), DurationMS: 300000}
	evaluation := evaluationArtifact{Outcome: "waiting_for_checks", ChecksStatus: "waiting_for_checks", NextAction: "wait_for_github_checks_then_retry"}

	snapshot := runProgressForRecord(workspace, record, evaluation)

	if snapshot.Phase != "review_not_ready" || snapshot.NextAction != "wait_for_github_checks_then_retry" || snapshot.Outcome != "waiting_for_checks" {
		t.Fatalf("unexpected review-not-ready snapshot: %#v", snapshot)
	}
}
