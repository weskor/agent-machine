package cleanupworker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	cleanuppolicy "github.com/weskor/agent-machine/internal/cleanup"
	"github.com/weskor/agent-machine/internal/state"
	"github.com/weskor/agent-machine/internal/workertask"
)

func TestScheduleEnqueuesVisibleWorkspacesOnly(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"CAG-1", ".hidden", "state"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("not a workspace"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := openTestStore(t, root)

	didWork, err := Schedule(context.Background(), Config{WorkspaceRoot: root}, store, pathOnlyDeps())
	if err != nil {
		t.Fatal(err)
	}
	if !didWork {
		t.Fatal("didWork=false; want visible workspace enqueued")
	}
	tasks, err := store.WorkerTasks(context.Background(), workertask.RoleCleanup)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].TaskKey != TaskKey("CAG-1") || tasks[0].Status != state.WorkerTaskStatusQueued {
		t.Fatalf("tasks = %+v; want one queued CAG-1 cleanup task", tasks)
	}
}

func TestRunQueuedDeletesWorkspaceAndRecordsResult(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-2")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	store := openTestStore(t, root)
	if _, enqueued, err := Enqueue(context.Background(), store, "CAG-2", workspace, time.Now().UTC(), pathOnlyDeps()); err != nil || !enqueued {
		t.Fatalf("Enqueue() enqueued=%t err=%v", enqueued, err)
	}
	deps := pathOnlyDeps()
	deps.DoneIssues = func(context.Context, string, string) (map[string]bool, error) {
		return map[string]bool{"CAG-2": true}, nil
	}
	deps.Decision = func(_ context.Context, _, workspace string, _ map[string]bool, _ *state.Store, _ WorkspaceChangeChecker) (cleanuppolicy.Decision, error) {
		return cleanuppolicy.Decision{Delete: true, IssueIdentifier: filepath.Base(workspace), WorkspacePath: workspace, Category: "completed", Reason: "SQLite issue CAG-2 is Done"}, nil
	}

	didWork, err := RunQueued(context.Background(), Config{WorkspaceRoot: root}, store, deps)
	if err != nil {
		t.Fatal(err)
	}
	if !didWork {
		t.Fatal("didWork=false; want queued task processed")
	}
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatalf("workspace still exists after cleanup worker: %v", err)
	}
	results, err := store.WorkerResults(context.Background(), workertask.RoleCleanup)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].TaskKey != TaskKey("CAG-2") || results[0].Status != state.WorkerTaskStatusCompleted || results[0].Reason != "cleanup_deleted" {
		t.Fatalf("results = %+v; want completed cleanup_deleted result", results)
	}
}

func openTestStore(t *testing.T, workspaceRoot string) *state.Store {
	t.Helper()
	store, err := state.Open(context.Background(), state.DefaultDBPath(workspaceRoot))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func pathOnlyDeps() Dependencies {
	return Dependencies{
		SafeWorkspaceRoot: func(root string) (string, error) {
			return filepath.Clean(root), nil
		},
		SafeWorkspacePath: func(root, name string) (string, error) {
			return filepath.Join(root, name), nil
		},
		AssertSafeDeletePath: func(string, string) error {
			return nil
		},
	}
}
