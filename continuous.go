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

	cfg "github.com/weskor/pi-symphony/internal/config"
	"github.com/weskor/pi-symphony/internal/state"
)

func runContinuous(client linearClient, wf workflow, config runnerConfig, maxCycles int) error {
	maxConcurrentAgents := configuredMaxConcurrentAgents(wf)
	log("mode=continuous; lanes=cleanup,merge,review,implementation; project=%s; states=%s; max_concurrent_agents=%d", config.ProjectSlug, strings.Join(config.ActiveStates, ", "), maxConcurrentAgents)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stateStore, _ := commandScopedStateStore(ctx, config.WorkspaceRoot, "continuous")
	if stateStore != nil {
		defer stateStore.Close()
	}
	recordHeartbeat := daemonHeartbeatRecorder(ctx, config, stateStore)

	scheduler := continuousScheduler{
		maxCycles:       maxCycles,
		recordHeartbeat: recordHeartbeat,
		lanes:           continuousLanes(ctx, client, wf, config, stateStore, maxConcurrentAgents),
	}
	return scheduler.run(ctx)
}

var runImplementationAttemptBatchForContinuous = runImplementationAttemptBatch
var issueIdentifiersByStateForContinuousCleanup = func(client linearClient, projectSlug, state string) (map[string]bool, error) {
	return client.issueIdentifiersByState(projectSlug, state)
}
var cleanupWorkspacesForContinuous = cleanupWorkspaces
var mergeApprovedPRsForContinuous = mergeApprovedPRsWithStore

func continuousLanes(ctx context.Context, client linearClient, wf workflow, config runnerConfig, stateStore *state.Store, maxConcurrentAgents int) []continuousLane {
	return []continuousLane{
		{
			name:       "cleanup",
			idleDelay:  30 * time.Second,
			continuous: true,
			run: func() (bool, error) {
				return runContinuousWorkerTask(ctx, stateStore, continuousWorkerTask{TaskKey: "continuous:cleanup", Role: cleanupWorkerRole, LaneName: "cleanup", LeaseName: "lane:cleanup", Payload: map[string]any{"project_slug": config.ProjectSlug, "done_state": config.DoneState, "apply": true}}, func() (bool, error) {
					doneIssues, err := issueIdentifiersByStateForContinuousCleanup(client, config.ProjectSlug, config.DoneState)
					if err != nil {
						return false, err
					}
					if err := cleanupWorkspacesForContinuous(config.WorkspaceRoot, cleanupOptions{Apply: true, DoneIssues: doneIssues, StateStore: stateStore}); err != nil {
						return false, err
					}
					return true, nil
				})
			},
		},
		{
			name:       "merge",
			idleDelay:  30 * time.Second,
			continuous: true,
			run: func() (bool, error) {
				return runContinuousWorkerTask(ctx, stateStore, continuousWorkerTask{TaskKey: "continuous:merge", Role: mergeWorkerRole, LaneName: "merge", LeaseName: "lane:merge", Payload: map[string]any{"project_slug": config.ProjectSlug, "handoff_state": config.HandoffState}}, func() (bool, error) {
					if err := mergeApprovedPRsForContinuous(client, config, stateStore); err != nil {
						return false, err
					}
					return true, nil
				})
			},
		},
		{
			name:      "review",
			idleDelay: 60 * time.Second,
			run: func() (bool, error) {
				return runContinuousWorkerTask(ctx, stateStore, continuousWorkerTask{TaskKey: "continuous:review", Role: reviewWorkerRole, LaneName: "review", LeaseName: "lane:review", Payload: map[string]any{"project_slug": config.ProjectSlug, "review_configured": strings.TrimSpace(config.ReviewCommand) != ""}}, func() (bool, error) {
					return runReviewReadyAttemptForWorker(client, wf, config, stateStore)
				})
			},
		},
		{
			name:      "implementation",
			idleDelay: 60 * time.Second,
			run: func() (bool, error) {
				return runContinuousWorkerTask(ctx, stateStore, continuousWorkerTask{TaskKey: "continuous:implementation", Role: implementationWorkerRole, LaneName: "implementation", LeaseName: "lane:implementation", Payload: map[string]any{"project_slug": config.ProjectSlug, "max_concurrent_agents": maxConcurrentAgents, "review_ready_resumes_skipped": true}}, func() (bool, error) {
					return runImplementationAttemptBatchForContinuous(client, wf, config, stateStore, maxConcurrentAgents)
				})
			},
		},
	}
}

func runClaimedAttemptBatch(client linearClient, wf workflow, config runnerConfig, stateStore *state.Store, capacity int) (bool, error) {
	if stateStore == nil {
		return false, fmt.Errorf("SQLite state store unavailable for continuous attempt batch at %s", state.DefaultDBPath(config.WorkspaceRoot))
	}
	return runClaimedAttemptBatchWithClaimer(client, wf, config, stateStore, capacity, claimNextRunAttempt)
}

type claimedAttemptFunc func(linearClient, workflow, runnerConfig, *state.Store) (*claimedRunAttempt, bool, error)

func runClaimedAttemptBatchWithClaimer(client linearClient, wf workflow, config runnerConfig, stateStore *state.Store, capacity int, claim claimedAttemptFunc) (bool, error) {
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
		claim, didWork, err := claim(client, wf, config, stateStore)
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
			_, err := executeClaimedRunAttempt(client, wf, config, stateStore, claim)
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
	TaskKey   string
	Role      string
	LaneName  string
	LeaseName string
	Payload   map[string]any
}

func runContinuousWorkerTask(ctx context.Context, store *state.Store, task continuousWorkerTask, run func() (bool, error)) (bool, error) {
	if store == nil {
		return run()
	}
	now := time.Now().UTC()
	payload := map[string]any{"lane": task.LaneName}
	for key, value := range task.Payload {
		payload[key] = value
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return false, fmt.Errorf("encode continuous worker task payload: %w", err)
	}
	queued := state.WorkerTask{TaskKey: task.TaskKey, Role: task.Role, Status: "queued", AvailableAt: now, LeaseName: task.LeaseName, Payload: payloadJSON, UpdatedAt: now}
	if err := store.UpsertWorkerTask(ctx, queued); err != nil {
		return false, err
	}
	recordContinuousWorkerTaskEvent(store, state.EventWorkerTaskQueued, queued, map[string]any{"lane": task.LaneName})
	claimed, ok, err := store.ClaimWorkerTask(ctx, task.TaskKey, now)
	if err != nil || !ok {
		return false, err
	}
	recordContinuousWorkerTaskEvent(store, state.EventWorkerTaskClaimed, claimed, map[string]any{"lane": task.LaneName})
	releaseLease, leaseErr := acquireWorkerTaskLease(ctx, store, task, claimed, now)
	if leaseErr != nil {
		finishedAt := time.Now().UTC()
		claimed.Status = "failed"
		claimed.UpdatedAt = finishedAt
		completeErr := store.CompleteWorkerTask(ctx, task.TaskKey, "failed", finishedAt)
		recordContinuousWorkerTaskEvent(store, state.EventWorkerTaskFailed, claimed, map[string]any{"lane": task.LaneName, "error": leaseErr.Error()})
		return false, errors.Join(leaseErr, completeErr)
	}
	if releaseLease != nil {
		defer releaseLease()
	}
	didWork, runErr := run()
	finishedAt := time.Now().UTC()
	status := "completed"
	eventType := state.EventWorkerTaskCompleted
	eventPayload := map[string]any{"lane": task.LaneName, "did_work": didWork}
	if runErr != nil {
		status = "failed"
		eventType = state.EventWorkerTaskFailed
		eventPayload["error"] = runErr.Error()
	}
	completeErr := store.CompleteWorkerTask(ctx, task.TaskKey, status, finishedAt)
	if completeErr != nil {
		eventPayload["completion_error"] = completeErr.Error()
	}
	claimed.Status = status
	claimed.UpdatedAt = finishedAt
	recordContinuousWorkerTaskEvent(store, eventType, claimed, eventPayload)
	if runErr != nil {
		return didWork, errors.Join(runErr, completeErr)
	}
	return didWork, completeErr
}

const workerTaskLeaseTTL = 5 * time.Minute

func acquireWorkerTaskLease(ctx context.Context, store *state.Store, task continuousWorkerTask, claimed state.WorkerTask, now time.Time) (func(), error) {
	leaseName := strings.TrimSpace(task.LeaseName)
	if leaseName == "" {
		leaseName = strings.TrimSpace(claimed.LeaseName)
	}
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
		if err := store.ReleaseLease(context.Background(), leaseName, time.Now().UTC(), "worker task completed"); err != nil {
			log("failed to release worker task lease %s: %v", leaseName, err)
		}
	}, nil
}

func recordContinuousWorkerTaskEvent(store *state.Store, eventType string, task state.WorkerTask, payload map[string]any) {
	if store == nil {
		return
	}
	if payload == nil {
		payload = map[string]any{}
	}
	payload["task_key"] = task.TaskKey
	payload["role"] = task.Role
	payload["status"] = task.Status
	if _, err := store.AppendEvent(context.Background(), state.EventInput{OccurredAt: time.Now().UTC(), IssueKey: task.IssueKey, IssueID: task.IssueID, Attempt: task.Attempt, RunID: task.TaskKey, Source: "worker." + task.Role, Type: eventType, Payload: payload}); err != nil {
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
	LaneName    string
	CycleNumber int
	Success     bool
	Err         error
	At          time.Time
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

func configuredMaxConcurrentAgents(wf workflow) int {
	schema, err := cfg.ParseConfig(wf.YAML)
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
