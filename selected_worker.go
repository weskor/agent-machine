package main

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const statusWorkerRole = "status"
const planWorkerRole = "plan"
const cleanupWorkerRole = "cleanup"
const mergeWorkerRole = "merge"
const reviewWorkerRole = "review"
const implementationWorkerRole = "implementation"
const handoffWorkerRole = "handoff"
const workWorkerRole = "work"

func runSelectedWorker(client linearClient, wf workflow, config runnerConfig, role string) error {
	role = strings.TrimSpace(role)
	switch role {
	case statusWorkerRole:
		return runStatusWorkerProcess(client, config)
	case planWorkerRole:
		return runPlanWorkerProcess(client, config)
	case cleanupWorkerRole:
		return runCleanupWorkerProcess(client, config)
	case mergeWorkerRole:
		return runMergeWorkerProcess(client, config)
	case reviewWorkerRole:
		return runReviewWorkerProcess(client, wf, config)
	case implementationWorkerRole:
		return runImplementationWorkerProcess(client, wf, config)
	case handoffWorkerRole:
		return runHandoffWorkerProcess(client, config)
	case workWorkerRole:
		return runWorkWorkerProcess(client, wf, config)
	default:
		return fmt.Errorf("unsupported worker role %q; supported roles: %s", role, strings.Join(supportedWorkerRoles(), ", "))
	}
}

func supportedWorkerRoles() []string {
	return []string{statusWorkerRole, planWorkerRole, cleanupWorkerRole, mergeWorkerRole, reviewWorkerRole, implementationWorkerRole, handoffWorkerRole, workWorkerRole}
}

func runStatusWorkerProcess(client linearClient, config runnerConfig) error {
	ctx := context.Background()
	stateStore, stateDBPath := commandScopedStateStore(ctx, config.WorkspaceRoot, "worker-status")
	if stateStore == nil {
		return fmt.Errorf("SQLite state store unavailable for status worker at %s", stateDBPath)
	}
	defer stateStore.Close()
	recordHeartbeat := daemonHeartbeatRecorder(ctx, config, stateStore)
	didWork, err := runContinuousWorkerTask(ctx, stateStore, continuousWorkerTask{
		TaskKey:   "process:status",
		Role:      statusWorkerRole,
		LaneName:  "worker:status",
		LeaseName: "worker:status",
		Payload:   map[string]any{"project_slug": config.ProjectSlug},
	}, func() (bool, error) {
		return true, printStatusForWorker(client, config)
	})
	recordContinuousHeartbeat(recordHeartbeat, continuousHeartbeat{LaneName: "worker:status", CycleNumber: 1, Success: err == nil && didWork, Err: err, At: stateNow()})
	return err
}

var printStatusForWorker = printStatus
var printPlanForWorker = printExplain
var cleanupWorkspacesForWorker = cleanupWorkspaces
var issueIdentifiersByStateForCleanupWorker = func(client linearClient, projectSlug, state string) (map[string]bool, error) {
	return client.issueIdentifiersByState(projectSlug, state)
}
var issueIdentifiersByStateForMergeWorker = func(client linearClient, projectSlug, state string) (map[string]bool, error) {
	return client.issueIdentifiersByState(projectSlug, state)
}
var mergeApprovedPRsForWorker = mergeApprovedPRs
var runReviewReadyAttemptForWorker = runReviewReadyAttempt
var runImplementationAttemptForWorker = runImplementationAttempt
var runHandoffPendingAttemptForWorker = runHandoffPendingAttempt
var runClaimedAttemptBatchForWorker = runClaimedAttemptBatch

func runPlanWorkerProcess(client linearClient, config runnerConfig) error {
	ctx := context.Background()
	stateStore, stateDBPath := commandScopedStateStore(ctx, config.WorkspaceRoot, "worker-plan")
	if stateStore == nil {
		return fmt.Errorf("SQLite state store unavailable for plan worker at %s", stateDBPath)
	}
	defer stateStore.Close()
	recordHeartbeat := daemonHeartbeatRecorder(ctx, config, stateStore)
	didWork, err := runContinuousWorkerTask(ctx, stateStore, continuousWorkerTask{
		TaskKey:   "process:plan",
		Role:      planWorkerRole,
		LaneName:  "worker:plan",
		LeaseName: "worker:plan",
		Payload:   map[string]any{"project_slug": config.ProjectSlug, "active_states": config.ActiveStates},
	}, func() (bool, error) {
		return true, printPlanForWorker(client, config)
	})
	recordContinuousHeartbeat(recordHeartbeat, continuousHeartbeat{LaneName: "worker:plan", CycleNumber: 1, Success: err == nil && didWork, Err: err, At: stateNow()})
	return err
}

func runCleanupWorkerProcess(client linearClient, config runnerConfig) error {
	ctx := context.Background()
	stateStore, stateDBPath := commandScopedStateStore(ctx, config.WorkspaceRoot, "worker-cleanup")
	if stateStore == nil {
		return fmt.Errorf("SQLite state store unavailable for cleanup worker at %s", stateDBPath)
	}
	defer stateStore.Close()
	recordHeartbeat := daemonHeartbeatRecorder(ctx, config, stateStore)
	didWork, err := runContinuousWorkerTask(ctx, stateStore, continuousWorkerTask{
		TaskKey:   "process:cleanup",
		Role:      cleanupWorkerRole,
		LaneName:  "worker:cleanup",
		LeaseName: "worker:cleanup",
		Payload:   map[string]any{"project_slug": config.ProjectSlug, "done_state": config.DoneState, "apply": true},
	}, func() (bool, error) {
		doneIssues, err := issueIdentifiersByStateForCleanupWorker(client, config.ProjectSlug, config.DoneState)
		if err != nil {
			return false, err
		}
		if err := cleanupWorkspacesForWorker(config.WorkspaceRoot, cleanupOptions{Apply: true, DoneIssues: doneIssues, StateStore: stateStore}); err != nil {
			return false, err
		}
		return true, nil
	})
	recordContinuousHeartbeat(recordHeartbeat, continuousHeartbeat{LaneName: "worker:cleanup", CycleNumber: 1, Success: err == nil && didWork, Err: err, At: stateNow()})
	return err
}

func runMergeWorkerProcess(client linearClient, config runnerConfig) error {
	ctx := context.Background()
	stateStore, stateDBPath := commandScopedStateStore(ctx, config.WorkspaceRoot, "worker-merge")
	if stateStore == nil {
		return fmt.Errorf("SQLite state store unavailable for merge worker at %s", stateDBPath)
	}
	defer stateStore.Close()
	recordHeartbeat := daemonHeartbeatRecorder(ctx, config, stateStore)
	didWork, err := runContinuousWorkerTask(ctx, stateStore, continuousWorkerTask{
		TaskKey:   "process:merge",
		Role:      mergeWorkerRole,
		LaneName:  "worker:merge",
		LeaseName: "worker:merge",
		Payload:   map[string]any{"project_slug": config.ProjectSlug, "done_state": config.DoneState, "apply": true},
	}, func() (bool, error) {
		doneIssues, err := issueIdentifiersByStateForMergeWorker(client, config.ProjectSlug, config.DoneState)
		if err != nil {
			return false, err
		}
		if err := cleanupWorkspacesForWorker(config.WorkspaceRoot, cleanupOptions{Apply: true, DoneIssues: doneIssues, StateStore: stateStore}); err != nil {
			return false, err
		}
		if err := mergeApprovedPRsForWorker(client, config); err != nil {
			return false, err
		}
		return true, nil
	})
	recordContinuousHeartbeat(recordHeartbeat, continuousHeartbeat{LaneName: "worker:merge", CycleNumber: 1, Success: err == nil && didWork, Err: err, At: stateNow()})
	return err
}

func runReviewWorkerProcess(client linearClient, wf workflow, config runnerConfig) error {
	ctx := context.Background()
	stateStore, stateDBPath := commandScopedStateStore(ctx, config.WorkspaceRoot, "worker-review")
	if stateStore == nil {
		return fmt.Errorf("SQLite state store unavailable for review worker at %s", stateDBPath)
	}
	defer stateStore.Close()
	recordHeartbeat := daemonHeartbeatRecorder(ctx, config, stateStore)
	didWork, err := runContinuousWorkerTask(ctx, stateStore, continuousWorkerTask{
		TaskKey:   "process:review",
		Role:      reviewWorkerRole,
		LaneName:  "worker:review",
		LeaseName: "worker:review",
		Payload:   map[string]any{"project_slug": config.ProjectSlug, "review_configured": strings.TrimSpace(config.ReviewCommand) != ""},
	}, func() (bool, error) {
		return runReviewReadyAttemptForWorker(client, wf, config, stateStore)
	})
	recordContinuousHeartbeat(recordHeartbeat, continuousHeartbeat{LaneName: "worker:review", CycleNumber: 1, Success: err == nil && didWork, Err: err, At: stateNow()})
	return err
}

func runImplementationWorkerProcess(client linearClient, wf workflow, config runnerConfig) error {
	ctx := context.Background()
	stateStore, stateDBPath := commandScopedStateStore(ctx, config.WorkspaceRoot, "worker-implementation")
	if stateStore == nil {
		return fmt.Errorf("SQLite state store unavailable for implementation worker at %s", stateDBPath)
	}
	defer stateStore.Close()
	recordHeartbeat := daemonHeartbeatRecorder(ctx, config, stateStore)
	didWork, err := runContinuousWorkerTask(ctx, stateStore, continuousWorkerTask{
		TaskKey:   "process:implementation",
		Role:      implementationWorkerRole,
		LaneName:  "worker:implementation",
		LeaseName: "worker:implementation",
		Payload:   map[string]any{"project_slug": config.ProjectSlug, "review_ready_resumes_skipped": true},
	}, func() (bool, error) {
		return runImplementationAttemptForWorker(client, wf, config, stateStore)
	})
	recordContinuousHeartbeat(recordHeartbeat, continuousHeartbeat{LaneName: "worker:implementation", CycleNumber: 1, Success: err == nil && didWork, Err: err, At: stateNow()})
	return err
}

func runHandoffWorkerProcess(client linearClient, config runnerConfig) error {
	ctx := context.Background()
	stateStore, stateDBPath := commandScopedStateStore(ctx, config.WorkspaceRoot, "worker-handoff")
	if stateStore == nil {
		return fmt.Errorf("SQLite state store unavailable for handoff worker at %s", stateDBPath)
	}
	defer stateStore.Close()
	recordHeartbeat := daemonHeartbeatRecorder(ctx, config, stateStore)
	didWork, err := runContinuousWorkerTask(ctx, stateStore, continuousWorkerTask{
		TaskKey:   "process:handoff",
		Role:      handoffWorkerRole,
		LaneName:  "worker:handoff",
		LeaseName: "worker:handoff",
		Payload:   map[string]any{"project_slug": config.ProjectSlug, "handoff_state": config.HandoffState},
	}, func() (bool, error) {
		return runHandoffPendingAttemptForWorker(client, config, stateStore)
	})
	recordContinuousHeartbeat(recordHeartbeat, continuousHeartbeat{LaneName: "worker:handoff", CycleNumber: 1, Success: err == nil && didWork, Err: err, At: stateNow()})
	return err
}

func runWorkWorkerProcess(client linearClient, wf workflow, config runnerConfig) error {
	ctx := context.Background()
	stateStore, stateDBPath := commandScopedStateStore(ctx, config.WorkspaceRoot, "worker-work")
	if stateStore == nil {
		return fmt.Errorf("SQLite state store unavailable for work worker at %s", stateDBPath)
	}
	defer stateStore.Close()
	maxConcurrentAgents := configuredMaxConcurrentAgents(wf)
	recordHeartbeat := daemonHeartbeatRecorder(ctx, config, stateStore)
	didWork, err := runContinuousWorkerTask(ctx, stateStore, continuousWorkerTask{
		TaskKey:   "process:work",
		Role:      "scheduler",
		LaneName:  "worker:work",
		LeaseName: "worker:work",
		Payload:   map[string]any{"project_slug": config.ProjectSlug, "max_concurrent_agents": maxConcurrentAgents},
	}, func() (bool, error) {
		return runClaimedAttemptBatchForWorker(client, wf, config, stateStore, maxConcurrentAgents)
	})
	recordContinuousHeartbeat(recordHeartbeat, continuousHeartbeat{LaneName: "worker:work", CycleNumber: 1, Success: err == nil && didWork, Err: err, At: stateNow()})
	return err
}

var stateNow = func() time.Time {
	return time.Now().UTC()
}
