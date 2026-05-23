package main

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const statusWorkerRole = "status"
const cleanupWorkerRole = "cleanup"

func runSelectedWorker(client linearClient, _ workflow, config runnerConfig, role string) error {
	role = strings.TrimSpace(role)
	switch role {
	case statusWorkerRole:
		return runStatusWorkerProcess(client, config)
	case cleanupWorkerRole:
		return runCleanupWorkerProcess(client, config)
	default:
		return fmt.Errorf("unsupported worker role %q; supported roles: %s", role, strings.Join(supportedWorkerRoles(), ", "))
	}
}

func supportedWorkerRoles() []string {
	return []string{statusWorkerRole, cleanupWorkerRole}
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
var cleanupWorkspacesForWorker = cleanupWorkspaces
var issueIdentifiersByStateForCleanupWorker = func(client linearClient, projectSlug, state string) (map[string]bool, error) {
	return client.issueIdentifiersByState(projectSlug, state)
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

var stateNow = func() time.Time {
	return time.Now().UTC()
}
