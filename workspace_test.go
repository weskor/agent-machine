package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weskor/agent-machine/internal/state"
)

func TestStateProjectionRunArtifactMatchesMirroringContract(t *testing.T) {
	workspace := t.TempDir()
	record := runRecord{
		IssueIdentifier:      "CAG-67",
		IssueID:              "issue-id",
		Workspace:            workspace,
		Branch:               "am/CAG-67-workspace",
		Status:               "success",
		PRURL:                "https://github.com/weskor/agent-machine/pull/67",
		ReviewStatus:         "passed",
		ReviewClassification: "clean",
		ReviewFindings:       "ship it",
		FeedbackHash:         "feedback-hash",
		StartedAt:            time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC),
		EndedAt:              time.Date(2026, 5, 19, 10, 30, 0, 0, time.UTC),
	}
	evaluation := evaluationArtifact{Outcome: "handoff_ready", RootCause: "none", MergeEligible: true, NextAction: "handoff", FeedbackRetryCount: 2}

	snapshot := stateProjection{}.RunArtifact(workspace, record, evaluation)

	if snapshot.IssueKey != "CAG-67" || snapshot.Attempt != 1 || snapshot.Repository != "weskor/agent-machine" || snapshot.PRNumber != 67 {
		t.Fatalf("unexpected run artifact identity projection: %+v", snapshot)
	}
	if !snapshot.ReviewPassed || !snapshot.MergeEligible || snapshot.TerminalOutcome != "handoff_ready" || snapshot.TerminalReason != "none" {
		t.Fatalf("unexpected run artifact decision projection: %+v", snapshot)
	}
	if snapshot.RunArtifactRef != filepath.Join(workspace, ".am-run.json") || snapshot.EvaluationRef != filepath.Join(workspace, evaluationArtifactName) {
		t.Fatalf("unexpected artifact refs: %+v", snapshot)
	}

	result := stateProjection{}.AttemptResult(workspace, record, evaluation)
	if result.IssueKey != snapshot.IssueKey || result.PRURL != snapshot.PRURL || result.TerminalOutcome != snapshot.TerminalOutcome || result.RetryCount != snapshot.RetryCount {
		t.Fatalf("attempt result diverged from artifact projection: result=%+v snapshot=%+v", result, snapshot)
	}
}

func TestRunArtifactProjectionPersistsFinishedEventWithAttemptState(t *testing.T) {
	ctx := context.Background()
	store, err := state.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	workspace := t.TempDir()
	record := runRecord{IssueIdentifier: "CAG-87", IssueID: "issue-id", Workspace: workspace, Status: runAttemptStatusSuccess, PRURL: "https://github.com/weskor/agent-machine/pull/87", ReviewStatus: "passed", EndedAt: time.Date(2026, 5, 20, 11, 0, 0, 0, time.UTC)}
	evaluation := evaluationArtifact{Outcome: "handoff_ready", MergeEligible: true}
	if err := store.UpsertRunArtifact(ctx, stateProjection{}.RunArtifact(workspace, record, evaluation)); err != nil {
		t.Fatalf("UpsertRunArtifact() error = %v", err)
	}
	events, err := store.Events(ctx, state.EventFilter{IssueKey: "CAG-87", Type: state.EventAttemptFinished, Limit: 10})
	if err != nil {
		t.Fatalf("RecentEvents() error = %v", err)
	}
	if len(events) != 1 || events[0].IssueKey != "CAG-87" || events[0].Type != state.EventAttemptFinished {
		t.Fatalf("events = %+v", events)
	}
	if !strings.Contains(string(events[0].Payload), `"pr_url"`) {
		t.Fatalf("payload missing pr_url: %s", events[0].Payload)
	}
}

func TestStateProjectionCleanupLeaseAndHeartbeat(t *testing.T) {
	now := time.Date(2026, 5, 19, 11, 0, 0, 0, time.UTC)
	projection := stateProjection{}

	cleanup := projection.Cleanup(cleanupResult{IssueIdentifier: "CAG-67", Category: "not-done", Reason: "still active", ArtifactRef: "artifact.json"}, false, "kept", true, now)
	if cleanup.BlockedReason != "still active" || cleanup.DeletionResult != "kept" || cleanup.UpdatedAt != now {
		t.Fatalf("unexpected cleanup projection: %+v", cleanup)
	}

	lock := runLock{IssueIdentifier: "CAG-67", Owner: "agent", Workspace: filepath.Join(t.TempDir(), "CAG-67"), StartedAt: now, HeartbeatAt: now.Add(time.Minute)}
	lease := projection.RunLockLease(lock, now)
	if lease.Name != "run:CAG-67" || lease.Scope != filepath.Dir(lock.Workspace) || lease.Owner != "agent" || !lease.ExpiresAt.Equal(lock.HeartbeatAt.Add(runLockStaleAfter)) {
		t.Fatalf("unexpected lease projection: %+v", lease)
	}

	heartbeat := projection.DaemonHeartbeat("host:123", runnerConfig{ConfigPath: "/repo/am.yaml"}, continuousHeartbeat{LaneName: "merge", CycleNumber: 3, Err: errors.New("boom"), ActiveTaskKey: "continuous:merge", ActiveTaskRole: "merge", ActiveLeaseName: "lane:merge", ActiveTaskStartedAt: now.Add(-time.Minute), At: now})
	if heartbeat.ProcessID != "host:123" || heartbeat.LaneName != "merge" || !heartbeat.RecoveryRequired || heartbeat.LastError != "boom" || heartbeat.UpdatedAt != now || heartbeat.ActiveTaskKey != "continuous:merge" || heartbeat.ActiveLeaseName != "lane:merge" {
		t.Fatalf("unexpected heartbeat projection: %+v", heartbeat)
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

func TestWriteEvaluationArtifactAlongsideRunRecord(t *testing.T) {
	workspace := t.TempDir()
	writeEvaluationArtifact(workspace, testWorkspaceRunRecord(workspace, runAttemptStatusSuccess, "https://github.com/weskor/agent-machine/pull/402", nil))

	data, err := os.ReadFile(filepath.Join(workspace, evaluationArtifactName))
	if err != nil {
		t.Fatal(err)
	}
	var evaluation evaluationArtifact
	if err := json.Unmarshal(data, &evaluation); err != nil {
		t.Fatal(err)
	}
	if evaluation.IssueIdentifier != "CAG-108" {
		t.Fatalf("unexpected evaluation artifact: %s", string(data))
	}
}

func TestWriteRunRecordPersistsSQLiteBeforeArtifactsForTerminalOutcomes(t *testing.T) {
	ctx := context.Background()
	statuses := []struct {
		name   string
		status string
		prURL  string
		review *reviewResult
	}{
		{name: "success", status: runAttemptStatusSuccess, prURL: "https://github.com/acme/repo/pull/1", review: &reviewResult{Status: "passed"}},
		{name: "failure", status: runAttemptStatusFailed, prURL: "https://github.com/acme/repo/pull/2"},
		{name: "review_failed", status: runAttemptStatusReviewFailed, prURL: "https://github.com/acme/repo/pull/3", review: &reviewResult{Status: "failed", Classification: reviewClassificationBehaviorSpecBlocker, Findings: "behavior mismatch"}},
		{name: "needs_info", status: runAttemptStatusNeedsInfo},
		{name: "missing_pr", status: runAttemptStatusFailed, prURL: ""},
	}
	for _, tc := range statuses {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			workspace := filepath.Join(root, "CAG-108")
			if err := os.MkdirAll(workspace, 0o700); err != nil {
				t.Fatal(err)
			}
			store, err := state.Open(ctx, filepath.Join(root, "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()

			record := testWorkspaceRunRecord(workspace, tc.status, tc.prURL, tc.review)
			if err := writeRunRecordWithCommandState(store, workspace, record); err != nil {
				t.Fatalf("writeRunRecordWithCommandState() error = %v", err)
			}
			assertFileExists(t, filepath.Join(workspace, ".am-run.json"))
			assertFileExists(t, filepath.Join(workspace, ".am-evaluation.json"))
			assertSQLiteAttempt(t, store, record.IssueIdentifier, tc.status)
			assertRunRecordEvents(t, store, record, tc.prURL != "", tc.review != nil, tc.status == runAttemptStatusFailed || tc.status == runAttemptStatusReviewFailed)
		})
	}
}

func TestWriteRunRecordDoesNotExportArtifactsWhenSQLiteCommitFails(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-108")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(ctx, filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	err = writeRunRecordWithCommandState(store, workspace, testWorkspaceRunRecord(workspace, runAttemptStatusSuccess, "https://github.com/acme/repo/pull/1", nil))
	if err == nil {
		t.Fatal("expected SQLite commit failure")
	}
	assertFileMissing(t, filepath.Join(workspace, ".am-run.json"))
	assertFileMissing(t, filepath.Join(workspace, ".am-evaluation.json"))
}

func TestWriteRunRecordHonorsCanceledContextBeforeSQLiteAndArtifactWrites(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-108")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(context.Background(), filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = writeRunRecordWithCommandStateContext(ctx, store, workspace, testWorkspaceRunRecord(workspace, runAttemptStatusSuccess, "https://github.com/acme/repo/pull/1", nil))
	if err != context.Canceled {
		t.Fatalf("writeRunRecordWithCommandStateContext() error = %v, want %v", err, context.Canceled)
	}
	assertFileMissing(t, filepath.Join(workspace, ".am-run.json"))
	assertFileMissing(t, filepath.Join(workspace, ".am-evaluation.json"))
	var attempts int
	if err := store.DB().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM issue_attempts WHERE issue_key = 'CAG-108'`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 {
		t.Fatalf("attempt rows = %d, want 0", attempts)
	}
}

func TestWriteRunRecordRecordsExportFailureAfterSQLiteCommit(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	workspaceFile := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(workspaceFile, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(ctx, filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	record := testWorkspaceRunRecord(workspaceFile, runAttemptStatusSuccess, "https://github.com/acme/repo/pull/1", nil)
	err = writeRunRecordWithCommandState(store, workspaceFile, record)
	if err == nil {
		t.Fatal("expected artifact export failure")
	}
	assertSQLiteAttempt(t, store, record.IssueIdentifier, runAttemptStatusSuccess)
	var failures int
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM external_fact_snapshots WHERE fact_key = 'artifact_export_failure'`).Scan(&failures); err != nil {
		t.Fatal(err)
	}
	if failures != 1 {
		t.Fatalf("artifact export failure facts = %d, want 1", failures)
	}
	var artifactRefs int
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM external_fact_snapshots WHERE fact_key IN ('run_record', 'evaluation')`).Scan(&artifactRefs); err != nil {
		t.Fatal(err)
	}
	if artifactRefs != 0 {
		t.Fatalf("artifact refs = %d, want 0 when artifact export failed after attempt result", artifactRefs)
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

func testWorkspaceRunRecord(workspace, status, prURL string, review *reviewResult) runRecord {
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	errorMessage := ""
	if status != runAttemptStatusSuccess {
		errorMessage = "missing PR URL"
	}
	return runAttemptOutcome{StartedAt: now, EndedAt: now.Add(time.Minute), Review: review, PRURL: prURL, Status: status, Error: errorMessage}.Record(&issue{ID: "issue-id", Identifier: "CAG-108", Title: "DB first", URL: "https://linear.app/acme/issue/CAG-108/db-first"}, workspace, "pi")
}

func assertFileExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected %s to exist: %v", path, err)
	}
}

func assertFileMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be missing, stat err=%v", path, err)
	}
}

func assertSQLiteAttempt(t *testing.T, store *state.Store, issueKey, status string) {
	t.Helper()
	var got string
	if err := store.DB().QueryRowContext(context.Background(), `SELECT status FROM issue_attempts WHERE issue_key = ? AND attempt = 1`, issueKey).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != status {
		t.Fatalf("SQLite status = %q, want %q", got, status)
	}
}

func assertRunRecordEvents(t *testing.T, store *state.Store, record runRecord, wantPR, wantReview, wantError bool) {
	t.Helper()
	events, err := store.Events(context.Background(), state.EventFilter{IssueKey: record.IssueIdentifier, Attempt: 1, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]int{}
	for _, event := range events {
		types[event.Type]++
	}
	if types[state.EventAttemptFinished] != 1 {
		t.Fatalf("attempt_finished events = %d, want 1; all=%v", types[state.EventAttemptFinished], types)
	}
	if wantPR && types[state.EventPRDetected] != 1 {
		t.Fatalf("pr_detected events = %d, want 1; all=%v", types[state.EventPRDetected], types)
	}
	if wantReview && types[state.EventReviewCompleted] != 1 {
		t.Fatalf("review_completed events = %d, want 1; all=%v", types[state.EventReviewCompleted], types)
	}
	if wantError && types[state.EventErrorRecorded] != 1 {
		t.Fatalf("error_recorded events = %d, want 1; all=%v", types[state.EventErrorRecorded], types)
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
