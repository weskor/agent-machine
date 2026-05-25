package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weskor/agent-machine/internal/state"
)

func TestOrchestrationSnapshotEmptyState(t *testing.T) {
	root := t.TempDir()
	snap, err := buildOrchestrationSnapshot(context.Background(), runnerConfig{WorkspaceRoot: root}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Issues) != 0 || len(snap.ActiveLocks) != 0 || len(snap.Artifacts) != 0 {
		t.Fatalf("expected empty snapshot, got %+v", snap)
	}
	if got := snap.SourcePrecedence; len(got) != 3 || got[0] != "active_locks_lanes" || got[1] != "sqlite" || got[2] != "artifacts_fallback" {
		t.Fatalf("unexpected precedence: %#v", got)
	}
}

func TestOrchestrationSnapshotMissingWorkspaceRootDegradesToEmpty(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".symphony", "workspaces")
	snap, err := buildOrchestrationSnapshot(context.Background(), runnerConfig{WorkspaceRoot: root}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Issues) != 0 || len(snap.ActiveLocks) != 0 || len(snap.Artifacts) != 0 {
		t.Fatalf("expected empty snapshot for missing workspace root, got %+v", snap)
	}
	if snap.SQLiteHealth.Exists {
		t.Fatalf("sqlite health exists = true for missing workspace root: %+v", snap.SQLiteHealth)
	}
	if snap.SQLiteHealthError != "" {
		t.Fatalf("sqlite health error = %q, want empty", snap.SQLiteHealthError)
	}
}

func TestOrchestrationSnapshotIncludesRecentEventSummaries(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), ".symphony", "workspaces")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(ctx, state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(ctx, state.EventInput{IssueKey: "CAG-104", Source: "test", Type: state.EventCleanupCompleted, Payload: map[string]any{"deletion_result": "deleted"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	snap, err := buildOrchestrationSnapshot(ctx, runnerConfig{WorkspaceRoot: root}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.RecentEvents) != 1 || snap.RecentEvents[0].Type != state.EventCleanupCompleted || snap.RecentEvents[0].IssueKey != "CAG-104" {
		t.Fatalf("recent events = %+v, want cleanup_completed summary", snap.RecentEvents)
	}
}

func TestOrchestrationSnapshotActiveLockOverridesSQLiteAndArtifact(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	writeSnapshotArtifact(t, root, "CAG-1", runRecord{IssueIdentifier: "CAG-1", Status: "success"})
	writeSnapshotState(t, root, state.RunArtifactSnapshot{IssueKey: "CAG-1", Attempt: 1, Status: "failed", UpdatedAt: now.Add(-time.Hour), TerminalOutcome: "failed"})
	writeSnapshotLock(t, root, "CAG-1", now)
	snap, err := buildOrchestrationSnapshot(context.Background(), runnerConfig{WorkspaceRoot: root}, now)
	if err != nil {
		t.Fatal(err)
	}
	issue := findSnapshotIssue(t, snap, "CAG-1")
	if issue.Source != "active_lock" || issue.Status != "active" {
		t.Fatalf("lock should win, got %+v", issue)
	}
	if len(snap.ActiveLocks) != 1 || !snap.ActiveLocks[0].Active {
		t.Fatalf("expected active lock: %+v", snap.ActiveLocks)
	}
}

func TestOrchestrationSnapshotStaleArtifactDoesNotOverrideSQLite(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC()
	writeSnapshotArtifact(t, root, "CAG-2", runRecord{IssueIdentifier: "CAG-2", Status: "success", PRURL: "https://example.test/old"})
	writeSnapshotState(t, root, state.RunArtifactSnapshot{IssueKey: "CAG-2", Attempt: 1, Status: "running", PRURL: "https://example.test/new", UpdatedAt: now})
	snap, err := buildOrchestrationSnapshot(context.Background(), runnerConfig{WorkspaceRoot: root}, now)
	if err != nil {
		t.Fatal(err)
	}
	issue := findSnapshotIssue(t, snap, "CAG-2")
	if issue.Source != "sqlite" || issue.Status != "running" || issue.PRURL != "https://example.test/new" {
		t.Fatalf("sqlite should win over artifact, got %+v", issue)
	}
}

func TestOrchestrationSnapshotCompletedAndFailedAttempts(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC()
	writeSnapshotState(t, root, state.RunArtifactSnapshot{IssueKey: "CAG-3", Attempt: 1, Status: "success", ReviewStatus: "passed", PRURL: "https://example.test/pr/3", TerminalOutcome: "success", UpdatedAt: now})
	writeSnapshotState(t, root, state.RunArtifactSnapshot{IssueKey: "CAG-4", Attempt: 1, Status: "failed", ReviewStatus: "failed", TerminalOutcome: "failed", UpdatedAt: now})
	snap, err := buildOrchestrationSnapshot(context.Background(), runnerConfig{WorkspaceRoot: root}, now)
	if err != nil {
		t.Fatal(err)
	}
	if issue := findSnapshotIssue(t, snap, "CAG-3"); issue.Outcome != "success" || issue.Review != "passed" {
		t.Fatalf("completed issue missing state: %+v", issue)
	}
	if issue := findSnapshotIssue(t, snap, "CAG-4"); issue.Outcome != "failed" || issue.Status != "failed" {
		t.Fatalf("failed attempt missing state: %+v", issue)
	}
}

func TestOrchestrationSnapshotKeepsBudgetAndReviewFieldsAvailable(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC()
	writeSnapshotState(t, root, state.RunArtifactSnapshot{IssueKey: "CAG-5", Attempt: 1, Status: "review_failed", ReviewStatus: "failed", ReviewClassification: "needs_work", MergeEligible: false, RetryBudgetState: "available", RetryReason: "review_failed", RetryNextState: "retry", UpdatedAt: now})
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rows, err := store.SnapshotAttempts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ReviewStatus != "failed" || rows[0].RetryBudgetState != "available" || rows[0].RetryNextState != "retry" {
		t.Fatalf("budget/review unavailable: %+v", rows)
	}
}

func TestOrchestrationSnapshotIncludesActiveLaneData(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC()
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.UpsertDaemonHeartbeat(context.Background(), state.DaemonHeartbeat{ProcessID: "pid-1", LaneName: "work", WorkflowPath: "symphony.yaml", CycleNumber: 7, LastSuccessAt: now, ActiveTaskKey: "continuous:work", ActiveTaskRole: "implementation", ActiveLeaseName: "lane:work", ActiveTaskStartedAt: now.Add(-time.Minute), UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	snap, err := buildOrchestrationSnapshot(context.Background(), runnerConfig{WorkspaceRoot: root}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.ActiveLanes) != 1 || snap.ActiveLanes[0].Name != "work" || snap.ActiveLanes[0].CycleNumber != 7 || snap.ActiveLanes[0].Source != "sqlite" {
		t.Fatalf("missing lane data: %+v", snap.ActiveLanes)
	}
	lines := strings.Join(summarizeActiveLanes(snap.ActiveLanes), "\n")
	for _, expected := range []string{"SQLite active lanes: total=1", "lane=work", "active_task=continuous:work", "active_role=implementation", "active_lease=lane:work"} {
		if !strings.Contains(lines, expected) {
			t.Fatalf("expected %q in %q", expected, lines)
		}
	}
}

func TestOrchestrationSnapshotIncludesWorkerTasks(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	now := time.Date(2026, 5, 23, 10, 30, 0, 0, time.UTC)
	store, err := state.Open(ctx, state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.UpsertWorkerTask(ctx, state.WorkerTask{TaskKey: "implementation:CAG-152:1", Role: "implementation", IssueKey: "CAG-152", Attempt: 1, Status: "queued", Priority: 8, AvailableAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertWorkerTask(ctx, state.WorkerTask{TaskKey: "review:CAG-151:1", Role: "review", IssueKey: "CAG-151", Attempt: 1, Status: "completed", Priority: 2, AvailableAt: now.Add(-time.Hour), UpdatedAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}

	snap, err := buildOrchestrationSnapshot(ctx, runnerConfig{WorkspaceRoot: root}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.WorkerTasks) != 2 {
		t.Fatalf("worker tasks = %+v; want two", snap.WorkerTasks)
	}
	if snap.WorkerTasks[0].TaskKey != "review:CAG-151:1" || snap.WorkerTasks[1].TaskKey != "implementation:CAG-152:1" {
		t.Fatalf("worker tasks not sorted by recency: %+v", snap.WorkerTasks)
	}
	lines := strings.Join(summarizeWorkerTasks(snap.WorkerTasks), "\n")
	for _, expected := range []string{"SQLite worker tasks: total=2 implementation:queued=1 review:completed=1", "task=review:CAG-151:1", "task=implementation:CAG-152:1"} {
		if !strings.Contains(lines, expected) {
			t.Fatalf("expected %q in %q", expected, lines)
		}
	}
}

func TestOrchestrationSnapshotIncludesWorkerResults(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	now := time.Date(2026, 5, 24, 11, 0, 0, 0, time.UTC)
	store, err := state.Open(ctx, state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.UpsertWorkerTask(ctx, state.WorkerTask{TaskKey: "continuous:implementation", Role: "implementation", Status: "claimed", AvailableAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertWorkerResult(ctx, state.WorkerResult{TaskKey: "continuous:implementation", Role: "implementation", LaneName: "implementation", Status: "failed", Reason: "worker_error", Error: "runtime unavailable", StartedAt: now, FinishedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}

	snap, err := buildOrchestrationSnapshot(ctx, runnerConfig{WorkspaceRoot: root}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.WorkerResults) != 1 || snap.WorkerResults[0].Status != "failed" || snap.WorkerResults[0].Reason != "worker_error" {
		t.Fatalf("worker results = %+v; want failed implementation result", snap.WorkerResults)
	}
	lines := strings.Join(summarizeWorkerResults(snap.WorkerResults), "\n")
	for _, expected := range []string{"SQLite worker results: total=1 implementation:failed=1", "task=continuous:implementation role=implementation lane=implementation status=failed did_work=false reason=worker_error"} {
		if !strings.Contains(lines, expected) {
			t.Fatalf("expected %q in %q", expected, lines)
		}
	}
}

func writeSnapshotState(t *testing.T, root string, snap state.RunArtifactSnapshot) {
	t.Helper()
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.UpsertRunArtifact(context.Background(), snap); err != nil {
		t.Fatal(err)
	}
}

func writeSnapshotArtifact(t *testing.T, root, issue string, record runRecord) {
	t.Helper()
	dir := filepath.Join(root, issue)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(record)
	if err := os.WriteFile(filepath.Join(dir, ".pi-symphony-run.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeSnapshotLock(t *testing.T, root, issue string, at time.Time) {
	t.Helper()
	dir := filepath.Join(root, issue)
	if err := os.MkdirAll(filepath.Dir(runLockPath(dir)), 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(runLock{IssueIdentifier: issue, Workspace: dir, Owner: "test", StartedAt: at, HeartbeatAt: at})
	if err := os.WriteFile(runLockPath(dir), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func findSnapshotIssue(t *testing.T, snap orchestrationSnapshot, issue string) snapshotIssue {
	t.Helper()
	for _, candidate := range snap.Issues {
		if candidate.Issue == issue {
			return candidate
		}
	}
	t.Fatalf("missing issue %s in %+v", issue, snap)
	return snapshotIssue{}
}
