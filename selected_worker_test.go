package main

import (
	"context"
	"testing"
	"time"

	"github.com/weskor/pi-symphony/internal/state"
)

func TestStatusWorkerProcessClaimsTaskRecordsHeartbeatAndReleasesLease(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	oldPrintStatus := printStatusForWorker
	oldStateNow := stateNow
	t.Cleanup(func() {
		printStatusForWorker = oldPrintStatus
		stateNow = oldStateNow
	})
	printCalled := false
	printStatusForWorker = func(linearClient, runnerConfig) error {
		printCalled = true
		return nil
	}
	stateNow = func() time.Time { return now }

	if err := runStatusWorkerProcess(linearClient{}, runnerConfig{WorkflowPath: "WORKFLOW.md", ProjectSlug: "CAG", WorkspaceRoot: root}); err != nil {
		t.Fatalf("runStatusWorkerProcess() error = %v", err)
	}
	if !printCalled {
		t.Fatal("status worker did not run status")
	}

	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	tasks, err := store.WorkerTasks(context.Background(), statusWorkerRole)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].TaskKey != "process:status" || tasks[0].Status != "completed" || tasks[0].LeaseName != "worker:status" {
		t.Fatalf("status worker task = %+v; want completed process:status with lease", tasks)
	}
	heartbeats, err := store.SnapshotHeartbeats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(heartbeats) != 1 || heartbeats[0].LaneName != "worker:status" || heartbeats[0].CycleNumber != 1 || heartbeats[0].RecoveryRequired {
		t.Fatalf("heartbeats = %+v; want successful worker:status heartbeat", heartbeats)
	}
	lease, ok, err := store.Lease(context.Background(), "worker:status")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || lease.ReleasedAt.IsZero() || lease.ReleaseReason != "worker task completed" {
		t.Fatalf("lease = %+v ok=%t; want released worker task lease", lease, ok)
	}
}
