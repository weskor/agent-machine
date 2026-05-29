package cleanupworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	cleanuppolicy "github.com/weskor/agent-machine/internal/cleanup"
	"github.com/weskor/agent-machine/internal/state"
	"github.com/weskor/agent-machine/internal/workertask"
)

type Config struct {
	ProjectSlug   string
	DoneState     string
	WorkspaceRoot string
}

type Payload struct {
	Phase         string `json:"phase"`
	WorkspaceName string `json:"workspace_name"`
	WorkspacePath string `json:"workspace_path"`
}

type WorkspaceChangeChecker func(string) (bool, error)

type Dependencies struct {
	SafeWorkspaceRoot    func(string) (string, error)
	SafeWorkspacePath    func(string, string) (string, error)
	AssertSafeDeletePath func(string, string) error
	DoneIssues           func(context.Context, string, string) (map[string]bool, error)
	Decision             func(context.Context, string, string, map[string]bool, *state.Store, WorkspaceChangeChecker) (cleanuppolicy.Decision, error)
	WorkspaceHasChanges  WorkspaceChangeChecker
	MirrorState          func(*state.Store, string, cleanuppolicy.Decision, bool, string, bool)
	RecordCleanupEvent   func(context.Context, *state.Store, string, cleanuppolicy.Decision, map[string]any)
	RecordCleanupError   func(context.Context, *state.Store, cleanuppolicy.Decision, error)
	RecordTaskEvent      func(context.Context, *state.Store, string, state.WorkerTask, map[string]any)
	Logf                 func(string, ...any)
}

func TaskKey(workspaceName string) string {
	return fmt.Sprintf("%s:%s", workertask.RoleCleanup, workspaceName)
}

func Schedule(ctx context.Context, config Config, store *state.Store, deps Dependencies) (bool, error) {
	if store == nil {
		return false, fmt.Errorf("SQLite state store unavailable for cleanup scheduling at %s", state.DefaultDBPath(config.WorkspaceRoot))
	}
	safeRoot, err := deps.safeWorkspaceRoot(config.WorkspaceRoot)
	if err != nil {
		return false, err
	}
	entries, err := os.ReadDir(safeRoot)
	if err != nil {
		return false, err
	}
	didWork := false
	now := time.Now().UTC()
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || entry.Name() == "state" {
			continue
		}
		workspace, err := deps.safeWorkspacePath(safeRoot, entry.Name())
		if err != nil {
			return didWork, err
		}
		if _, enqueued, err := Enqueue(ctx, store, entry.Name(), workspace, now, deps); err != nil {
			return didWork, err
		} else if enqueued {
			didWork = true
		}
	}
	return didWork, nil
}

func Enqueue(ctx context.Context, store *state.Store, workspaceName, workspace string, now time.Time, deps Dependencies) (state.WorkerTask, bool, error) {
	if store == nil {
		return state.WorkerTask{}, false, nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	taskKey := TaskKey(workspaceName)
	tasks, err := store.WorkerTasks(ctx, workertask.RoleCleanup)
	if err != nil {
		return state.WorkerTask{}, false, err
	}
	for _, task := range tasks {
		if task.TaskKey != taskKey {
			continue
		}
		if workertask.BlocksDispatch(task.Status) {
			return task, false, nil
		}
		break
	}
	availableAt, err := workertask.AvailableAtAfterLatestFailure(ctx, store, taskKey, workertask.RoleCleanup, now)
	if err != nil {
		return state.WorkerTask{}, false, err
	}
	payload, err := json.Marshal(Payload{
		Phase:         "cleanup",
		WorkspaceName: workspaceName,
		WorkspacePath: workspace,
	})
	if err != nil {
		return state.WorkerTask{}, false, fmt.Errorf("encode cleanup worker task payload: %w", err)
	}
	task := state.WorkerTask{
		TaskKey:     taskKey,
		Role:        workertask.RoleCleanup,
		IssueKey:    workspaceName,
		Attempt:     1,
		Status:      "queued",
		AvailableAt: availableAt,
		LeaseName:   "worker:cleanup:" + workspaceName,
		Payload:     payload,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := store.UpsertWorkerTask(ctx, task); err != nil {
		return state.WorkerTask{}, false, err
	}
	deps.recordTaskEvent(ctx, store, state.EventWorkerTaskQueued, task, map[string]any{"lane": "cleanup", "workspace": workspace})
	return task, true, nil
}

func Claim(ctx context.Context, store *state.Store, now time.Time, deps Dependencies) (state.WorkerTask, bool, error) {
	if store == nil {
		return state.WorkerTask{}, false, nil
	}
	claimed, ok, err := store.ClaimNextWorkerTask(ctx, workertask.RoleCleanup, now)
	if err != nil || !ok {
		return claimed, ok, err
	}
	if claimed.TaskKey == "continuous:cleanup" || claimed.TaskKey == "process:cleanup" {
		if err := store.CompleteWorkerTask(ctx, claimed.TaskKey, "completed", now); err != nil {
			return state.WorkerTask{}, false, err
		}
		return state.WorkerTask{}, false, nil
	}
	deps.recordTaskEvent(ctx, store, state.EventWorkerTaskClaimed, claimed, map[string]any{"lane": "cleanup", "issue_key": claimed.IssueKey})
	return claimed, true, nil
}

func RunQueued(ctx context.Context, config Config, store *state.Store, deps Dependencies) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if store == nil {
		return false, fmt.Errorf("SQLite state store unavailable for cleanup worker at %s", state.DefaultDBPath(config.WorkspaceRoot))
	}
	now := time.Now().UTC()
	task, ok, err := Claim(ctx, store, now, deps)
	if err != nil || !ok {
		return false, err
	}
	startedAt := time.Now().UTC()
	payload := Payload{WorkspaceName: task.IssueKey}
	if len(task.Payload) > 0 {
		_ = json.Unmarshal(task.Payload, &payload)
	}
	finish := func(status, reason, errorText string) error {
		return Complete(ctx, store, task, status, true, reason, errorText, startedAt, time.Now().UTC(), deps)
	}
	safeRoot, err := deps.safeWorkspaceRoot(config.WorkspaceRoot)
	if err != nil {
		return true, errors.Join(err, finish("failed", "unsafe_workspace_root", err.Error()))
	}
	workspaceName := firstNonEmpty(payload.WorkspaceName, task.IssueKey)
	workspace, err := deps.safeWorkspacePath(safeRoot, workspaceName)
	if err != nil {
		return true, errors.Join(err, finish("failed", "unsafe_workspace_path", err.Error()))
	}
	if payload.WorkspacePath != "" && filepath.Clean(payload.WorkspacePath) != filepath.Clean(workspace) {
		err := fmt.Errorf("cleanup task workspace %s conflicts with expected workspace %s", payload.WorkspacePath, workspace)
		return true, errors.Join(err, finish("failed", "workspace_path_conflict", err.Error()))
	}
	doneIssues, err := deps.doneIssues(ctx, config.ProjectSlug, config.DoneState)
	if err != nil {
		return true, errors.Join(err, finish("failed", "linear_done_refresh_failed", err.Error()))
	}
	if _, err := os.Stat(workspace); err != nil {
		if os.IsNotExist(err) {
			return true, finish("completed", "workspace_missing", "")
		}
		return true, errors.Join(err, finish("failed", "workspace_stat_failed", err.Error()))
	}
	decision, err := deps.decision(ctx, safeRoot, workspace, doneIssues, store, deps.WorkspaceHasChanges)
	if err != nil {
		deps.recordCleanupError(ctx, store, cleanuppolicy.Decision{IssueIdentifier: workspaceName, WorkspacePath: workspace}, err)
		return true, errors.Join(err, finish("failed", "cleanup_decision_failed", err.Error()))
	}
	deps.recordCleanupEvent(ctx, store, state.EventCleanupCandidateFound, decision, map[string]any{"reason": decision.Reason, "category": decision.Category, "delete": decision.Delete})
	if !decision.Delete {
		deps.mirrorState(store, safeRoot, decision, false, cleanuppolicy.DeletionResult(decision, "kept"), true)
		deps.recordCleanupEvent(ctx, store, state.EventCleanupSkipped, decision, map[string]any{"reason": decision.Reason, "category": decision.Category})
		deps.logf("keep %s [%s]: %s", workspace, decision.Category, decision.Reason)
		return true, finish("completed", "cleanup_kept", "")
	}
	deps.recordCleanupEvent(ctx, store, state.EventCleanupDeletionAttempted, decision, map[string]any{"reason": decision.Reason, "category": decision.Category})
	if err := deps.assertSafeDeletePath(safeRoot, workspace); err != nil {
		deps.mirrorState(store, safeRoot, decision, true, "failed", true)
		deps.recordCleanupError(ctx, store, decision, err)
		return true, errors.Join(err, finish("failed", "cleanup_delete_unsafe", err.Error()))
	}
	if err := os.RemoveAll(workspace); err != nil {
		deps.mirrorState(store, safeRoot, decision, true, "failed", true)
		deps.recordCleanupError(ctx, store, decision, err)
		return true, errors.Join(err, finish("failed", "cleanup_delete_failed", err.Error()))
	}
	deps.mirrorState(store, safeRoot, decision, true, "deleted", false)
	deps.recordCleanupEvent(ctx, store, state.EventCleanupDeletionSucceeded, decision, map[string]any{"reason": decision.Reason, "category": decision.Category})
	deps.logf("deleted %s [%s]: %s", workspace, decision.Category, decision.Reason)
	return true, finish("completed", "cleanup_deleted", "")
}

func Complete(ctx context.Context, store *state.Store, task state.WorkerTask, status string, didWork bool, reason, errorText string, startedAt, finishedAt time.Time, deps Dependencies) error {
	if store == nil || task.TaskKey == "" {
		return nil
	}
	if status == "" {
		status = "completed"
	}
	if reason == "" {
		reason = "cleanup_task_completed"
	}
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	if finishedAt.IsZero() {
		finishedAt = time.Now().UTC()
	}
	completeErr := store.CompleteWorkerTask(ctx, task.TaskKey, status, finishedAt)
	payload := map[string]any{
		"lane":      "cleanup",
		"task_key":  task.TaskKey,
		"role":      workertask.RoleCleanup,
		"status":    status,
		"reason":    reason,
		"did_work":  didWork,
		"issue_key": task.IssueKey,
	}
	if errorText != "" {
		payload["error"] = errorText
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode cleanup worker result payload: %w", err)
	}
	resultErr := store.UpsertWorkerResult(ctx, state.WorkerResult{
		TaskKey:    task.TaskKey,
		Role:       workertask.RoleCleanup,
		LaneName:   "cleanup",
		IssueKey:   task.IssueKey,
		IssueID:    task.IssueID,
		Attempt:    task.Attempt,
		Status:     status,
		DidWork:    didWork,
		Reason:     reason,
		Error:      errorText,
		Payload:    payloadJSON,
		StartedAt:  startedAt,
		FinishedAt: finishedAt,
		UpdatedAt:  finishedAt,
	})
	eventType := state.EventWorkerTaskCompleted
	if status == "failed" {
		eventType = state.EventWorkerTaskFailed
	}
	task.Status = status
	task.UpdatedAt = finishedAt
	deps.recordTaskEvent(ctx, store, eventType, task, payload)
	return errors.Join(completeErr, resultErr)
}

func (deps Dependencies) safeWorkspaceRoot(root string) (string, error) {
	if deps.SafeWorkspaceRoot == nil {
		return "", fmt.Errorf("cleanup worker dependency SafeWorkspaceRoot is required")
	}
	return deps.SafeWorkspaceRoot(root)
}

func (deps Dependencies) safeWorkspacePath(root, name string) (string, error) {
	if deps.SafeWorkspacePath == nil {
		return "", fmt.Errorf("cleanup worker dependency SafeWorkspacePath is required")
	}
	return deps.SafeWorkspacePath(root, name)
}

func (deps Dependencies) assertSafeDeletePath(root, workspace string) error {
	if deps.AssertSafeDeletePath == nil {
		return fmt.Errorf("cleanup worker dependency AssertSafeDeletePath is required")
	}
	return deps.AssertSafeDeletePath(root, workspace)
}

func (deps Dependencies) doneIssues(ctx context.Context, projectSlug, stateName string) (map[string]bool, error) {
	if deps.DoneIssues == nil {
		return nil, fmt.Errorf("cleanup worker dependency DoneIssues is required")
	}
	return deps.DoneIssues(ctx, projectSlug, stateName)
}

func (deps Dependencies) decision(ctx context.Context, safeRoot, workspace string, doneIssues map[string]bool, store *state.Store, hasChanges WorkspaceChangeChecker) (cleanuppolicy.Decision, error) {
	if deps.Decision == nil {
		return cleanuppolicy.Decision{}, fmt.Errorf("cleanup worker dependency Decision is required")
	}
	return deps.Decision(ctx, safeRoot, workspace, doneIssues, store, hasChanges)
}

func (deps Dependencies) mirrorState(store *state.Store, safeRoot string, decision cleanuppolicy.Decision, eligible bool, deletionResult string, workspaceExists bool) {
	if deps.MirrorState != nil {
		deps.MirrorState(store, safeRoot, decision, eligible, deletionResult, workspaceExists)
	}
}

func (deps Dependencies) recordCleanupEvent(ctx context.Context, store *state.Store, eventType string, decision cleanuppolicy.Decision, payload map[string]any) {
	if deps.RecordCleanupEvent != nil {
		deps.RecordCleanupEvent(ctx, store, eventType, decision, payload)
	}
}

func (deps Dependencies) recordCleanupError(ctx context.Context, store *state.Store, decision cleanuppolicy.Decision, err error) {
	if deps.RecordCleanupError != nil {
		deps.RecordCleanupError(ctx, store, decision, err)
	}
}

func (deps Dependencies) recordTaskEvent(ctx context.Context, store *state.Store, eventType string, task state.WorkerTask, payload map[string]any) {
	if deps.RecordTaskEvent != nil {
		deps.RecordTaskEvent(ctx, store, eventType, task, payload)
	}
}

func (deps Dependencies) logf(format string, args ...any) {
	if deps.Logf != nil {
		deps.Logf(format, args...)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
