package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	cfg "github.com/weskor/agent-machine/internal/config"
	"github.com/weskor/agent-machine/internal/state"
)

func runContinuous(client linearClient, proj project, config runnerConfig, maxCycles int) error {
	maxConcurrentAgents := configuredMaxConcurrentAgents(proj)
	log("mode=continuous; lanes=scheduler,cleanup,merge,handoff,review,implementation; project=%s; states=%s; max_concurrent_agents=%d", config.ProjectSlug, strings.Join(config.ActiveStates, ", "), maxConcurrentAgents)
	if _, err := ensureWorkspaceRoot(config.WorkspaceRoot); err != nil {
		return fmt.Errorf("workspace root preflight failed for continuous mode: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stateStore, stateDBPath := commandScopedStateStore(ctx, config.WorkspaceRoot, "continuous")
	if stateStore == nil {
		return fmt.Errorf("SQLite state store unavailable for continuous mode at %s", stateDBPath)
	}
	defer stateStore.Close()
	recordHeartbeat := daemonHeartbeatRecorder(ctx, config, stateStore)

	scheduler := continuousScheduler{
		maxCycles:       maxCycles,
		recordHeartbeat: recordHeartbeat,
		lanes:           continuousLanes(ctx, client, proj, config, stateStore, maxConcurrentAgents, recordHeartbeat),
	}
	return scheduler.run(ctx)
}

var scheduleWorkerTasksForContinuous = scheduleContinuousWorkerTasks
var runCleanupWorkerTaskForContinuous = runQueuedCleanupWorkerTaskContext
var runImplementationAttemptBatchForContinuous = runQueuedImplementationAttemptBatchContext
var issueIdentifiersByStateForContinuousCleanup = func(ctx context.Context, client linearClient, projectSlug, state string) (map[string]bool, error) {
	return client.issueIdentifiersByStateContext(ctx, projectSlug, state)
}
var runMergeWorkerTaskForContinuous = runQueuedMergeWorkerTaskContext
var runHandoffPendingAttemptForContinuous = runHandoffPendingAttemptContext

const staleWorkerTaskAfter = 15 * time.Minute

func continuousLanes(ctx context.Context, client linearClient, proj project, config runnerConfig, stateStore *state.Store, maxConcurrentAgents int, recordHeartbeat func(continuousHeartbeat)) []continuousLane {
	return []continuousLane{
		{
			name:      "scheduler",
			idleDelay: 30 * time.Second,
			run: func() (bool, error) {
				return runContinuousWorkerTask(ctx, stateStore, continuousWorkerTask{TaskKey: "continuous:scheduler", Role: schedulerWorkerRole, LaneName: "scheduler", LeaseName: "lane:scheduler", Payload: map[string]any{"project_slug": config.ProjectSlug, "max_concurrent_agents": maxConcurrentAgents}, RecordHeartbeat: recordHeartbeat}, func(runCtx context.Context) (bool, error) {
					return scheduleWorkerTasksForContinuous(runCtx, client, config, stateStore, maxConcurrentAgents)
				})
			},
		},
		{
			name:       "cleanup",
			idleDelay:  30 * time.Second,
			continuous: true,
			run: func() (bool, error) {
				return runContinuousWorkerTask(ctx, stateStore, continuousWorkerTask{TaskKey: "continuous:cleanup", Role: cleanupWorkerRole, LaneName: "cleanup", LeaseName: "lane:cleanup", Payload: map[string]any{"project_slug": config.ProjectSlug, "done_state": config.DoneState, "apply": true}, RecordHeartbeat: recordHeartbeat}, func(runCtx context.Context) (bool, error) {
					return runCleanupWorkerTaskForContinuous(runCtx, client, config, stateStore)
				})
			},
		},
		{
			name:       "merge",
			idleDelay:  30 * time.Second,
			continuous: true,
			run: func() (bool, error) {
				return runContinuousWorkerTask(ctx, stateStore, continuousWorkerTask{TaskKey: "continuous:merge", Role: mergeWorkerRole, LaneName: "merge", LeaseName: "lane:merge", Payload: map[string]any{"project_slug": config.ProjectSlug, "handoff_state": config.HandoffState}, RecordHeartbeat: recordHeartbeat}, func(runCtx context.Context) (bool, error) {
					return runMergeWorkerTaskForContinuous(runCtx, client, config, stateStore)
				})
			},
		},
		{
			name:      "handoff",
			idleDelay: 60 * time.Second,
			run: func() (bool, error) {
				return runContinuousWorkerTask(ctx, stateStore, continuousWorkerTask{TaskKey: "continuous:handoff", Role: handoffWorkerRole, LaneName: "handoff", LeaseName: "lane:handoff", Payload: map[string]any{"project_slug": config.ProjectSlug, "handoff_state": config.HandoffState}, RecordHeartbeat: recordHeartbeat}, func(runCtx context.Context) (bool, error) {
					return runHandoffPendingAttemptForContinuous(runCtx, client, config, stateStore)
				})
			},
		},
		{
			name:      "review",
			idleDelay: 60 * time.Second,
			run: func() (bool, error) {
				return runContinuousWorkerTask(ctx, stateStore, continuousWorkerTask{TaskKey: "continuous:review", Role: reviewWorkerRole, LaneName: "review", LeaseName: "lane:review", Payload: map[string]any{"project_slug": config.ProjectSlug, "review_configured": strings.TrimSpace(config.ReviewCommand) != ""}, RecordHeartbeat: recordHeartbeat}, func(runCtx context.Context) (bool, error) {
					return runReviewReadyAttemptForWorker(runCtx, client, proj, config, stateStore)
				})
			},
		},
		{
			name:      "implementation",
			idleDelay: 60 * time.Second,
			run: func() (bool, error) {
				return runContinuousWorkerTask(ctx, stateStore, continuousWorkerTask{TaskKey: "continuous:implementation", Role: implementationWorkerRole, LaneName: "implementation", LeaseName: "lane:implementation", Payload: map[string]any{"project_slug": config.ProjectSlug, "max_concurrent_agents": maxConcurrentAgents, "review_ready_resumes_skipped": true}, RecordHeartbeat: recordHeartbeat}, func(runCtx context.Context) (bool, error) {
					return runImplementationAttemptBatchForContinuous(runCtx, client, proj, config, stateStore, maxConcurrentAgents)
				})
			},
		},
	}
}

func scheduleContinuousWorkerTasks(ctx context.Context, client linearClient, config runnerConfig, stateStore *state.Store, maxConcurrentAgents int) (bool, error) {
	recovered, err := recoverStaleWorkerTasks(ctx, stateStore, time.Now().UTC())
	if err != nil || recovered {
		return recovered, err
	}
	didCleanupWork, err := scheduleCleanupWorkerTasks(ctx, config, stateStore)
	if err != nil {
		return didCleanupWork, err
	}
	didMergeWork, err := scheduleMergeWorkerTasks(ctx, client, config, stateStore)
	if err != nil {
		return didCleanupWork || didMergeWork, err
	}
	didReviewWork, err := scheduleReviewReadyWorkerTasks(ctx, client, config, stateStore, maxConcurrentAgents)
	if err != nil {
		return didCleanupWork || didMergeWork || didReviewWork, err
	}
	didImplementationWork, err := scheduleImplementationWorkerTasks(ctx, client, config, stateStore, maxConcurrentAgents)
	return didCleanupWork || didMergeWork || didReviewWork || didImplementationWork, err
}

func recoverStaleWorkerTasks(ctx context.Context, stateStore *state.Store, now time.Time) (bool, error) {
	if stateStore == nil {
		return false, fmt.Errorf("SQLite state store unavailable for worker recovery")
	}
	recovered, err := stateStore.MarkStaleClaimedWorkerTasksReconciliationNeeded(ctx, now, staleWorkerTaskAfter)
	if err != nil {
		return false, err
	}
	for _, task := range recovered {
		log("worker task %s marked reconciliation_needed after stale claimed state", task.TaskKey)
	}
	return len(recovered) > 0, nil
}

type claimedAttemptFunc func(context.Context, linearClient, project, runnerConfig, *state.Store) (*claimedRunAttempt, bool, error)

func runClaimedAttemptBatchWithClaimerContext(ctx context.Context, client linearClient, proj project, config runnerConfig, stateStore *state.Store, capacity int, claim claimedAttemptFunc) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if capacity < 1 {
		capacity = 1
	}
	claims := make([]claimedRunAttempt, 0, capacity)
	didAnyWork := false
	releaseClaims := func() {
		for i := range claims {
			if claims[i].ReleaseLock != nil {
				claims[i].ReleaseLock()
				claims[i].ReleaseLock = nil
			}
		}
	}
	for len(claims) < capacity {
		if err := ctx.Err(); err != nil {
			releaseClaims()
			return didAnyWork, err
		}
		claim, didWork, err := claim(ctx, client, proj, config, stateStore)
		if didWork {
			didAnyWork = true
		}
		if err != nil {
			releaseClaims()
			return didAnyWork, err
		}
		if claim == nil {
			break
		}
		claims = append(claims, *claim)
	}
	if len(claims) == 0 {
		return didAnyWork, nil
	}

	errs := make(chan error, len(claims))
	var wg sync.WaitGroup
	for i := range claims {
		claim := claims[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := executeClaimedRunAttempt(ctx, client, proj, config, stateStore, claim)
			if err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	if err := <-errs; err != nil {
		return true, err
	}
	return true, nil
}

type continuousWorkerTask struct {
	TaskKey         string
	Role            string
	LaneName        string
	LeaseName       string
	Payload         map[string]any
	RecordHeartbeat func(continuousHeartbeat)
}

type continuousWorkerRunResult struct {
	didWork bool
	err     error
}

func runContinuousWorkerTask(ctx context.Context, store *state.Store, task continuousWorkerTask, run func(context.Context) (bool, error)) (bool, error) {
	if store == nil {
		return false, fmt.Errorf("SQLite state store unavailable for continuous worker task %s", task.TaskKey)
	}
	now := time.Now().UTC()
	if recovered, err := recoverStaleWorkerTasks(ctx, store, now); err != nil || recovered {
		return recovered, err
	}
	var existing state.WorkerTask
	var hasExisting bool
	if current, ok, err := store.WorkerTask(ctx, task.TaskKey); err != nil {
		return false, err
	} else if ok && (current.Status == state.WorkerTaskStatusClaimed || current.Status == state.WorkerTaskStatusReconciliationNeeded) {
		return false, nil
	} else if ok {
		existing = current
		hasExisting = true
	}
	payload := map[string]any{"lane": task.LaneName}
	for key, value := range task.Payload {
		payload[key] = value
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return false, fmt.Errorf("encode continuous worker task payload: %w", err)
	}
	availableAt := now
	if hasExisting {
		availableAt, err = workerTaskAvailableAtAfterLatestFailure(ctx, store, existing.TaskKey, task.Role, now)
		if err != nil {
			return false, err
		}
	}
	queued := state.WorkerTask{TaskKey: task.TaskKey, Role: task.Role, Status: "queued", AvailableAt: availableAt, LeaseName: task.LeaseName, Payload: payloadJSON, UpdatedAt: now}
	if err := store.UpsertWorkerTask(ctx, queued); err != nil {
		return false, err
	}
	recordContinuousWorkerTaskEvent(ctx, store, state.EventWorkerTaskQueued, queued, map[string]any{"lane": task.LaneName})
	claimed, ok, err := store.ClaimWorkerTask(ctx, task.TaskKey, now)
	if err != nil || !ok {
		return false, err
	}
	startedAt := time.Now().UTC()
	recordContinuousWorkerTaskEvent(ctx, store, state.EventWorkerTaskClaimed, claimed, map[string]any{"lane": task.LaneName})
	releaseLease, leaseErr := acquireWorkerTaskLease(ctx, store, task, claimed, now)
	if leaseErr != nil {
		finishedAt := time.Now().UTC()
		claimed.Status = "failed"
		claimed.UpdatedAt = finishedAt
		completeErr := store.CompleteWorkerTask(ctx, task.TaskKey, "failed", finishedAt)
		resultErr := recordContinuousWorkerResult(ctx, store, task, claimed, "failed", false, "lease_unavailable", leaseErr.Error(), startedAt, finishedAt, map[string]any{"lane": task.LaneName})
		recordContinuousWorkerTaskEvent(ctx, store, state.EventWorkerTaskFailed, claimed, map[string]any{"lane": task.LaneName, "error": leaseErr.Error()})
		return false, errors.Join(leaseErr, completeErr, resultErr)
	}
	if releaseLease != nil {
		defer releaseLease()
	}
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	supervision := startWorkerTaskSupervision(runCtx, store, task, claimed, leaseNameForWorkerTask(task, claimed), startedAt)
	runDone := make(chan continuousWorkerRunResult, 1)
	go func() {
		didWork, runErr := run(runCtx)
		runDone <- continuousWorkerRunResult{didWork: didWork, err: runErr}
	}()
	var result continuousWorkerRunResult
	var supervisionErr error
	canceledBySupervision := false
	select {
	case result = <-runDone:
	case supervisionErr = <-supervision.Err():
		canceledBySupervision = true
		cancelRun()
		result = <-runDone
	}
	finishedAt := time.Now().UTC()
	supervisionErr = errors.Join(supervisionErr, supervision.Stop(finishedAt))
	select {
	case err := <-supervision.Err():
		supervisionErr = errors.Join(supervisionErr, err)
	default:
	}
	didWork := result.didWork
	runErr := result.err
	if canceledBySupervision && errors.Is(runErr, context.Canceled) {
		runErr = nil
	}
	status := "completed"
	reason := "idle"
	eventType := state.EventWorkerTaskCompleted
	eventPayload := map[string]any{"lane": task.LaneName, "did_work": didWork}
	errorText := ""
	if didWork {
		reason = "work_completed"
	}
	if runErr != nil {
		status = "failed"
		reason = "worker_error"
		errorText = runErr.Error()
		eventType = state.EventWorkerTaskFailed
		eventPayload["error"] = runErr.Error()
	}
	if supervisionErr != nil {
		eventPayload["supervision_error"] = supervisionErr.Error()
		if runErr == nil {
			status = "failed"
			reason = "worker_supervision_error"
			errorText = supervisionErr.Error()
			eventType = state.EventWorkerTaskFailed
		}
	}
	completeErr := store.CompleteWorkerTask(ctx, task.TaskKey, status, finishedAt)
	if completeErr != nil {
		eventPayload["completion_error"] = completeErr.Error()
		if runErr == nil {
			status = "failed"
			reason = "worker_completion_error"
			errorText = completeErr.Error()
		}
	}
	claimed.Status = status
	claimed.UpdatedAt = finishedAt
	resultErr := recordContinuousWorkerResult(ctx, store, task, claimed, status, didWork, reason, errorText, startedAt, finishedAt, eventPayload)
	recordContinuousWorkerTaskEvent(ctx, store, eventType, claimed, eventPayload)
	if runErr != nil {
		return didWork, errors.Join(runErr, supervisionErr, completeErr, resultErr)
	}
	return didWork, errors.Join(supervisionErr, completeErr, resultErr)
}

func recordContinuousWorkerResult(ctx context.Context, store *state.Store, task continuousWorkerTask, claimed state.WorkerTask, status string, didWork bool, reason, errorText string, startedAt, finishedAt time.Time, payload map[string]any) error {
	if store == nil {
		return nil
	}
	if payload == nil {
		payload = map[string]any{}
	}
	payload["task_key"] = claimed.TaskKey
	payload["role"] = claimed.Role
	payload["status"] = status
	payload["reason"] = reason
	payload["did_work"] = didWork
	if _, ok := payload["lane"]; !ok {
		payload["lane"] = task.LaneName
	}
	if errorText != "" {
		payload["error"] = errorText
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode worker result payload: %w", err)
	}
	return store.UpsertWorkerResult(ctx, state.WorkerResult{TaskKey: claimed.TaskKey, Role: claimed.Role, LaneName: task.LaneName, IssueKey: claimed.IssueKey, IssueID: claimed.IssueID, Attempt: claimed.Attempt, Status: status, DidWork: didWork, Reason: reason, Error: errorText, Payload: payloadJSON, StartedAt: startedAt, FinishedAt: finishedAt, UpdatedAt: finishedAt})
}

const workerTaskLeaseTTL = 5 * time.Minute

var workerTaskLeaseRenewInterval = time.Minute

func acquireWorkerTaskLease(ctx context.Context, store *state.Store, task continuousWorkerTask, claimed state.WorkerTask, now time.Time) (func(), error) {
	leaseName := leaseNameForWorkerTask(task, claimed)
	if leaseName == "" {
		return nil, nil
	}
	acquired, err := store.AcquireLease(ctx, state.Lease{Name: leaseName, Scope: claimed.TaskKey, Owner: daemonProcessID(), AcquiredAt: now, RenewedAt: now, ExpiresAt: now.Add(workerTaskLeaseTTL)}, now)
	if err != nil {
		return nil, err
	}
	if !acquired {
		return nil, fmt.Errorf("worker task lease %s: %w", leaseName, state.ErrLeaseHeld)
	}
	return func() {
		if err := store.ReleaseLease(context.WithoutCancel(ctx), leaseName, time.Now().UTC(), "worker task completed"); err != nil {
			log("failed to release worker task lease %s: %v", leaseName, err)
		}
	}, nil
}

func leaseNameForWorkerTask(task continuousWorkerTask, claimed state.WorkerTask) string {
	leaseName := strings.TrimSpace(task.LeaseName)
	if leaseName == "" {
		leaseName = strings.TrimSpace(claimed.LeaseName)
	}
	return leaseName
}

type workerTaskSupervision struct {
	errc <-chan error
	stop func(time.Time) error
}

func (s workerTaskSupervision) Err() <-chan error {
	return s.errc
}

func (s workerTaskSupervision) Stop(finishedAt time.Time) error {
	if s.stop == nil {
		return nil
	}
	return s.stop(finishedAt)
}

func startWorkerTaskSupervision(ctx context.Context, store *state.Store, task continuousWorkerTask, claimed state.WorkerTask, leaseName string, startedAt time.Time) workerTaskSupervision {
	recordWorkerTaskHeartbeat(task, claimed, leaseName, startedAt, startedAt)
	errc := make(chan error, 1)
	if store == nil || workerTaskLeaseRenewInterval <= 0 {
		return workerTaskSupervision{errc: errc, stop: func(finishedAt time.Time) error {
			recordContinuousHeartbeat(task.RecordHeartbeat, continuousHeartbeat{LaneName: task.LaneName, At: finishedAt})
			return nil
		}}
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(workerTaskLeaseRenewInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				errc <- ctx.Err()
				return
			case <-stop:
				return
			case now := <-ticker.C:
				if err := renewWorkerTaskSupervision(ctx, store, task, claimed, leaseName, startedAt, now.UTC()); err != nil {
					errc <- err
					return
				}
			}
		}
	}()
	var once sync.Once
	return workerTaskSupervision{errc: errc, stop: func(finishedAt time.Time) error {
		once.Do(func() { close(stop) })
		<-done
		recordContinuousHeartbeat(task.RecordHeartbeat, continuousHeartbeat{LaneName: task.LaneName, At: finishedAt})
		return nil
	}}
}

func renewWorkerTaskSupervision(ctx context.Context, store *state.Store, task continuousWorkerTask, claimed state.WorkerTask, leaseName string, startedAt, now time.Time) error {
	if leaseName != "" {
		if err := store.RenewLease(ctx, leaseName, now, now.Add(workerTaskLeaseTTL)); err != nil {
			return err
		}
	}
	if err := store.TouchClaimedWorkerTask(ctx, claimed.TaskKey, now); err != nil {
		return err
	}
	recordWorkerTaskHeartbeat(task, claimed, leaseName, startedAt, now)
	return nil
}

func recordWorkerTaskHeartbeat(task continuousWorkerTask, claimed state.WorkerTask, leaseName string, startedAt, at time.Time) {
	recordContinuousHeartbeat(task.RecordHeartbeat, continuousHeartbeat{
		LaneName:            task.LaneName,
		ActiveTaskKey:       claimed.TaskKey,
		ActiveTaskRole:      claimed.Role,
		ActiveLeaseName:     leaseName,
		ActiveTaskStartedAt: startedAt,
		At:                  at,
	})
}

func recordContinuousWorkerTaskEvent(ctx context.Context, store *state.Store, eventType string, task state.WorkerTask, payload map[string]any) {
	if store == nil {
		return
	}
	if payload == nil {
		payload = map[string]any{}
	}
	payload["task_key"] = task.TaskKey
	payload["role"] = task.Role
	payload["status"] = task.Status
	if _, err := store.AppendEvent(ctx, state.EventInput{OccurredAt: time.Now().UTC(), IssueKey: task.IssueKey, IssueID: task.IssueID, Attempt: task.Attempt, RunID: task.TaskKey, Source: "worker." + task.Role, Type: eventType, Payload: payload}); err != nil {
		log("failed to append worker task event %s for %s: %v", eventType, task.TaskKey, err)
	}
}

type continuousLane struct {
	name       string
	idleDelay  time.Duration
	continuous bool
	run        func() (bool, error)
}

type continuousScheduler struct {
	lanes           []continuousLane
	maxCycles       int
	recordHeartbeat func(continuousHeartbeat)
}

func (s continuousScheduler) run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errs := make(chan error, len(s.lanes))
	var wg sync.WaitGroup
	for _, lane := range s.lanes {
		lane := lane
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := runContinuousLane(ctx, lane, s.maxCycles, s.recordHeartbeat); err != nil {
				errs <- err
				cancel()
			}
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		select {
		case err := <-errs:
			return err
		default:
			return nil
		}
	case err := <-errs:
		cancel()
		<-done
		return err
	}
}

type continuousHeartbeat struct {
	LaneName            string
	CycleNumber         int
	Success             bool
	Err                 error
	ActiveTaskKey       string
	ActiveTaskRole      string
	ActiveLeaseName     string
	ActiveTaskStartedAt time.Time
	At                  time.Time
}

func runContinuousLane(ctx context.Context, lane continuousLane, maxCycles int, recordHeartbeat func(continuousHeartbeat)) error {
	cycles := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		log("lane=%s cycle=%d starting", lane.name, cycles+1)
		didWork, err := lane.run()
		cycleNumber := cycles + 1
		if err != nil {
			recordContinuousHeartbeat(recordHeartbeat, continuousHeartbeat{LaneName: lane.name, CycleNumber: cycleNumber, Err: err, At: time.Now().UTC()})
			return err
		}
		cycles++
		recordContinuousHeartbeat(recordHeartbeat, continuousHeartbeat{LaneName: lane.name, CycleNumber: cycles, Success: true, At: time.Now().UTC()})
		if maxCycles > 0 && cycles >= maxCycles {
			log("lane=%s completed %d continuous cycle(s)", lane.name, cycles)
			return nil
		}

		delay := time.Duration(0)
		if lane.continuous || !didWork {
			delay = lane.idleDelay
		}
		if delay > 0 {
			if !didWork {
				log("lane=%s idle; sleeping %s", lane.name, delay)
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(delay):
			}
		}
	}
}

func configuredMaxConcurrentAgents(proj project) int {
	schema, err := cfg.ParseConfig(proj.YAML)
	if err != nil || schema.Agent.MaxConcurrentAgents < 1 {
		return 1
	}
	return schema.Agent.MaxConcurrentAgents
}

func recordContinuousHeartbeat(recordHeartbeat func(continuousHeartbeat), heartbeat continuousHeartbeat) {
	if recordHeartbeat != nil {
		recordHeartbeat(heartbeat)
	}
}

func daemonHeartbeatRecorder(ctx context.Context, config runnerConfig, commandStore *state.Store) func(continuousHeartbeat) {
	if commandStore != nil {
		processID := daemonProcessID()
		return func(heartbeat continuousHeartbeat) {
			if err := commandStore.UpsertDaemonHeartbeat(ctx, stateProjection{}.DaemonHeartbeat(processID, config, heartbeat)); err != nil {
				log("SQLite daemon heartbeat degraded: lane=%s cycle=%d error=%q", heartbeat.LaneName, heartbeat.CycleNumber, err.Error())
			}
		}
	}
	dbPath := state.DefaultDBPath(config.WorkspaceRoot)
	if dbPath == "" {
		return nil
	}
	store, err := state.Open(ctx, dbPath)
	if err != nil {
		log("SQLite daemon heartbeat degraded: open path=%s error=%q", dbPath, err.Error())
		return nil
	}
	processID := daemonProcessID()
	return func(heartbeat continuousHeartbeat) {
		if err := store.UpsertDaemonHeartbeat(ctx, stateProjection{}.DaemonHeartbeat(processID, config, heartbeat)); err != nil {
			log("SQLite daemon heartbeat degraded: lane=%s cycle=%d error=%q", heartbeat.LaneName, heartbeat.CycleNumber, err.Error())
		}
	}
}

func daemonProcessID() string {
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = "unknown-host"
	}
	return fmt.Sprintf("%s:%d", hostname, os.Getpid())
}

// runOne executes a single Linear issue attempt, including optional review
// handoff. It returns false when there is no eligible issue to process.
