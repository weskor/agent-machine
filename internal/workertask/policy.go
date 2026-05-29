package workertask

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/weskor/agent-machine/internal/state"
)

const (
	RoleStatus         = "status"
	RolePlan           = "plan"
	RoleScheduler      = "scheduler"
	RoleCleanup        = "cleanup"
	RoleMerge          = "merge"
	RoleReconciliation = "reconciliation"
	RoleReview         = "review"
	RoleImplementation = "implementation"
	RoleHandoff        = "handoff"
	RoleLinearStatus   = "linear-status"
	RoleWork           = "work"

	RepairReasonOperatorRequeuedReconciliationTask = "operator_requeued_reconciliation_task"
)

func SupportedSelectedRole(role string) bool {
	role = NormalizeRole(role)
	for _, supported := range SupportedSelectedRoles() {
		if role == supported {
			return true
		}
	}
	return false
}

func RequireSupportedSelectedRole(role string) error {
	role = NormalizeRole(role)
	if SupportedSelectedRole(role) {
		return nil
	}
	return fmt.Errorf("unsupported worker role %q; supported roles: %s", role, strings.Join(SupportedSelectedRoles(), ", "))
}

func SelectedRoleNeedsWorkspaceRoot(role string) bool {
	switch NormalizeRole(role) {
	case RoleCleanup, RoleMerge, RoleReconciliation, RoleReview, RoleImplementation, RoleHandoff, RoleWork:
		return true
	default:
		return false
	}
}

func SupportedSelectedRoles() []string {
	return []string{RoleStatus, RolePlan, RoleCleanup, RoleMerge, RoleReconciliation, RoleReview, RoleImplementation, RoleHandoff, RoleLinearStatus, RoleWork}
}

func BlocksDispatch(status string) bool {
	switch status {
	case state.WorkerTaskStatusQueued, state.WorkerTaskStatusClaimed, state.WorkerTaskStatusReconciliationNeeded:
		return true
	default:
		return false
	}
}

func NormalizeRole(role string) string {
	return strings.TrimSpace(role)
}

func RetryBackoff(role string) time.Duration {
	switch role {
	case RoleImplementation, RoleWork:
		return 5 * time.Minute
	case RoleReview, RoleHandoff, RoleMerge:
		return 2 * time.Minute
	case RoleScheduler, RoleCleanup, RoleLinearStatus, RoleReconciliation, RoleStatus, RolePlan:
		return time.Minute
	default:
		return time.Minute
	}
}

func AvailableAtAfterLatestFailure(ctx context.Context, store *state.Store, taskKey, role string, now time.Time) (time.Time, error) {
	if store == nil || taskKey == "" || role == "" {
		return now, nil
	}
	results, err := store.WorkerResults(ctx, role)
	if err != nil {
		return time.Time{}, err
	}
	for _, result := range results {
		if result.TaskKey != taskKey {
			continue
		}
		if result.Status != state.WorkerTaskStatusFailed {
			return now, nil
		}
		finishedAt := result.FinishedAt
		if finishedAt.IsZero() {
			finishedAt = result.UpdatedAt
		}
		availableAt := finishedAt.Add(RetryBackoff(role))
		if availableAt.After(now) {
			return availableAt, nil
		}
		return now, nil
	}
	return now, nil
}

func RequeueReconciliationNeeded(ctx context.Context, store *state.Store, taskKey string, now time.Time) (state.WorkerTask, error) {
	taskKey = strings.TrimSpace(taskKey)
	if taskKey == "" {
		return state.WorkerTask{}, fmt.Errorf("repair worker task: task key is required")
	}
	if store == nil {
		return state.WorkerTask{}, fmt.Errorf("repair worker task: state store is required")
	}
	return store.RequeueReconciliationNeededWorkerTask(ctx, taskKey, RepairReasonOperatorRequeuedReconciliationTask, now)
}
