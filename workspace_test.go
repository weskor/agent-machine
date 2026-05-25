package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/weskor/agent-machine/internal/state"
)

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
