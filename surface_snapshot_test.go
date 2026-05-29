package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/weskor/agent-machine/internal/state"
)

func TestSurfaceSnapshotBuildsReadOnlyControlPlaneJSON(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	now := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)
	writeSnapshotState(t, root, state.RunArtifactSnapshot{IssueKey: "CAG-201", Attempt: 1, Status: "success", ReviewStatus: "passed", PRURL: "https://example.test/pr/201", TerminalOutcome: "handoff_ready", UpdatedAt: now})
	store, err := state.Open(ctx, state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertWorkerTask(ctx, state.WorkerTask{TaskKey: "implementation:CAG-202:1", Role: "implementation", IssueKey: "CAG-202", Attempt: 1, Status: "queued", AvailableAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	snap, err := buildSurfaceSnapshot(ctx, runnerConfig{ConfigPath: filepath.Join(root, "am.yaml"), ProjectSlug: "CAG", WorkspaceRoot: root}, now)
	if err != nil {
		t.Fatal(err)
	}
	if snap.SchemaVersion != 1 || snap.ProjectSlug != "CAG" || snap.WorkspaceRoot != root {
		t.Fatalf("snapshot identity = %+v", snap)
	}
	if !snap.SQLite.OK || !snap.SQLite.Exists || snap.SQLite.Counts.IssueAttempts != 1 || snap.SQLite.Counts.WorkerTasks != 1 {
		t.Fatalf("sqlite health/counts = %+v", snap.SQLite)
	}
	if len(snap.Issues) != 1 || snap.Issues[0].Issue != "CAG-201" || snap.Issues[0].Source != "sqlite" || snap.Issues[0].PRURL == "" {
		t.Fatalf("issues = %+v", snap.Issues)
	}
	if len(snap.WorkerTasks) != 1 || snap.WorkerTasks[0].TaskKey != "implementation:CAG-202:1" {
		t.Fatalf("worker tasks = %+v", snap.WorkerTasks)
	}

	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"schema_version", "observed_at", "source_precedence", "work_items", "issue_queue", "active_lanes", "worker_tasks", "recent_events"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("encoded snapshot missing key %q: %s", key, string(data))
		}
	}
}

func TestSurfaceSnapshotExposesPrioritizedIssueQueueWithEvidence(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	now := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)
	writeSnapshotState(t, root, state.RunArtifactSnapshot{IssueKey: "CAG-100", Attempt: 1, Status: "success", ReviewStatus: "passed", PRURL: "https://example.test/pr/100", TerminalOutcome: "handoff_ready", MergeEligible: true, UpdatedAt: now.Add(-time.Hour)})
	writeSnapshotState(t, root, state.RunArtifactSnapshot{IssueKey: "CAG-300", Attempt: 1, Status: "running", UpdatedAt: now.Add(-2 * time.Hour)})
	writeSnapshotArtifact(t, root, "CAG-300", runRecord{IssueIdentifier: "CAG-300", Status: "review_failed", ReviewStatus: "failed", PRURL: "https://example.test/pr/300"})
	writeSnapshotEvaluation(t, root, "CAG-300", evaluationArtifact{
		IssueIdentifier:           "CAG-300",
		PRURL:                     "https://example.test/pr/300",
		Outcome:                   "review_failed",
		ReviewStatus:              "failed",
		ReviewClassification:      "behavior_spec_blocker",
		RootCause:                 "out_of_scope_diff",
		NextAction:                "repair_review_findings_before_handoff",
		BlockedBy:                 []string{"review_failed", "merge_blocked"},
		OperatorAttentionRequired: true,
		MergeBlockReason:          "review did not pass",
		ChecksStatus:              "success",
	})
	store, err := state.Open(ctx, state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertWorkerTask(ctx, state.WorkerTask{TaskKey: "implementation:CAG-300:1", Role: "implementation", IssueKey: "CAG-300", Attempt: 1, Status: "reconciliation_needed", Priority: 1, LeaseName: "lane:work", UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertDaemonHeartbeat(ctx, state.DaemonHeartbeat{ProcessID: "pid-1", LaneName: "implementation", WorkflowPath: "am.yaml", CycleNumber: 1, LastSuccessAt: now, ActiveTaskKey: "implementation:CAG-999:1", ActiveTaskRole: "implementation", ActiveLeaseName: "lane:implementation", ActiveTaskStartedAt: now.Add(-time.Minute), UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	snap, err := buildSurfaceSnapshot(ctx, runnerConfig{ConfigPath: filepath.Join(root, "am.yaml"), ProjectSlug: "CAG", WorkspaceRoot: root}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.IssueQueue) != 2 || len(snap.WorkItems) != len(snap.IssueQueue) {
		t.Fatalf("issue queue/work items = %+v / %+v", snap.IssueQueue, snap.WorkItems)
	}
	first := snap.IssueQueue[0]
	if first.IssueIdentifier != "CAG-300" {
		t.Fatalf("first queue item = %+v; want CAG-300 before alphabetically earlier done issue", first)
	}
	if first.AMStatus != "reconciliation_needed" || first.PriorityBucket != "reconciliation_needed" || first.Attention != "reconciliation_needed" {
		t.Fatalf("first queue status/priority/attention = %+v", first)
	}
	if first.NextAction != "repair_review_findings_before_handoff" || first.BlockerReason != "review did not pass" {
		t.Fatalf("first queue next/blocker = %+v", first)
	}
	if first.LaneRoleHint != "implementation" || first.CurrentActivity.Task != "implementation:CAG-300:1" || first.ExternalState.PR != "https://example.test/pr/300" {
		t.Fatalf("first queue evidence = %+v", first)
	}
	if first.CurrentActivity.Lane == "implementation" {
		t.Fatalf("first queue lane = %+v; unrelated same-role active lane should not stamp issue evidence", first.CurrentActivity)
	}
	if snap.IssueQueue[1].IssueIdentifier != "CAG-100" || snap.IssueQueue[1].AMStatus == "mergeable" || snap.IssueQueue[1].NextAction == "merge_pr" {
		t.Fatalf("second queue item = %+v; artifact merge eligibility should not become current mergeable state", snap.IssueQueue[1])
	}
}

func writeSnapshotEvaluation(t *testing.T, root, issue string, evaluation evaluationArtifact) {
	t.Helper()
	dir := filepath.Join(root, issue)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(evaluation)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, evaluationArtifactName), data, 0o644); err != nil {
		t.Fatal(err)
	}
}
