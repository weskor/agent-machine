package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/weskor/agent-machine/internal/state"
)

func TestContinuousSchedulerRunsMergeLaneWhileWorkLaneIsBusy(t *testing.T) {
	workStarted := make(chan struct{})
	allowWorkDone := make(chan struct{})
	mergeRan := make(chan struct{}, 1)
	var workStartedCount atomic.Int32

	scheduler := continuousScheduler{
		maxCycles: 1,
		lanes: []continuousLane{
			{
				name: "merge",
				run: func() (bool, error) {
					mergeRan <- struct{}{}
					return true, nil
				},
			},
			{
				name: "work",
				run: func() (bool, error) {
					if workStartedCount.Add(1) == 1 {
						close(workStarted)
					}
					<-allowWorkDone
					return true, nil
				},
			},
		},
	}

	done := make(chan error, 1)
	go func() { done <- scheduler.run(context.Background()) }()

	select {
	case <-workStarted:
	case <-time.After(time.Second):
		t.Fatal("work lane did not start")
	}
	select {
	case <-mergeRan:
	case <-time.After(time.Second):
		t.Fatal("merge lane did not run while work lane was busy")
	}
	close(allowWorkDone)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop after max cycles")
	}
}

func TestContinuousSchedulerRecordsLaneHeartbeats(t *testing.T) {
	var mu sync.Mutex
	var heartbeats []continuousHeartbeat
	scheduler := continuousScheduler{
		maxCycles: 1,
		recordHeartbeat: func(heartbeat continuousHeartbeat) {
			mu.Lock()
			defer mu.Unlock()
			heartbeats = append(heartbeats, heartbeat)
		},
		lanes: []continuousLane{
			{name: "merge", run: func() (bool, error) { return true, nil }},
			{name: "work", run: func() (bool, error) { return false, nil }},
		},
	}

	if err := scheduler.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	seen := map[string]bool{}
	for _, heartbeat := range heartbeats {
		if heartbeat.CycleNumber == 1 && heartbeat.Success && heartbeat.Err == nil {
			seen[heartbeat.LaneName] = true
		}
	}
	if !seen["merge"] || !seen["work"] {
		t.Fatalf("heartbeats = %+v; want successful merge and work lane heartbeats", heartbeats)
	}
}

func TestContinuousLanesSplitCleanupMergeReviewAndImplementationWork(t *testing.T) {
	root := t.TempDir()
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	oldIssueIdentifiers := issueIdentifiersByStateForContinuousCleanup
	oldRunCleanupWorkerTask := runCleanupWorkerTaskForContinuous
	oldRunMergeWorkerTask := runMergeWorkerTaskForContinuous
	oldRunHandoffPendingAttempt := runHandoffPendingAttemptForContinuous
	oldRunReviewReadyAttempt := runReviewReadyAttemptForWorker
	oldScheduleWorkerTasks := scheduleWorkerTasksForContinuous
	oldRunImplementationBatch := runImplementationAttemptBatchForContinuous
	t.Cleanup(func() {
		issueIdentifiersByStateForContinuousCleanup = oldIssueIdentifiers
		runCleanupWorkerTaskForContinuous = oldRunCleanupWorkerTask
		runMergeWorkerTaskForContinuous = oldRunMergeWorkerTask
		runHandoffPendingAttemptForContinuous = oldRunHandoffPendingAttempt
		runReviewReadyAttemptForWorker = oldRunReviewReadyAttempt
		scheduleWorkerTasksForContinuous = oldScheduleWorkerTasks
		runImplementationAttemptBatchForContinuous = oldRunImplementationBatch
	})
	var calls []string
	scheduleWorkerTasksForContinuous = func(ctx context.Context, client linearClient, config runnerConfig, store *state.Store, capacity int) (bool, error) {
		calls = append(calls, "scheduler")
		if config.WorkspaceRoot != root || config.ProjectSlug != "CAG" || store == nil || capacity != 4 {
			t.Fatalf("scheduler lane input = config %+v store=%v capacity=%d; want root/project with shared store capacity=4", config, store, capacity)
		}
		return true, nil
	}
	runCleanupWorkerTaskForContinuous = func(ctx context.Context, client linearClient, config runnerConfig, store *state.Store) (bool, error) {
		calls = append(calls, "cleanup")
		if config.WorkspaceRoot != root || config.ProjectSlug != "CAG" || store == nil {
			t.Fatalf("cleanup lane input = config %+v store=%v; want root/project with shared store", config, store)
		}
		return true, nil
	}
	runMergeWorkerTaskForContinuous = func(ctx context.Context, client linearClient, config runnerConfig, store *state.Store) (bool, error) {
		calls = append(calls, "merge")
		if config.WorkspaceRoot != root || config.ProjectSlug != "CAG" || store == nil {
			t.Fatalf("merge lane input = config %+v store=%v; want root/project with shared store", config, store)
		}
		return true, nil
	}
	runHandoffPendingAttemptForContinuous = func(ctx context.Context, client linearClient, config runnerConfig, store *state.Store) (bool, error) {
		calls = append(calls, "handoff")
		if config.WorkspaceRoot != root || config.ProjectSlug != "CAG" || store == nil {
			t.Fatalf("handoff lane input = config %+v store=%v; want root/project with shared store", config, store)
		}
		return true, nil
	}
	runReviewReadyAttemptForWorker = func(ctx context.Context, client linearClient, proj project, config runnerConfig, store *state.Store) (bool, error) {
		calls = append(calls, "review")
		if config.WorkspaceRoot != root || store == nil {
			t.Fatalf("review lane input = config %+v store=%v; want root with shared store", config, store)
		}
		return true, nil
	}
	runImplementationAttemptBatchForContinuous = func(ctx context.Context, client linearClient, proj project, config runnerConfig, store *state.Store, capacity int) (bool, error) {
		calls = append(calls, "implementation")
		if config.WorkspaceRoot != root || store == nil || capacity != 4 {
			t.Fatalf("implementation lane input = config %+v store=%v capacity=%d; want root with shared store capacity=4", config, store, capacity)
		}
		return true, nil
	}

	lanes := continuousLanes(context.Background(), linearClient{}, project{}, runnerConfig{ProjectSlug: "CAG", WorkspaceRoot: root, DoneState: "Done", ReviewCommand: "pi review"}, store, 4, nil)
	if len(lanes) != 6 || lanes[0].name != "scheduler" || lanes[1].name != "cleanup" || lanes[2].name != "merge" || lanes[3].name != "handoff" || lanes[4].name != "review" || lanes[5].name != "implementation" {
		t.Fatalf("lanes = %+v; want scheduler, cleanup, merge, handoff, review, implementation", lanes)
	}
	if _, err := lanes[0].run(); err != nil {
		t.Fatal(err)
	}
	if _, err := lanes[1].run(); err != nil {
		t.Fatal(err)
	}
	if _, err := lanes[2].run(); err != nil {
		t.Fatal(err)
	}
	if _, err := lanes[3].run(); err != nil {
		t.Fatal(err)
	}
	if _, err := lanes[4].run(); err != nil {
		t.Fatal(err)
	}
	if _, err := lanes[5].run(); err != nil {
		t.Fatal(err)
	}
	want := []string{"scheduler", "cleanup", "merge", "handoff", "review", "implementation"}
	if !equalStrings(calls, want) {
		t.Fatalf("calls = %#v; want %v", calls, want)
	}
}

func TestRunContinuousRequiresStateStore(t *testing.T) {
	err := runContinuous(linearClient{}, project{}, runnerConfig{}, 1)
	if err == nil || !strings.Contains(err.Error(), "SQLite state store unavailable for continuous mode") {
		t.Fatalf("error = %v; want SQLite state store unavailable", err)
	}
}

func TestScheduleContinuousWorkerTasksRecoversStaleClaimBeforeScheduling(t *testing.T) {
	ctx := context.Background()
	store, err := state.Open(ctx, state.DefaultDBPath(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC().Add(-staleWorkerTaskAfter - time.Minute)
	if err := store.UpsertWorkerTask(ctx, state.WorkerTask{TaskKey: "implementation:CAG-240:1", Role: implementationWorkerRole, IssueKey: "CAG-240", Attempt: 1, Status: state.WorkerTaskStatusClaimed, AvailableAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}

	didWork, err := scheduleContinuousWorkerTasks(ctx, linearClient{}, runnerConfig{WorkspaceRoot: t.TempDir()}, store, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !didWork {
		t.Fatal("didWork = false; want stale task recovery work")
	}
	task, ok, err := store.WorkerTask(ctx, "implementation:CAG-240:1")
	if err != nil || !ok {
		t.Fatalf("WorkerTask() ok=%v err=%v", ok, err)
	}
	if task.Status != state.WorkerTaskStatusReconciliationNeeded {
		t.Fatalf("task status = %q, want reconciliation_needed", task.Status)
	}
}

func TestContinuousWorkerTaskWrapsLaneWithClaimAndCompletion(t *testing.T) {
	ctx := context.Background()
	store, err := state.Open(ctx, state.DefaultDBPath(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ran := false
	didWork, err := runContinuousWorkerTask(ctx, store, continuousWorkerTask{TaskKey: "continuous:merge", Role: "merge", LaneName: "merge", LeaseName: "lane:merge"}, func(context.Context) (bool, error) {
		ran = true
		tasks, err := store.WorkerTasks(ctx, "merge")
		if err != nil {
			t.Fatal(err)
		}
		if len(tasks) != 1 || tasks[0].Status != "claimed" {
			t.Fatalf("worker task during run = %+v; want claimed", tasks)
		}
		lease, ok, err := store.Lease(ctx, "lane:merge")
		if err != nil {
			t.Fatal(err)
		}
		if !ok || !lease.ReleasedAt.IsZero() {
			t.Fatalf("lease during run = %+v ok=%t; want held lane:merge lease", lease, ok)
		}
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ran || !didWork {
		t.Fatalf("ran=%v didWork=%v; want true true", ran, didWork)
	}
	tasks, err := store.WorkerTasks(ctx, "merge")
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Status != "completed" {
		t.Fatalf("worker task after run = %+v; want completed", tasks)
	}
	results, err := store.WorkerResults(ctx, "merge")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Status != "completed" || !results[0].DidWork || results[0].Reason != "work_completed" {
		t.Fatalf("worker result after run = %+v; want completed did_work result", results)
	}
	lease, ok, err := store.Lease(ctx, "lane:merge")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || lease.ReleasedAt.IsZero() {
		t.Fatalf("lease after run = %+v ok=%t; want released lane:merge lease", lease, ok)
	}
	events, err := store.Events(ctx, state.EventFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	got := eventTypes(events)
	want := []string{state.EventWorkerTaskQueued, state.EventWorkerTaskClaimed, state.EventWorkerTaskCompleted}
	if !equalStrings(got, want) {
		t.Fatalf("events = %v; want %v", got, want)
	}
}

func TestContinuousWorkerTaskRenewsLeaseAndActiveHeartbeatWhileRunning(t *testing.T) {
	ctx := context.Background()
	store, err := state.Open(ctx, state.DefaultDBPath(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	oldRenewInterval := workerTaskLeaseRenewInterval
	workerTaskLeaseRenewInterval = 5 * time.Millisecond
	defer func() { workerTaskLeaseRenewInterval = oldRenewInterval }()

	recordHeartbeat := daemonHeartbeatRecorder(ctx, runnerConfig{ConfigPath: "/repo/am.yaml"}, store)
	didWork, err := runContinuousWorkerTask(ctx, store, continuousWorkerTask{
		TaskKey:         "continuous:implementation",
		Role:            implementationWorkerRole,
		LaneName:        "implementation",
		LeaseName:       "lane:implementation",
		RecordHeartbeat: recordHeartbeat,
	}, func(context.Context) (bool, error) {
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			lease, ok, err := store.Lease(ctx, "lane:implementation")
			if err != nil {
				return false, err
			}
			tasks, err := store.WorkerTasks(ctx, implementationWorkerRole)
			if err != nil {
				return false, err
			}
			heartbeats, err := store.SnapshotHeartbeats(ctx)
			if err != nil {
				return false, err
			}
			if ok && lease.RenewedAt.After(lease.AcquiredAt) && len(tasks) == 1 && tasks[0].Status == state.WorkerTaskStatusClaimed && tasks[0].UpdatedAt.After(lease.AcquiredAt) && len(heartbeats) == 1 && heartbeats[0].ActiveTaskKey == "continuous:implementation" && heartbeats[0].ActiveTaskRole == implementationWorkerRole && heartbeats[0].ActiveLeaseName == "lane:implementation" {
				return true, nil
			}
			time.Sleep(time.Millisecond)
		}
		return false, fmt.Errorf("timed out waiting for worker task supervision renewal")
	})
	if err != nil {
		t.Fatal(err)
	}
	if !didWork {
		t.Fatal("didWork = false; want true")
	}
	heartbeats, err := store.SnapshotHeartbeats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(heartbeats) != 1 || heartbeats[0].ActiveTaskKey != "" || heartbeats[0].ActiveLeaseName != "" {
		t.Fatalf("heartbeat after completion = %+v; want active task cleared", heartbeats)
	}
	lease, ok, err := store.Lease(ctx, "lane:implementation")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || lease.ReleasedAt.IsZero() || !lease.RenewedAt.After(lease.AcquiredAt) {
		t.Fatalf("lease after completion = %+v ok=%t; want renewed then released", lease, ok)
	}
}

func TestContinuousWorkerTaskFailsWhenSupervisionCannotRenewLease(t *testing.T) {
	ctx := context.Background()
	store, err := state.Open(ctx, state.DefaultDBPath(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	oldRenewInterval := workerTaskLeaseRenewInterval
	workerTaskLeaseRenewInterval = 5 * time.Millisecond
	defer func() { workerTaskLeaseRenewInterval = oldRenewInterval }()

	didWork, err := runContinuousWorkerTask(ctx, store, continuousWorkerTask{
		TaskKey:   "continuous:merge",
		Role:      mergeWorkerRole,
		LaneName:  "merge",
		LeaseName: "lane:merge",
	}, func(context.Context) (bool, error) {
		if err := store.ReleaseLease(ctx, "lane:merge", time.Now().UTC(), "test forced release"); err != nil {
			return false, err
		}
		time.Sleep(25 * time.Millisecond)
		return true, nil
	})
	if err == nil || !strings.Contains(err.Error(), "renew lease") {
		t.Fatalf("error = %v; want renewal failure", err)
	}
	if !didWork {
		t.Fatal("didWork = false; want true run result preserved")
	}
	results, err := store.WorkerResults(ctx, mergeWorkerRole)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Status != state.WorkerTaskStatusFailed || results[0].Reason != "worker_supervision_error" {
		t.Fatalf("worker result = %+v; want failed worker_supervision_error", results)
	}
}

func TestContinuousWorkerTaskCancelsRunContextWhenSupervisionFails(t *testing.T) {
	ctx := context.Background()
	store, err := state.Open(ctx, state.DefaultDBPath(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	oldRenewInterval := workerTaskLeaseRenewInterval
	workerTaskLeaseRenewInterval = 5 * time.Millisecond
	defer func() { workerTaskLeaseRenewInterval = oldRenewInterval }()

	startedAt := time.Now()
	didWork, err := runContinuousWorkerTask(ctx, store, continuousWorkerTask{
		TaskKey:   "continuous:review",
		Role:      reviewWorkerRole,
		LaneName:  "review",
		LeaseName: "lane:review",
	}, func(runCtx context.Context) (bool, error) {
		if err := store.ReleaseLease(ctx, "lane:review", time.Now().UTC(), "test forced release"); err != nil {
			return false, err
		}
		select {
		case <-runCtx.Done():
			return false, runCtx.Err()
		case <-time.After(time.Second):
			return true, nil
		}
	})
	if err == nil || !strings.Contains(err.Error(), "renew lease") {
		t.Fatalf("error = %v; want renewal failure", err)
	}
	if didWork {
		t.Fatal("didWork = true; want canceled task result")
	}
	if elapsed := time.Since(startedAt); elapsed > 500*time.Millisecond {
		t.Fatalf("elapsed = %s; want prompt context cancellation after supervision failure", elapsed)
	}
	results, err := store.WorkerResults(ctx, reviewWorkerRole)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Status != state.WorkerTaskStatusFailed || results[0].Reason != "worker_supervision_error" || strings.Contains(results[0].Error, context.Canceled.Error()) {
		t.Fatalf("worker result = %+v; want supervision failure without context cancellation as primary error", results)
	}
}

func TestContinuousWorkerTaskRequiresStateStore(t *testing.T) {
	ran := false
	didWork, err := runContinuousWorkerTask(context.Background(), nil, continuousWorkerTask{TaskKey: "continuous:merge", Role: "merge", LaneName: "merge"}, func(context.Context) (bool, error) {
		ran = true
		return true, nil
	})
	if err == nil || !strings.Contains(err.Error(), "SQLite state store unavailable for continuous worker task continuous:merge") {
		t.Fatalf("error = %v; want SQLite state store unavailable", err)
	}
	if didWork || ran {
		t.Fatalf("didWork=%v ran=%v; want lane not executed without SQLite", didWork, ran)
	}
}

func TestWorkerTaskLeaseReleaseSurvivesCanceledParentContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store, err := state.Open(ctx, state.DefaultDBPath(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	task := continuousWorkerTask{TaskKey: "continuous:merge", Role: mergeWorkerRole, LaneName: "merge", LeaseName: "lane:merge"}
	claimed := state.WorkerTask{TaskKey: task.TaskKey, Role: task.Role, Status: state.WorkerTaskStatusClaimed, LeaseName: task.LeaseName}
	release, err := acquireWorkerTaskLease(ctx, store, task, claimed, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	cancel()

	release()

	lease, ok, err := store.Lease(context.Background(), task.LeaseName)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || lease.ReleasedAt.IsZero() || lease.ReleaseReason != "worker task completed" {
		t.Fatalf("lease = %+v ok=%t; want released after parent cancellation", lease, ok)
	}
}

func TestContinuousWorkerTaskRecordsFailures(t *testing.T) {
	ctx := context.Background()
	store, err := state.Open(ctx, state.DefaultDBPath(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runErr := errors.New("lane failed")
	didWork, err := runContinuousWorkerTask(ctx, store, continuousWorkerTask{TaskKey: "continuous:scheduler", Role: "scheduler", LaneName: "work", LeaseName: "lane:work"}, func(context.Context) (bool, error) {
		return false, runErr
	})
	if !errors.Is(err, runErr) {
		t.Fatalf("error = %v; want %v", err, runErr)
	}
	if didWork {
		t.Fatal("didWork = true; want false")
	}
	tasks, err := store.WorkerTasks(ctx, "scheduler")
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Status != "failed" {
		t.Fatalf("worker task after failure = %+v; want failed", tasks)
	}
	results, err := store.WorkerResults(ctx, "scheduler")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Status != "failed" || results[0].Reason != "worker_error" || results[0].Error != runErr.Error() {
		t.Fatalf("worker result after failure = %+v; want failed worker_error result", results)
	}
	events, err := store.Events(ctx, state.EventFilter{Type: state.EventWorkerTaskFailed, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("failed events = %+v; want one", events)
	}
}

func TestContinuousWorkerTaskBacksOffAfterFailure(t *testing.T) {
	ctx := context.Background()
	store, err := state.Open(ctx, state.DefaultDBPath(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runErr := errors.New("lane failed")
	if _, err := runContinuousWorkerTask(ctx, store, continuousWorkerTask{TaskKey: "continuous:merge", Role: mergeWorkerRole, LaneName: "merge", LeaseName: "lane:merge"}, func(context.Context) (bool, error) {
		return false, runErr
	}); !errors.Is(err, runErr) {
		t.Fatalf("first run error = %v; want %v", err, runErr)
	}

	ran := false
	didWork, err := runContinuousWorkerTask(ctx, store, continuousWorkerTask{TaskKey: "continuous:merge", Role: mergeWorkerRole, LaneName: "merge", LeaseName: "lane:merge"}, func(context.Context) (bool, error) {
		ran = true
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if didWork || ran {
		t.Fatalf("didWork=%v ran=%v; want failed task requeued for future backoff without execution", didWork, ran)
	}
	task, ok, err := store.WorkerTask(ctx, "continuous:merge")
	if err != nil || !ok {
		t.Fatalf("WorkerTask() ok=%v err=%v", ok, err)
	}
	if task.Status != state.WorkerTaskStatusQueued || !task.AvailableAt.After(time.Now().UTC()) {
		t.Fatalf("task after immediate retry = %+v; want queued with future available_at", task)
	}
}

func eventTypes(events []state.Event) []string {
	out := make([]string, 0, len(events))
	for _, event := range events {
		out = append(out, event.Type)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
