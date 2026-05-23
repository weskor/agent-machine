package main

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const statusWorkerRole = "status"

func runSelectedWorker(client linearClient, _ workflow, config runnerConfig, role string) error {
	role = strings.TrimSpace(role)
	switch role {
	case statusWorkerRole:
		return runStatusWorkerProcess(client, config)
	default:
		return fmt.Errorf("unsupported worker role %q; supported roles: %s", role, statusWorkerRole)
	}
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

var stateNow = func() time.Time {
	return time.Now().UTC()
}
