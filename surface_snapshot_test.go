package main

import (
	"context"
	"encoding/json"
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
	if len(snap.WorkItems) != 2 {
		t.Fatalf("work items = %+v; want run artifact and task-only issue", snap.WorkItems)
	}
	if snap.WorkItems[0].IssueIdentifier != "CAG-201" || snap.WorkItems[0].PriorityBucket != "review_or_merge_blockers" || snap.WorkItems[0].NextAction.Code == "" || snap.WorkItems[0].ExternalState.PRURL == "" {
		t.Fatalf("first work item = %+v; want handoff-ready PR before queued work", snap.WorkItems[0])
	}
	if snap.WorkItems[1].IssueIdentifier != "CAG-202" || snap.WorkItems[1].AMStatus != "queued" || snap.WorkItems[1].LaneRoleHint != "implementation" {
		t.Fatalf("second work item = %+v; want queued task after handoff-ready PR", snap.WorkItems[1])
	}

	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"schema_version", "observed_at", "source_precedence", "work_items", "active_lanes", "worker_tasks", "recent_events"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("encoded snapshot missing key %q: %s", key, string(data))
		}
	}
}
