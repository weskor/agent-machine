package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weskor/agent-machine/internal/state"
)

func TestRunSelectedWorkerContextHonorsCanceledContextBeforeOpeningState(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := runSelectedWorkerContext(ctx, linearClient{}, project{}, runnerConfig{ConfigPath: "am.yaml", ProjectSlug: "CAG", WorkspaceRoot: root}, statusWorkerRole)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runSelectedWorkerContext() error = %v; want canceled", err)
	}
	if _, statErr := os.Stat(state.DefaultDBPath(root)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("state db stat error = %v; want no state opened for canceled worker", statErr)
	}
}

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

	if err := runStatusWorkerProcess(linearClient{}, runnerConfig{ConfigPath: "am.yaml", ProjectSlug: "CAG", WorkspaceRoot: root}); err != nil {
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

func TestRunSelectedCleanupWorkerCreatesMissingWorkspaceRoot(t *testing.T) {
	parent := filepath.Join(t.TempDir(), ".am")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "workspaces")
	oldIssueIdentifiers := issueIdentifiersByStateForCleanupWorker
	oldCleanup := cleanupWorkspacesForWorker
	t.Cleanup(func() {
		issueIdentifiersByStateForCleanupWorker = oldIssueIdentifiers
		cleanupWorkspacesForWorker = oldCleanup
	})
	issueIdentifiersByStateForCleanupWorker = func(context.Context, linearClient, string, string) (map[string]bool, error) {
		return map[string]bool{}, nil
	}
	cleanupWorkspacesForWorker = func(ctx context.Context, workspaceRoot string, options cleanupOptions) error {
		if workspaceRoot != root {
			t.Fatalf("workspaceRoot = %q, want %q", workspaceRoot, root)
		}
		info, err := os.Stat(root)
		if err != nil {
			t.Fatalf("workspace root was not created before selected cleanup worker: %v", err)
		}
		if !info.IsDir() {
			t.Fatalf("workspace root exists but is not a directory: %s", root)
		}
		return nil
	}

	if err := runSelectedWorkerContext(context.Background(), linearClient{}, project{}, runnerConfig{ConfigPath: "am.yaml", ProjectSlug: "CAG", WorkspaceRoot: root, DoneState: "Done"}, cleanupWorkerRole); err != nil {
		t.Fatal(err)
	}
}

func TestPlanWorkerProcessClaimsTaskRunsReadOnlyPlanningAndRecordsHeartbeat(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 5, 23, 12, 2, 0, 0, time.UTC)
	oldPrintPlan := printPlanForWorker
	oldStateNow := stateNow
	t.Cleanup(func() {
		printPlanForWorker = oldPrintPlan
		stateNow = oldStateNow
	})
	printCalled := false
	printPlanForWorker = func(client linearClient, config runnerConfig) error {
		printCalled = true
		if config.WorkspaceRoot != root || config.ProjectSlug != "CAG" || len(config.ActiveStates) != 2 {
			t.Fatalf("plan worker config = %+v; want root/project/active states", config)
		}
		return nil
	}
	stateNow = func() time.Time { return now }

	if err := runPlanWorkerProcess(linearClient{}, runnerConfig{ConfigPath: "am.yaml", ProjectSlug: "CAG", WorkspaceRoot: root, ActiveStates: []string{"Ready for Agent", "In Progress"}}); err != nil {
		t.Fatalf("runPlanWorkerProcess() error = %v", err)
	}
	if !printCalled {
		t.Fatal("plan worker did not run planning output")
	}

	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	tasks, err := store.WorkerTasks(context.Background(), planWorkerRole)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].TaskKey != "process:plan" || tasks[0].Status != "completed" || tasks[0].LeaseName != "worker:plan" {
		t.Fatalf("plan worker task = %+v; want completed process:plan with lease", tasks)
	}
	heartbeats, err := store.SnapshotHeartbeats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(heartbeats) != 1 || heartbeats[0].LaneName != "worker:plan" || heartbeats[0].CycleNumber != 1 || heartbeats[0].RecoveryRequired {
		t.Fatalf("heartbeats = %+v; want successful worker:plan heartbeat", heartbeats)
	}
	lease, ok, err := store.Lease(context.Background(), "worker:plan")
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
	issueIdentifiersByStateForCleanupWorker = func(ctx context.Context, client linearClient, projectSlug, stateName string) (map[string]bool, error) {
		if err := ctx.Err(); err != nil {
			t.Fatalf("cleanup issue refresh context error = %v", err)
		}
		if projectSlug != "CAG" || stateName != "Done" {
			t.Fatalf("Done issue refresh = project %q state %q; want CAG/Done", projectSlug, stateName)
		}
		return map[string]bool{"CAG-160": true}, nil
	}
	cleanupCalled := false
	cleanupWorkspacesForWorker = func(ctx context.Context, workspaceRoot string, options cleanupOptions) error {
		cleanupCalled = true
		if err := ctx.Err(); err != nil {
			t.Fatalf("cleanup worker context error = %v", err)
		}
		if workspaceRoot != root || !options.Apply || !options.DoneIssues["CAG-160"] || options.StateStore == nil {
			t.Fatalf("cleanup options = root %q options %+v; want apply with Done issues and state store", workspaceRoot, options)
		}
		return nil
	}

	if err := runCleanupWorkerProcess(linearClient{}, runnerConfig{ConfigPath: "am.yaml", ProjectSlug: "CAG", WorkspaceRoot: root, DoneState: "Done"}); err != nil {
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

func TestMergeWorkerProcessClaimsTaskRunsMergeWithoutCleanupAndRecordsHeartbeat(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 5, 23, 12, 10, 0, 0, time.UTC)
	oldCleanupWorkspaces := cleanupWorkspacesForWorker
	oldMergeApprovedPRs := mergeApprovedPRsForWorker
	oldStateNow := stateNow
	t.Cleanup(func() {
		cleanupWorkspacesForWorker = oldCleanupWorkspaces
		mergeApprovedPRsForWorker = oldMergeApprovedPRs
		stateNow = oldStateNow
	})
	stateNow = func() time.Time { return now }
	var calls []string
	cleanupWorkspacesForWorker = func(ctx context.Context, workspaceRoot string, options cleanupOptions) error {
		t.Fatal("merge worker should not invoke cleanup prepass")
		return nil
	}
	mergeApprovedPRsForWorker = func(ctx context.Context, client linearClient, config runnerConfig, store *state.Store) error {
		calls = append(calls, "merge")
		if err := ctx.Err(); err != nil {
			t.Fatalf("merge worker context error = %v", err)
		}
		if config.WorkspaceRoot != root || config.ProjectSlug != "CAG" || store == nil {
			t.Fatalf("merge config/store = %+v/%v; want root/project with worker state store", config, store)
		}
		return nil
	}

	if err := runMergeWorkerProcess(linearClient{}, runnerConfig{ConfigPath: "am.yaml", ProjectSlug: "CAG", WorkspaceRoot: root, DoneState: "Done"}); err != nil {
		t.Fatalf("runMergeWorkerProcess() error = %v", err)
	}
	if len(calls) != 1 || calls[0] != "merge" {
		t.Fatalf("calls = %#v; want merge only", calls)
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

func TestReconciliationWorkerProcessClaimsTaskRunsScanAndRecordsHeartbeat(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 5, 23, 12, 12, 0, 0, time.UTC)
	oldRunReconciliationScan := runReconciliationScanForWorker
	oldStateNow := stateNow
	t.Cleanup(func() {
		runReconciliationScanForWorker = oldRunReconciliationScan
		stateNow = oldStateNow
	})
	stateNow = func() time.Time { return now }
	scanCalled := false
	runReconciliationScanForWorker = func(ctx context.Context, client linearClient, config runnerConfig, store *state.Store) (bool, error) {
		scanCalled = true
		if err := ctx.Err(); err != nil {
			t.Fatalf("reconciliation worker context error = %v", err)
		}
		if config.WorkspaceRoot != root || config.ProjectSlug != "CAG" || store == nil {
			t.Fatalf("reconciliation config/store = %+v/%v; want root/project with worker state store", config, store)
		}
		return true, nil
	}

	if err := runReconciliationWorkerProcess(linearClient{}, runnerConfig{ConfigPath: "am.yaml", ProjectSlug: "CAG", WorkspaceRoot: root}); err != nil {
		t.Fatalf("runReconciliationWorkerProcess() error = %v", err)
	}
	if !scanCalled {
		t.Fatal("reconciliation worker did not run scan")
	}

	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	tasks, err := store.WorkerTasks(context.Background(), reconciliationWorkerRole)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].TaskKey != "process:reconciliation" || tasks[0].Status != "completed" || tasks[0].LeaseName != "worker:reconciliation" {
		t.Fatalf("reconciliation worker task = %+v; want completed process:reconciliation with lease", tasks)
	}
	heartbeats, err := store.SnapshotHeartbeats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(heartbeats) != 1 || heartbeats[0].LaneName != "worker:reconciliation" || heartbeats[0].CycleNumber != 1 || heartbeats[0].RecoveryRequired {
		t.Fatalf("heartbeats = %+v; want successful worker:reconciliation heartbeat", heartbeats)
	}
	lease, ok, err := store.Lease(context.Background(), "worker:reconciliation")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || lease.ReleasedAt.IsZero() || lease.ReleaseReason != "worker task completed" {
		t.Fatalf("lease = %+v ok=%t; want released worker task lease", lease, ok)
	}
}

func TestSelectedWorkerRejectsUnsupportedRole(t *testing.T) {
	err := runSelectedWorker(linearClient{}, project{}, runnerConfig{}, "not-a-worker")
	if err == nil || !strings.Contains(err.Error(), "unsupported worker role") || !strings.Contains(err.Error(), reconciliationWorkerRole) {
		t.Fatalf("runSelectedWorker() error = %v; want unsupported role listing reconciliation", err)
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
	runReviewReadyAttemptForWorker = func(ctx context.Context, client linearClient, proj project, config runnerConfig, store *state.Store) (bool, error) {
		resumeCalled = true
		if config.WorkspaceRoot != root || config.ProjectSlug != "CAG" || config.ReviewCommand != "pi review" || store == nil {
			t.Fatalf("review worker input = config %+v store=%v; want root/project/review/store", config, store != nil)
		}
		return true, nil
	}

	if err := runReviewWorkerProcess(linearClient{}, project{}, runnerConfig{ConfigPath: "am.yaml", ProjectSlug: "CAG", WorkspaceRoot: root, ReviewCommand: "pi review"}); err != nil {
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
	runImplementationAttemptForWorker = func(ctx context.Context, client linearClient, proj project, config runnerConfig, store *state.Store) (bool, error) {
		implementationCalled = true
		if config.WorkspaceRoot != root || config.ProjectSlug != "CAG" || store == nil {
			t.Fatalf("implementation worker input = config %+v store=%v; want root/project/store", config, store != nil)
		}
		return true, nil
	}

	if err := runImplementationWorkerProcess(linearClient{}, project{}, runnerConfig{ConfigPath: "am.yaml", ProjectSlug: "CAG", WorkspaceRoot: root}); err != nil {
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

func TestHandoffWorkerProcessClaimsTaskRunsPendingHandoffAndRecordsHeartbeat(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 5, 23, 12, 16, 0, 0, time.UTC)
	oldRunHandoffPendingAttempt := runHandoffPendingAttemptForWorker
	oldStateNow := stateNow
	t.Cleanup(func() {
		runHandoffPendingAttemptForWorker = oldRunHandoffPendingAttempt
		stateNow = oldStateNow
	})
	stateNow = func() time.Time { return now }
	handoffCalled := false
	runHandoffPendingAttemptForWorker = func(ctx context.Context, client linearClient, config runnerConfig, store *state.Store) (bool, error) {
		handoffCalled = true
		if config.WorkspaceRoot != root || config.ProjectSlug != "CAG" || config.HandoffState != "Human Review" || store == nil {
			t.Fatalf("handoff worker input = config %+v store=%v; want root/project/handoff/store", config, store != nil)
		}
		return true, nil
	}

	if err := runHandoffWorkerProcess(linearClient{}, runnerConfig{ConfigPath: "am.yaml", ProjectSlug: "CAG", WorkspaceRoot: root, HandoffState: "Human Review"}); err != nil {
		t.Fatalf("runHandoffWorkerProcess() error = %v", err)
	}
	if !handoffCalled {
		t.Fatal("handoff worker did not run pending handoff attempt")
	}

	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	tasks, err := store.WorkerTasks(context.Background(), handoffWorkerRole)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].TaskKey != "process:handoff" || tasks[0].Status != "completed" || tasks[0].LeaseName != "worker:handoff" {
		t.Fatalf("handoff worker task = %+v; want completed process:handoff with lease", tasks)
	}
	heartbeats, err := store.SnapshotHeartbeats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(heartbeats) != 1 || heartbeats[0].LaneName != "worker:handoff" || heartbeats[0].CycleNumber != 1 || heartbeats[0].RecoveryRequired {
		t.Fatalf("heartbeats = %+v; want successful worker:handoff heartbeat", heartbeats)
	}
	lease, ok, err := store.Lease(context.Background(), "worker:handoff")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || lease.ReleasedAt.IsZero() || lease.ReleaseReason != "worker task completed" {
		t.Fatalf("lease = %+v ok=%t; want released worker task lease", lease, ok)
	}
}

func TestLinearStatusWorkerProcessClaimsTaskRunsTransitionIntentAndRecordsHeartbeat(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 5, 23, 12, 17, 0, 0, time.UTC)
	oldRunLinearStatusTransitionTask := runLinearStatusTransitionTaskForWorker
	oldStateNow := stateNow
	t.Cleanup(func() {
		runLinearStatusTransitionTaskForWorker = oldRunLinearStatusTransitionTask
		stateNow = oldStateNow
	})
	stateNow = func() time.Time { return now }
	transitionCalled := false
	runLinearStatusTransitionTaskForWorker = func(ctx context.Context, client linearClient, config runnerConfig, store *state.Store) (bool, error) {
		transitionCalled = true
		if config.WorkspaceRoot != root || config.ProjectSlug != "CAG" || store == nil {
			t.Fatalf("linear-status worker input = config %+v store=%v; want root/project/store", config, store != nil)
		}
		return true, nil
	}

	if err := runLinearStatusWorkerProcess(linearClient{}, runnerConfig{ConfigPath: "am.yaml", ProjectSlug: "CAG", WorkspaceRoot: root}); err != nil {
		t.Fatalf("runLinearStatusWorkerProcess() error = %v", err)
	}
	if !transitionCalled {
		t.Fatal("linear-status worker did not run transition task consumer")
	}

	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	tasks, err := store.WorkerTasks(context.Background(), linearStatusWorkerRole)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].TaskKey != "process:linear-status" || tasks[0].Status != "completed" || tasks[0].LeaseName != "worker:linear-status" {
		t.Fatalf("linear-status worker task = %+v; want completed process:linear-status with lease", tasks)
	}
	heartbeats, err := store.SnapshotHeartbeats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(heartbeats) != 1 || heartbeats[0].LaneName != "worker:linear-status" || heartbeats[0].CycleNumber != 1 || heartbeats[0].RecoveryRequired {
		t.Fatalf("heartbeats = %+v; want successful worker:linear-status heartbeat", heartbeats)
	}
	lease, ok, err := store.Lease(context.Background(), "worker:linear-status")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || lease.ReleasedAt.IsZero() || lease.ReleaseReason != "worker task completed" {
		t.Fatalf("lease = %+v ok=%t; want released worker task lease", lease, ok)
	}
}

func TestWorkWorkerProcessClaimsTaskRunsImplementationBatchAndRecordsHeartbeat(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 5, 23, 12, 15, 0, 0, time.UTC)
	oldRunImplementationAttemptBatch := runImplementationAttemptBatchForWorkWorker
	oldStateNow := stateNow
	t.Cleanup(func() {
		runImplementationAttemptBatchForWorkWorker = oldRunImplementationAttemptBatch
		stateNow = oldStateNow
	})
	stateNow = func() time.Time { return now }
	batchCalled := false
	runImplementationAttemptBatchForWorkWorker = func(ctx context.Context, client linearClient, proj project, config runnerConfig, store *state.Store, capacity int) (bool, error) {
		batchCalled = true
		if config.WorkspaceRoot != root || config.ProjectSlug != "CAG" || store == nil || capacity != 3 {
			t.Fatalf("work batch input = config %+v store=%v capacity=%d; want root/project/store/capacity=3", config, store != nil, capacity)
		}
		return true, nil
	}
	proj := project{YAML: "tracker:\n  project_slug: CAG\nworkspace:\n  root: " + root + "\nagent:\n  max_concurrent_agents: 3"}

	if err := runWorkWorkerProcess(linearClient{}, proj, runnerConfig{ConfigPath: "am.yaml", ProjectSlug: "CAG", WorkspaceRoot: root}); err != nil {
		t.Fatalf("runWorkWorkerProcess() error = %v", err)
	}
	if !batchCalled {
		t.Fatal("work worker did not run implementation batch")
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
