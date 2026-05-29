package statusreport

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weskor/agent-machine/internal/state"
)

func TestSummarizeStateStoreReportsHealthyDB(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	workspaceRoot := filepath.Join(root, ".am", "workspaces")
	if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(ctx, state.DefaultDBPath(workspaceRoot))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := store.UpsertRunArtifact(ctx, state.RunArtifactSnapshot{IssueKey: "CAG-62", Attempt: 1, BranchName: "am/CAG-62-workspace", BaseBranch: "main", Status: "success", Repository: "weskor/agent-machine", PRNumber: 62, PRURL: "https://github.com/weskor/agent-machine/pull/62", ReviewStatus: "passed", TerminalOutcome: "handoff_ready"}); err != nil {
		t.Fatalf("UpsertRunArtifact() error = %v", err)
	}
	if err := store.UpsertDaemonHeartbeat(ctx, state.DaemonHeartbeat{ProcessID: "host:123", LaneName: "merge", WorkflowPath: "/repo/am.yaml", CycleNumber: 1}); err != nil {
		t.Fatalf("UpsertDaemonHeartbeat() error = %v", err)
	}
	if err := store.UpsertWorkerTask(ctx, state.WorkerTask{TaskKey: "review:CAG-62:1", Role: "review", IssueKey: "CAG-62", Attempt: 1, Status: "queued", Priority: 7}); err != nil {
		t.Fatalf("UpsertWorkerTask() error = %v", err)
	}
	if err := store.UpsertWorkerResult(ctx, state.WorkerResult{TaskKey: "review:CAG-62:1", Role: "review", LaneName: "review", IssueKey: "CAG-62", Attempt: 1, Status: "failed", Reason: "worker_error", Error: "checks unavailable"}); err != nil {
		t.Fatalf("UpsertWorkerResult() error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	joined := strings.Join(SummarizeStateStore(workspaceRoot), "\n")
	for _, expected := range []string{"SQLite state path: " + state.DefaultDBPath(workspaceRoot), "SQLite state health: healthy", "schema_version=8", "journal_mode=wal", "busy_timeout_ms=5000", "issue_attempts=1", "pr_mappings=1", "review_states=1", "terminal_outcomes=1", "daemon_heartbeats=1", "cleanup_states=0", "worker_tasks=1", "worker_results=1", "worker_payload_refs=0", "pr_handoff_intents=0", "events=3", "SQLite active lanes: total=1", "lane=merge", "SQLite worker tasks: total=1 review:queued=1", "SQLite worker results: total=1 review:failed=1", "task=review:CAG-62:1", "role=review", "status=queued", "reason=worker_error"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("expected %q in %q", expected, joined)
		}
	}
}

func TestSummarizeStateStoreReportsRecentEvents(t *testing.T) {
	ctx := context.Background()
	workspaceRoot := filepath.Join(t.TempDir(), ".am", "workspaces")
	store, err := state.Open(ctx, state.DefaultDBPath(workspaceRoot))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(ctx, state.EventInput{IssueKey: "CAG-104", Source: "test", Type: state.EventMergeBlocked, Payload: map[string]any{"reason": "checks"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	joined := strings.Join(SummarizeStateStore(workspaceRoot), "\n")
	for _, expected := range []string{"SQLite recent events:", "merge_blocked", "issue=CAG-104", "source=test"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("expected %q in %q", expected, joined)
		}
	}
}

func TestSummarizeStateStoreReportsMissingDB(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), ".am", "workspaces")
	joined := strings.Join(SummarizeStateStore(workspaceRoot), "\n")
	for _, expected := range []string{"SQLite state path: " + state.DefaultDBPath(workspaceRoot), "SQLite state health: missing", "action=run am start"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("expected %q in %q", expected, joined)
		}
	}
}

func TestSummarizeStateStoreReportsUnopenableDB(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), ".am", "workspaces")
	if err := os.MkdirAll(state.DefaultDBPath(workspaceRoot), 0o755); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(SummarizeStateStore(workspaceRoot), "\n")
	for _, expected := range []string{"SQLite state path: " + state.DefaultDBPath(workspaceRoot), "SQLite state health: degraded", "action=check state DB path and permissions"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("expected %q in %q", expected, joined)
		}
	}
}
