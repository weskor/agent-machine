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

func TestCleanupWorkerProcessClaimsTaskRefreshesDoneIssuesAndRecordsHeartbeat(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 5, 23, 12, 5, 0, 0, time.UTC)
	oldCleanupWorkspaces := cleanupWorkspacesForWorker
	oldIssueIdentifiers := issueIdentifiersByStateForCleanupWorker
	oldStateNow := stateNow
	t.Cleanup(func() {
		cleanupWorkspacesForWorker = oldCleanupWorkspaces
		issueIdentifiersByStateForCleanupWorker = oldIssueIdentifiers
		stateNow = oldStateNow
	})
	stateNow = func() time.Time { return now }
	issueIdentifiersByStateForCleanupWorker = func(client linearClient, projectSlug, stateName string) (map[string]bool, error) {
		if projectSlug != "CAG" || stateName != "Done" {
			t.Fatalf("Done issue refresh = project %q state %q; want CAG/Done", projectSlug, stateName)
		}
		return map[string]bool{"CAG-160": true}, nil
	}
	cleanupCalled := false
	cleanupWorkspacesForWorker = func(workspaceRoot string, options cleanupOptions) error {
		cleanupCalled = true
		if workspaceRoot != root || !options.Apply || !options.DoneIssues["CAG-160"] || options.StateStore == nil {
			t.Fatalf("cleanup options = root %q options %+v; want apply with Done issues and state store", workspaceRoot, options)
		}
		return nil
	}

	if err := runCleanupWorkerProcess(linearClient{}, runnerConfig{WorkflowPath: "WORKFLOW.md", ProjectSlug: "CAG", WorkspaceRoot: root, DoneState: "Done"}); err != nil {
		t.Fatalf("runCleanupWorkerProcess() error = %v", err)
	}
	if !cleanupCalled {
		t.Fatal("cleanup worker did not run cleanup")
	}

	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	tasks, err := store.WorkerTasks(context.Background(), cleanupWorkerRole)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].TaskKey != "process:cleanup" || tasks[0].Status != "completed" || tasks[0].LeaseName != "worker:cleanup" {
		t.Fatalf("cleanup worker task = %+v; want completed process:cleanup with lease", tasks)
	}
	heartbeats, err := store.SnapshotHeartbeats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(heartbeats) != 1 || heartbeats[0].LaneName != "worker:cleanup" || heartbeats[0].CycleNumber != 1 || heartbeats[0].RecoveryRequired {
		t.Fatalf("heartbeats = %+v; want successful worker:cleanup heartbeat", heartbeats)
	}
	lease, ok, err := store.Lease(context.Background(), "worker:cleanup")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || lease.ReleasedAt.IsZero() || lease.ReleaseReason != "worker task completed" {
		t.Fatalf("lease = %+v ok=%t; want released worker task lease", lease, ok)
	}
}

func TestMergeWorkerProcessClaimsTaskRunsCleanupThenMergeAndRecordsHeartbeat(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 5, 23, 12, 10, 0, 0, time.UTC)
	oldCleanupWorkspaces := cleanupWorkspacesForWorker
	oldIssueIdentifiers := issueIdentifiersByStateForMergeWorker
	oldMergeApprovedPRs := mergeApprovedPRsForWorker
	oldStateNow := stateNow
	t.Cleanup(func() {
		cleanupWorkspacesForWorker = oldCleanupWorkspaces
		issueIdentifiersByStateForMergeWorker = oldIssueIdentifiers
		mergeApprovedPRsForWorker = oldMergeApprovedPRs
		stateNow = oldStateNow
	})
	stateNow = func() time.Time { return now }
	issueIdentifiersByStateForMergeWorker = func(client linearClient, projectSlug, stateName string) (map[string]bool, error) {
		if projectSlug != "CAG" || stateName != "Done" {
			t.Fatalf("Done issue refresh = project %q state %q; want CAG/Done", projectSlug, stateName)
		}
		return map[string]bool{"CAG-161": true}, nil
	}
	var calls []string
	cleanupWorkspacesForWorker = func(workspaceRoot string, options cleanupOptions) error {
		calls = append(calls, "cleanup")
		if workspaceRoot != root || !options.Apply || !options.DoneIssues["CAG-161"] || options.StateStore == nil {
			t.Fatalf("cleanup options = root %q options %+v; want apply with Done issues and state store", workspaceRoot, options)
		}
		return nil
	}
	mergeApprovedPRsForWorker = func(client linearClient, config runnerConfig) error {
		calls = append(calls, "merge")
		if config.WorkspaceRoot != root || config.ProjectSlug != "CAG" {
			t.Fatalf("merge config = %+v; want root/project", config)
		}
		return nil
	}

	if err := runMergeWorkerProcess(linearClient{}, runnerConfig{WorkflowPath: "WORKFLOW.md", ProjectSlug: "CAG", WorkspaceRoot: root, DoneState: "Done"}); err != nil {
		t.Fatalf("runMergeWorkerProcess() error = %v", err)
	}
	if len(calls) != 2 || calls[0] != "cleanup" || calls[1] != "merge" {
		t.Fatalf("calls = %#v; want cleanup then merge", calls)
	}

	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	tasks, err := store.WorkerTasks(context.Background(), mergeWorkerRole)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].TaskKey != "process:merge" || tasks[0].Status != "completed" || tasks[0].LeaseName != "worker:merge" {
		t.Fatalf("merge worker task = %+v; want completed process:merge with lease", tasks)
	}
	heartbeats, err := store.SnapshotHeartbeats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(heartbeats) != 1 || heartbeats[0].LaneName != "worker:merge" || heartbeats[0].CycleNumber != 1 || heartbeats[0].RecoveryRequired {
		t.Fatalf("heartbeats = %+v; want successful worker:merge heartbeat", heartbeats)
	}
	lease, ok, err := store.Lease(context.Background(), "worker:merge")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || lease.ReleasedAt.IsZero() || lease.ReleaseReason != "worker task completed" {
		t.Fatalf("lease = %+v ok=%t; want released worker task lease", lease, ok)
	}
}

func TestReviewWorkerProcessClaimsTaskRunsReviewResumeAndRecordsHeartbeat(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 5, 23, 12, 12, 0, 0, time.UTC)
	oldRunReviewReadyAttempt := runReviewReadyAttemptForWorker
	oldStateNow := stateNow
	t.Cleanup(func() {
		runReviewReadyAttemptForWorker = oldRunReviewReadyAttempt
		stateNow = oldStateNow
	})
	stateNow = func() time.Time { return now }
	resumeCalled := false
	runReviewReadyAttemptForWorker = func(client linearClient, wf workflow, config runnerConfig, store *state.Store) (bool, error) {
		resumeCalled = true
		if config.WorkspaceRoot != root || config.ProjectSlug != "CAG" || config.ReviewCommand != "pi review" || store == nil {
			t.Fatalf("review worker input = config %+v store=%v; want root/project/review/store", config, store != nil)
		}
		return true, nil
	}

	if err := runReviewWorkerProcess(linearClient{}, workflow{}, runnerConfig{WorkflowPath: "WORKFLOW.md", ProjectSlug: "CAG", WorkspaceRoot: root, ReviewCommand: "pi review"}); err != nil {
		t.Fatalf("runReviewWorkerProcess() error = %v", err)
	}
	if !resumeCalled {
		t.Fatal("review worker did not run review-ready attempt")
	}

	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	tasks, err := store.WorkerTasks(context.Background(), reviewWorkerRole)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].TaskKey != "process:review" || tasks[0].Status != "completed" || tasks[0].LeaseName != "worker:review" {
		t.Fatalf("review worker task = %+v; want completed process:review with lease", tasks)
	}
	heartbeats, err := store.SnapshotHeartbeats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(heartbeats) != 1 || heartbeats[0].LaneName != "worker:review" || heartbeats[0].CycleNumber != 1 || heartbeats[0].RecoveryRequired {
		t.Fatalf("heartbeats = %+v; want successful worker:review heartbeat", heartbeats)
	}
	lease, ok, err := store.Lease(context.Background(), "worker:review")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || lease.ReleasedAt.IsZero() || lease.ReleaseReason != "worker task completed" {
		t.Fatalf("lease = %+v ok=%t; want released worker task lease", lease, ok)
	}
}

func TestImplementationWorkerProcessClaimsTaskRunsFreshAttemptAndRecordsHeartbeat(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 5, 23, 12, 14, 0, 0, time.UTC)
	oldRunImplementationAttempt := runImplementationAttemptForWorker
	oldStateNow := stateNow
	t.Cleanup(func() {
		runImplementationAttemptForWorker = oldRunImplementationAttempt
		stateNow = oldStateNow
	})
	stateNow = func() time.Time { return now }
	implementationCalled := false
	runImplementationAttemptForWorker = func(client linearClient, wf workflow, config runnerConfig, store *state.Store) (bool, error) {
		implementationCalled = true
		if config.WorkspaceRoot != root || config.ProjectSlug != "CAG" || store == nil {
			t.Fatalf("implementation worker input = config %+v store=%v; want root/project/store", config, store != nil)
		}
		return true, nil
	}

	if err := runImplementationWorkerProcess(linearClient{}, workflow{}, runnerConfig{WorkflowPath: "WORKFLOW.md", ProjectSlug: "CAG", WorkspaceRoot: root}); err != nil {
		t.Fatalf("runImplementationWorkerProcess() error = %v", err)
	}
	if !implementationCalled {
		t.Fatal("implementation worker did not run implementation attempt")
	}

	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	tasks, err := store.WorkerTasks(context.Background(), implementationWorkerRole)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].TaskKey != "process:implementation" || tasks[0].Status != "completed" || tasks[0].LeaseName != "worker:implementation" {
		t.Fatalf("implementation worker task = %+v; want completed process:implementation with lease", tasks)
	}
	heartbeats, err := store.SnapshotHeartbeats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(heartbeats) != 1 || heartbeats[0].LaneName != "worker:implementation" || heartbeats[0].CycleNumber != 1 || heartbeats[0].RecoveryRequired {
		t.Fatalf("heartbeats = %+v; want successful worker:implementation heartbeat", heartbeats)
	}
	lease, ok, err := store.Lease(context.Background(), "worker:implementation")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || lease.ReleasedAt.IsZero() || lease.ReleaseReason != "worker task completed" {
		t.Fatalf("lease = %+v ok=%t; want released worker task lease", lease, ok)
	}
}

func TestWorkWorkerProcessClaimsTaskRunsAttemptBatchAndRecordsHeartbeat(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 5, 23, 12, 15, 0, 0, time.UTC)
	oldRunClaimedAttemptBatch := runClaimedAttemptBatchForWorker
	oldStateNow := stateNow
	t.Cleanup(func() {
		runClaimedAttemptBatchForWorker = oldRunClaimedAttemptBatch
		stateNow = oldStateNow
	})
	stateNow = func() time.Time { return now }
	batchCalled := false
	runClaimedAttemptBatchForWorker = func(client linearClient, wf workflow, config runnerConfig, store *state.Store, capacity int) (bool, error) {
		batchCalled = true
		if config.WorkspaceRoot != root || config.ProjectSlug != "CAG" || store == nil || capacity != 3 {
			t.Fatalf("work batch input = config %+v store=%v capacity=%d; want root/project/store/capacity=3", config, store != nil, capacity)
		}
		return true, nil
	}
	wf := workflow{YAML: "tracker:\n  project_slug: CAG\nworkspace:\n  root: " + root + "\nagent:\n  max_concurrent_agents: 3"}

	if err := runWorkWorkerProcess(linearClient{}, wf, runnerConfig{WorkflowPath: "WORKFLOW.md", ProjectSlug: "CAG", WorkspaceRoot: root}); err != nil {
		t.Fatalf("runWorkWorkerProcess() error = %v", err)
	}
	if !batchCalled {
		t.Fatal("work worker did not run attempt batch")
	}

	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	tasks, err := store.WorkerTasks(context.Background(), "scheduler")
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].TaskKey != "process:work" || tasks[0].Status != "completed" || tasks[0].LeaseName != "worker:work" {
		t.Fatalf("work worker task = %+v; want completed process:work with lease", tasks)
	}
	heartbeats, err := store.SnapshotHeartbeats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(heartbeats) != 1 || heartbeats[0].LaneName != "worker:work" || heartbeats[0].CycleNumber != 1 || heartbeats[0].RecoveryRequired {
		t.Fatalf("heartbeats = %+v; want successful worker:work heartbeat", heartbeats)
	}
	lease, ok, err := store.Lease(context.Background(), "worker:work")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || lease.ReleasedAt.IsZero() || lease.ReleaseReason != "worker task completed" {
		t.Fatalf("lease = %+v ok=%t; want released worker task lease", lease, ok)
	}
}
