package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/weskor/agent-machine/internal/state"
	"github.com/weskor/agent-machine/internal/workertask"
)

const statusWorkerRole = workertask.RoleStatus
const planWorkerRole = workertask.RolePlan
const schedulerWorkerRole = workertask.RoleScheduler
const cleanupWorkerRole = workertask.RoleCleanup
const mergeWorkerRole = workertask.RoleMerge
const reconciliationWorkerRole = workertask.RoleReconciliation
const reviewWorkerRole = workertask.RoleReview
const implementationWorkerRole = workertask.RoleImplementation
const handoffWorkerRole = workertask.RoleHandoff
const linearStatusWorkerRole = workertask.RoleLinearStatus
const workWorkerRole = workertask.RoleWork

func runSelectedWorker(client linearClient, proj project, config runnerConfig, role string) error {
	return runSelectedWorkerContext(context.Background(), client, proj, config, role)
}

func runSelectedWorkerContext(ctx context.Context, client linearClient, proj project, config runnerConfig, role string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	role = workertask.NormalizeRole(role)
	if err := workertask.RequireSupportedSelectedRole(role); err != nil {
		return err
	}
	if selectedWorkerNeedsWorkspaceRoot(role) {
		if _, err := ensureWorkspaceRoot(config.WorkspaceRoot); err != nil {
			return fmt.Errorf("workspace root preflight failed for worker mode: %w", err)
		}
	}
	switch role {
	case statusWorkerRole:
		return runStatusWorkerProcessContext(ctx, client, config)
	case planWorkerRole:
		return runPlanWorkerProcessContext(ctx, client, config)
	case cleanupWorkerRole:
		return runCleanupWorkerProcessContext(ctx, client, config)
	case mergeWorkerRole:
		return runMergeWorkerProcessContext(ctx, client, config)
	case reconciliationWorkerRole:
		return runReconciliationWorkerProcessContext(ctx, client, config)
	case reviewWorkerRole:
		return runReviewWorkerProcessContext(ctx, client, proj, config)
	case implementationWorkerRole:
		return runImplementationWorkerProcessContext(ctx, client, proj, config)
	case handoffWorkerRole:
		return runHandoffWorkerProcessContext(ctx, client, config)
	case linearStatusWorkerRole:
		return runLinearStatusWorkerProcessContext(ctx, client, config)
	case workWorkerRole:
		return runWorkWorkerProcessContext(ctx, client, proj, config)
	default:
		return workertask.RequireSupportedSelectedRole(role)
	}
}

func supportedWorkerRole(role string) bool {
	return workertask.SupportedSelectedRole(role)
}

func selectedWorkerNeedsWorkspaceRoot(role string) bool {
	return workertask.SelectedRoleNeedsWorkspaceRoot(role)
}

func supportedWorkerRoles() []string {
	return workertask.SupportedSelectedRoles()
}

func runStatusWorkerProcess(client linearClient, config runnerConfig) error {
	return runStatusWorkerProcessContext(context.Background(), client, config)
}

func runStatusWorkerProcessContext(ctx context.Context, client linearClient, config runnerConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	stateStore, stateDBPath := commandScopedStateStore(ctx, config.WorkspaceRoot, "worker-status")
	if stateStore == nil {
		return fmt.Errorf("SQLite state store unavailable for status worker at %s", stateDBPath)
	}
	defer stateStore.Close()
	recordHeartbeat := daemonHeartbeatRecorder(ctx, config, stateStore)
	didWork, err := runContinuousWorkerTask(ctx, stateStore, continuousWorkerTask{
		TaskKey:         "process:status",
		Role:            statusWorkerRole,
		LaneName:        "worker:status",
		LeaseName:       "worker:status",
		Payload:         map[string]any{"project_slug": config.ProjectSlug},
		RecordHeartbeat: recordHeartbeat,
	}, func(context.Context) (bool, error) {
		return true, printStatusForWorker(client, config)
	})
	recordContinuousHeartbeat(recordHeartbeat, continuousHeartbeat{LaneName: "worker:status", CycleNumber: 1, Success: err == nil && didWork, Err: err, At: stateNow()})
	return err
}

var printStatusForWorker = printStatus
var printPlanForWorker = printExplain
var cleanupWorkspacesForWorker = cleanupWorkspacesContext
var issueIdentifiersByStateForCleanupWorker = func(ctx context.Context, client linearClient, projectSlug, state string) (map[string]bool, error) {
	return client.issueIdentifiersByStateContext(ctx, projectSlug, state)
}
var mergeApprovedPRsForWorker = mergeApprovedPRsWithStoreContext
var runReconciliationScanForWorker = runReconciliationScanContext
var runReviewReadyAttemptForWorker = runReviewReadyAttemptContext
var runImplementationAttemptForWorker = func(ctx context.Context, client linearClient, proj project, config runnerConfig, stateStore *state.Store) (bool, error) {
	return runImplementationAttemptBatchContext(ctx, client, proj, config, stateStore, 1)
}
var runHandoffPendingAttemptForWorker = runHandoffPendingAttemptContext
var runLinearStatusTransitionTaskForWorker = runLinearStatusTransitionTaskContext
var runImplementationAttemptBatchForWorkWorker = runImplementationAttemptBatchContext

func runPlanWorkerProcess(client linearClient, config runnerConfig) error {
	return runPlanWorkerProcessContext(context.Background(), client, config)
}

func runPlanWorkerProcessContext(ctx context.Context, client linearClient, config runnerConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	stateStore, stateDBPath := commandScopedStateStore(ctx, config.WorkspaceRoot, "worker-plan")
	if stateStore == nil {
		return fmt.Errorf("SQLite state store unavailable for plan worker at %s", stateDBPath)
	}
	defer stateStore.Close()
	recordHeartbeat := daemonHeartbeatRecorder(ctx, config, stateStore)
	didWork, err := runContinuousWorkerTask(ctx, stateStore, continuousWorkerTask{
		TaskKey:         "process:plan",
		Role:            planWorkerRole,
		LaneName:        "worker:plan",
		LeaseName:       "worker:plan",
		Payload:         map[string]any{"project_slug": config.ProjectSlug, "active_states": config.ActiveStates},
		RecordHeartbeat: recordHeartbeat,
	}, func(context.Context) (bool, error) {
		return true, printPlanForWorker(client, config)
	})
	recordContinuousHeartbeat(recordHeartbeat, continuousHeartbeat{LaneName: "worker:plan", CycleNumber: 1, Success: err == nil && didWork, Err: err, At: stateNow()})
	return err
}

func runCleanupWorkerProcess(client linearClient, config runnerConfig) error {
	return runCleanupWorkerProcessContext(context.Background(), client, config)
}

func runCleanupWorkerProcessContext(ctx context.Context, client linearClient, config runnerConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	stateStore, stateDBPath := commandScopedStateStore(ctx, config.WorkspaceRoot, "worker-cleanup")
	if stateStore == nil {
		return fmt.Errorf("SQLite state store unavailable for cleanup worker at %s", stateDBPath)
	}
	defer stateStore.Close()
	recordHeartbeat := daemonHeartbeatRecorder(ctx, config, stateStore)
	didWork, err := runContinuousWorkerTask(ctx, stateStore, continuousWorkerTask{
		TaskKey:         "process:cleanup",
		Role:            cleanupWorkerRole,
		LaneName:        "worker:cleanup",
		LeaseName:       "worker:cleanup",
		Payload:         map[string]any{"project_slug": config.ProjectSlug, "done_state": config.DoneState, "apply": true},
		RecordHeartbeat: recordHeartbeat,
	}, func(runCtx context.Context) (bool, error) {
		select {
		case <-runCtx.Done():
			return false, runCtx.Err()
		default:
		}
		doneIssues, err := issueIdentifiersByStateForCleanupWorker(runCtx, client, config.ProjectSlug, config.DoneState)
		if err != nil {
			return false, err
		}
		if err := cleanupWorkspacesForWorker(runCtx, config.WorkspaceRoot, cleanupOptions{Apply: true, DoneIssues: doneIssues, StateStore: stateStore}); err != nil {
			return false, err
		}
		return true, nil
	})
	recordContinuousHeartbeat(recordHeartbeat, continuousHeartbeat{LaneName: "worker:cleanup", CycleNumber: 1, Success: err == nil && didWork, Err: err, At: stateNow()})
	return err
}

func runMergeWorkerProcess(client linearClient, config runnerConfig) error {
	return runMergeWorkerProcessContext(context.Background(), client, config)
}

func runMergeWorkerProcessContext(ctx context.Context, client linearClient, config runnerConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	stateStore, stateDBPath := commandScopedStateStore(ctx, config.WorkspaceRoot, "worker-merge")
	if stateStore == nil {
		return fmt.Errorf("SQLite state store unavailable for merge worker at %s", stateDBPath)
	}
	defer stateStore.Close()
	recordHeartbeat := daemonHeartbeatRecorder(ctx, config, stateStore)
	didWork, err := runContinuousWorkerTask(ctx, stateStore, continuousWorkerTask{
		TaskKey:         "process:merge",
		Role:            mergeWorkerRole,
		LaneName:        "worker:merge",
		LeaseName:       "worker:merge",
		Payload:         map[string]any{"project_slug": config.ProjectSlug, "handoff_state": config.HandoffState},
		RecordHeartbeat: recordHeartbeat,
	}, func(runCtx context.Context) (bool, error) {
		select {
		case <-runCtx.Done():
			return false, runCtx.Err()
		default:
		}
		if err := mergeApprovedPRsForWorker(runCtx, client, config, stateStore); err != nil {
			return false, err
		}
		return true, nil
	})
	recordContinuousHeartbeat(recordHeartbeat, continuousHeartbeat{LaneName: "worker:merge", CycleNumber: 1, Success: err == nil && didWork, Err: err, At: stateNow()})
	return err
}

func runReconciliationWorkerProcess(client linearClient, config runnerConfig) error {
	return runReconciliationWorkerProcessContext(context.Background(), client, config)
}

func runReconciliationWorkerProcessContext(ctx context.Context, client linearClient, config runnerConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	stateStore, stateDBPath := commandScopedStateStore(ctx, config.WorkspaceRoot, "worker-reconciliation")
	if stateStore == nil {
		return fmt.Errorf("SQLite state store unavailable for reconciliation worker at %s", stateDBPath)
	}
	defer stateStore.Close()
	recordHeartbeat := daemonHeartbeatRecorder(ctx, config, stateStore)
	didWork, err := runContinuousWorkerTask(ctx, stateStore, continuousWorkerTask{
		TaskKey:         "process:reconciliation",
		Role:            reconciliationWorkerRole,
		LaneName:        "worker:reconciliation",
		LeaseName:       "worker:reconciliation",
		Payload:         map[string]any{"project_slug": config.ProjectSlug},
		RecordHeartbeat: recordHeartbeat,
	}, func(runCtx context.Context) (bool, error) {
		return runReconciliationScanForWorker(runCtx, client, config, stateStore)
	})
	recordContinuousHeartbeat(recordHeartbeat, continuousHeartbeat{LaneName: "worker:reconciliation", CycleNumber: 1, Success: err == nil && didWork, Err: err, At: stateNow()})
	return err
}

func runReviewWorkerProcess(client linearClient, proj project, config runnerConfig) error {
	return runReviewWorkerProcessContext(context.Background(), client, proj, config)
}

func runReviewWorkerProcessContext(ctx context.Context, client linearClient, proj project, config runnerConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	stateStore, stateDBPath := commandScopedStateStore(ctx, config.WorkspaceRoot, "worker-review")
	if stateStore == nil {
		return fmt.Errorf("SQLite state store unavailable for review worker at %s", stateDBPath)
	}
	defer stateStore.Close()
	recordHeartbeat := daemonHeartbeatRecorder(ctx, config, stateStore)
	didWork, err := runContinuousWorkerTask(ctx, stateStore, continuousWorkerTask{
		TaskKey:         "process:review",
		Role:            reviewWorkerRole,
		LaneName:        "worker:review",
		LeaseName:       "worker:review",
		Payload:         map[string]any{"project_slug": config.ProjectSlug, "review_configured": strings.TrimSpace(config.ReviewCommand) != ""},
		RecordHeartbeat: recordHeartbeat,
	}, func(runCtx context.Context) (bool, error) {
		return runReviewReadyAttemptForWorker(runCtx, client, proj, config, stateStore)
	})
	recordContinuousHeartbeat(recordHeartbeat, continuousHeartbeat{LaneName: "worker:review", CycleNumber: 1, Success: err == nil && didWork, Err: err, At: stateNow()})
	return err
}

func runImplementationWorkerProcess(client linearClient, proj project, config runnerConfig) error {
	return runImplementationWorkerProcessContext(context.Background(), client, proj, config)
}

func runImplementationWorkerProcessContext(ctx context.Context, client linearClient, proj project, config runnerConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	stateStore, stateDBPath := commandScopedStateStore(ctx, config.WorkspaceRoot, "worker-implementation")
	if stateStore == nil {
		return fmt.Errorf("SQLite state store unavailable for implementation worker at %s", stateDBPath)
	}
	defer stateStore.Close()
	recordHeartbeat := daemonHeartbeatRecorder(ctx, config, stateStore)
	didWork, err := runContinuousWorkerTask(ctx, stateStore, continuousWorkerTask{
		TaskKey:         "process:implementation",
		Role:            implementationWorkerRole,
		LaneName:        "worker:implementation",
		LeaseName:       "worker:implementation",
		Payload:         map[string]any{"project_slug": config.ProjectSlug, "review_ready_resumes_skipped": true},
		RecordHeartbeat: recordHeartbeat,
	}, func(runCtx context.Context) (bool, error) {
		return runImplementationAttemptForWorker(runCtx, client, proj, config, stateStore)
	})
	recordContinuousHeartbeat(recordHeartbeat, continuousHeartbeat{LaneName: "worker:implementation", CycleNumber: 1, Success: err == nil && didWork, Err: err, At: stateNow()})
	return err
}

func runHandoffWorkerProcess(client linearClient, config runnerConfig) error {
	return runHandoffWorkerProcessContext(context.Background(), client, config)
}

func runHandoffWorkerProcessContext(ctx context.Context, client linearClient, config runnerConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	stateStore, stateDBPath := commandScopedStateStore(ctx, config.WorkspaceRoot, "worker-handoff")
	if stateStore == nil {
		return fmt.Errorf("SQLite state store unavailable for handoff worker at %s", stateDBPath)
	}
	defer stateStore.Close()
	recordHeartbeat := daemonHeartbeatRecorder(ctx, config, stateStore)
	didWork, err := runContinuousWorkerTask(ctx, stateStore, continuousWorkerTask{
		TaskKey:         "process:handoff",
		Role:            handoffWorkerRole,
		LaneName:        "worker:handoff",
		LeaseName:       "worker:handoff",
		Payload:         map[string]any{"project_slug": config.ProjectSlug, "handoff_state": config.HandoffState},
		RecordHeartbeat: recordHeartbeat,
	}, func(runCtx context.Context) (bool, error) {
		return runHandoffPendingAttemptForWorker(runCtx, client, config, stateStore)
	})
	recordContinuousHeartbeat(recordHeartbeat, continuousHeartbeat{LaneName: "worker:handoff", CycleNumber: 1, Success: err == nil && didWork, Err: err, At: stateNow()})
	return err
}

func runLinearStatusWorkerProcess(client linearClient, config runnerConfig) error {
	return runLinearStatusWorkerProcessContext(context.Background(), client, config)
}

func runLinearStatusWorkerProcessContext(ctx context.Context, client linearClient, config runnerConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	stateStore, stateDBPath := commandScopedStateStore(ctx, config.WorkspaceRoot, "worker-linear-status")
	if stateStore == nil {
		return fmt.Errorf("SQLite state store unavailable for Linear status worker at %s", stateDBPath)
	}
	defer stateStore.Close()
	recordHeartbeat := daemonHeartbeatRecorder(ctx, config, stateStore)
	didWork, err := runContinuousWorkerTask(ctx, stateStore, continuousWorkerTask{
		TaskKey:         "process:linear-status",
		Role:            linearStatusWorkerRole,
		LaneName:        "worker:linear-status",
		LeaseName:       "worker:linear-status",
		Payload:         map[string]any{"project_slug": config.ProjectSlug},
		RecordHeartbeat: recordHeartbeat,
	}, func(runCtx context.Context) (bool, error) {
		return runLinearStatusTransitionTaskForWorker(runCtx, client, config, stateStore)
	})
	recordContinuousHeartbeat(recordHeartbeat, continuousHeartbeat{LaneName: "worker:linear-status", CycleNumber: 1, Success: err == nil && didWork, Err: err, At: stateNow()})
	return err
}

func runWorkWorkerProcess(client linearClient, proj project, config runnerConfig) error {
	return runWorkWorkerProcessContext(context.Background(), client, proj, config)
}

func runWorkWorkerProcessContext(ctx context.Context, client linearClient, proj project, config runnerConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	stateStore, stateDBPath := commandScopedStateStore(ctx, config.WorkspaceRoot, "worker-work")
	if stateStore == nil {
		return fmt.Errorf("SQLite state store unavailable for work worker at %s", stateDBPath)
	}
	defer stateStore.Close()
	maxConcurrentAgents := configuredMaxConcurrentAgents(proj)
	recordHeartbeat := daemonHeartbeatRecorder(ctx, config, stateStore)
	didWork, err := runContinuousWorkerTask(ctx, stateStore, continuousWorkerTask{
		TaskKey:         "process:work",
		Role:            implementationWorkerRole,
		LaneName:        "worker:work",
		LeaseName:       "worker:work",
		Payload:         map[string]any{"project_slug": config.ProjectSlug, "max_concurrent_agents": maxConcurrentAgents, "compatibility_role": workWorkerRole},
		RecordHeartbeat: recordHeartbeat,
	}, func(runCtx context.Context) (bool, error) {
		return runImplementationAttemptBatchForWorkWorker(runCtx, client, proj, config, stateStore, maxConcurrentAgents)
	})
	recordContinuousHeartbeat(recordHeartbeat, continuousHeartbeat{LaneName: "worker:work", CycleNumber: 1, Success: err == nil && didWork, Err: err, At: stateNow()})
	return err
}

var stateNow = func() time.Time {
	return time.Now().UTC()
}
