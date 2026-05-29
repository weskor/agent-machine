package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	artifactio "github.com/weskor/agent-machine/internal/artifacts"
	cfg "github.com/weskor/agent-machine/internal/config"
	sh "github.com/weskor/agent-machine/internal/shell"
	"github.com/weskor/agent-machine/internal/state"
	"github.com/weskor/agent-machine/internal/stateprojection"
	ws "github.com/weskor/agent-machine/internal/workspace"
)

const (
	runLockFile       = ws.RunLockFile
	runLockDir        = ws.RunLockDir
	runLockStaleAfter = ws.RunLockStaleAfter
)

var errRunLocked = ws.ErrRunLocked

func runLockManager() ws.LockManager { return ws.LockManager{Logf: log} }

func runLockManagerWithState(store *state.Store) ws.LockManager {
	return ws.LockManager{Logf: log, StateStore: store}
}

func acquireRunLock(workspace string, candidate *issue, branch string, now time.Time) (*runLock, func(), error) {
	return runLockManager().Acquire(workspace, candidate, branch, now)
}

func acquireRunLockWithState(store *state.Store, workspace string, candidate *issue, branch string, now time.Time) (*runLock, func(), error) {
	return runLockManagerWithState(store).Acquire(workspace, candidate, branch, now)
}

func acquireRunLockWithStateContext(ctx context.Context, store *state.Store, workspace string, candidate *issue, branch string, now time.Time) (*runLock, func(), error) {
	return runLockManagerWithState(store).AcquireContext(ctx, workspace, candidate, branch, now)
}

func heartbeatRunLock(workspace string, at time.Time) { runLockManager().Heartbeat(workspace, at) }

func heartbeatRunLockWithState(store *state.Store, workspace string, at time.Time) {
	runLockManagerWithState(store).Heartbeat(workspace, at)
}

func heartbeatRunLockWithStateContext(ctx context.Context, store *state.Store, workspace string, at time.Time) {
	runLockManagerWithState(store).HeartbeatContext(ctx, workspace, at)
}

func cleanupStaleRunLocks(workspaceRoot string, now time.Time) (int, error) {
	return runLockManager().CleanupStale(workspaceRoot, now)
}

func cleanupStaleRunLocksWithStateContext(ctx context.Context, store *state.Store, workspaceRoot string, now time.Time) (int, error) {
	return runLockManagerWithState(store).CleanupStaleContext(ctx, workspaceRoot, now)
}

func hasRunLock(workspace string) bool { return ws.HasLock(workspace) }

func isEmptyIgnoringRunLock(dir string) bool { return ws.IsEmptyIgnoringLock(dir) }

func readRunLock(path string) (runLock, error) { return ws.Read(path) }

func runLockPath(workspace string) string { return ws.Path(workspace) }

func hostname() string { return ws.Hostname() }

func ensureIsolatedWorkspace(workspaceRoot, workspace, identifier string) error {
	return ensureIsolatedWorkspaceContext(context.Background(), workspaceRoot, workspace, identifier)
}

func ensureIsolatedWorkspaceContext(ctx context.Context, workspaceRoot, workspace, identifier string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := assertSafeDeletePath(workspaceRoot, workspace); err != nil {
		return err
	}
	topLevel, err := sh.CaptureQuietContext(ctx, "git rev-parse --show-toplevel", workspace)
	if err != nil {
		return fmt.Errorf("workspace %s is not a git checkout: %w", workspace, err)
	}
	topAbs, err := filepath.Abs(strings.TrimSpace(topLevel))
	if err != nil {
		return err
	}
	workspaceAbs, err := filepath.Abs(workspace)
	if err != nil {
		return err
	}
	if filepath.Clean(topAbs) != filepath.Clean(workspaceAbs) {
		return fmt.Errorf("refusing shared git checkout: top-level %s does not match workspace %s", strings.TrimSpace(topLevel), workspace)
	}
	branch := expectedWorkspaceBranch(identifier)
	current, err := currentGitBranchContext(ctx, workspace)
	if err != nil {
		return err
	}
	if current == branch {
		return nil
	}
	if current != "" && strings.HasPrefix(current, "am/") {
		return fmt.Errorf("workspace %s is on unexpected Agent Machine branch %q; expected %q", workspace, current, branch)
	}
	if err := sh.RunWithContext(ctx, "git switch -C "+sh.Quote(branch), workspace); err != nil {
		return err
	}
	return nil
}

func writeRunRecord(workspace string, record runRecord) {
	_ = writeRunRecordWithState(nil, workspace, record)
}

func writeRunRecordWithState(store *state.Store, workspace string, record runRecord) error {
	return writeRunRecordWithStateContext(context.Background(), store, workspace, record)
}

func writeRunRecordWithCommandState(store *state.Store, workspace string, record runRecord) error {
	return writeRunRecordWithCommandStateContext(context.Background(), store, workspace, record)
}

func writeRunRecordWithStateContext(ctx context.Context, store *state.Store, workspace string, record runRecord) error {
	return writeRunRecordWithStateFallbackContext(ctx, store, true, workspace, record)
}

func writeRunRecordWithCommandStateContext(ctx context.Context, store *state.Store, workspace string, record runRecord) error {
	return writeRunRecordWithStateFallbackContext(ctx, store, false, workspace, record)
}

func writeRunRecordWithStateFallbackContext(ctx context.Context, store *state.Store, fallbackOpen bool, workspace string, record runRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	evaluation := evaluationForRun(workspace, record)
	stateStore, dbPath, closeStore, err := stateStoreForRunRecordExportContext(ctx, store, fallbackOpen, record.WorkspaceRoot)
	if err != nil {
		if dbPath != "" {
			log("failed to persist run record into SQLite state at %s before artifact export: %v", dbPath, err)
		} else {
			log("failed to persist run record into SQLite state before artifact export: %v", err)
		}
		return err
	}
	if closeStore != nil {
		defer closeStore()
	}
	if stateStore != nil {
		if err := stateStore.UpsertAttemptResult(ctx, stateProjection{}.AttemptResult(workspace, record, evaluation)); err != nil {
			log("failed to persist run record into SQLite state before artifact export: %v", err)
			return err
		}
	}
	path, err := artifactManager().WriteRunRecord(workspace, record)
	if err != nil {
		log("failed to write run record: %v", err)
		recordArtifactExportFailureContext(ctx, stateStore, record, "run_record", err)
		return err
	}
	log("wrote run record: %s", path)
	evaluationPath, evaluation, err := writeEvaluationArtifactResult(workspace, record)
	if err != nil {
		recordArtifactExportFailureContext(ctx, stateStore, record, "evaluation", err)
		return err
	}
	logRunArtifactSummary(path, evaluationPath, record, evaluation)
	writeRunProgress(record.WorkspaceRoot, runProgressForRecord(workspace, record, evaluation))
	return nil
}
func stateStoreForRunRecordExportContext(ctx context.Context, store *state.Store, fallbackOpen bool, workspaceRoot string) (*state.Store, string, func(), error) {
	if store != nil {
		return store, "", nil, nil
	}
	if !fallbackOpen {
		return nil, "", nil, nil
	}
	if strings.TrimSpace(workspaceRoot) == "" {
		return nil, "", nil, nil
	}
	opened, dbPath, err := openStateProjectionStore(ctx, workspaceRoot)
	if err != nil {
		return nil, dbPath, nil, err
	}
	return opened, dbPath, func() { _ = opened.Close() }, nil
}

func recordArtifactExportFailureContext(ctx context.Context, store *state.Store, record runRecord, artifact string, exportErr error) {
	if store == nil || exportErr == nil {
		return
	}
	if err := store.RecordArtifactExportFailure(ctx, record.IssueIdentifier, 1, artifact, exportErr.Error(), time.Now().UTC()); err != nil {
		log("failed to record artifact export failure in SQLite state: %v", err)
	}
}

func artifactManager() artifactio.Manager {
	return artifactio.Manager{
		Evaluate:       evaluationForRun,
		PRStateForURL:  prStateForURL,
		TerminalStatus: terminalRunStatus,
	}
}

func logRunArtifactSummary(runRecordPath, evaluationPath string, record runRecord, evaluation evaluationArtifact) {
	log("run summary: issue=%s status=%s outcome=%s pr=%s review=%s checks=%s next_action=%s duration_ms=%d run_record=%s evaluation=%s", emptyAsUnknown(record.IssueIdentifier), emptyAsUnknown(record.Status), emptyAsUnknown(evaluation.Outcome), emptyAsUnknown(record.PRURL), emptyAsUnknown(record.ReviewStatus), emptyAsUnknown(evaluation.ChecksStatus), emptyAsUnknown(evaluation.NextAction), record.DurationMS, runRecordPath, evaluationPath)
}

func baseBranchForWorkspace(workspace string) string {
	proj, err := cfg.ReadProject(filepath.Join(workspace, cfg.DefaultConfigPath))
	if err != nil {
		return "main"
	}
	base := cfg.BaseBranchFromConfig(proj.YAML)
	if strings.TrimSpace(base) == "" {
		return "main"
	}
	return base
}

type stateProjection struct{}

func parseGitHubPR(prURL string) (string, int) {
	return stateprojection.ParseGitHubPR(prURL)
}

func retryableRunStatus(status string) bool {
	return stateprojection.RetryableRunStatus(status)
}

func (stateProjection) RunArtifact(workspace string, record runRecord, evaluation evaluationArtifact) state.RunArtifactSnapshot {
	return stateProjectionCore().RunArtifact(workspace, record, evaluation)
}

func (stateProjection) AttemptResult(workspace string, record runRecord, evaluation evaluationArtifact) state.AttemptResult {
	return stateProjectionCore().AttemptResult(workspace, record, evaluation)
}

func (stateProjection) Cleanup(decision cleanupResult, eligible bool, deletionResult string, workspaceExists bool, updatedAt time.Time) state.CleanupState {
	return stateProjectionCore().Cleanup(stateprojection.CleanupDecision{
		Reason:          decision.Reason,
		Category:        decision.Category,
		IssueIdentifier: decision.IssueIdentifier,
		ArtifactRef:     decision.ArtifactRef,
	}, eligible, deletionResult, workspaceExists, updatedAt)
}

func (stateProjection) RunLockLease(lock runLock, observedAt time.Time) state.Lease {
	return stateProjectionCore().RunLockLease(lock, observedAt)
}

func (stateProjection) RunLockLeaseName(lock runLock) string {
	return stateProjectionCore().RunLockLeaseName(lock)
}

func (stateProjection) RunLockLeaseScope(lock runLock) string {
	return stateProjectionCore().RunLockLeaseScope(lock)
}

func (stateProjection) DaemonHeartbeat(processID string, config runnerConfig, heartbeat continuousHeartbeat) state.DaemonHeartbeat {
	return stateProjectionCore().DaemonHeartbeat(processID, config, stateprojection.DaemonHeartbeatInput{
		LaneName:            heartbeat.LaneName,
		CycleNumber:         heartbeat.CycleNumber,
		Success:             heartbeat.Success,
		Err:                 heartbeat.Err,
		ActiveTaskKey:       heartbeat.ActiveTaskKey,
		ActiveTaskRole:      heartbeat.ActiveTaskRole,
		ActiveLeaseName:     heartbeat.ActiveLeaseName,
		ActiveTaskStartedAt: heartbeat.ActiveTaskStartedAt,
		At:                  heartbeat.At,
	})
}

func stateProjectionCore() stateprojection.Projection {
	return stateprojection.Projection{BaseBranch: baseBranchForWorkspace, TerminalStatus: terminalRunStatus, RunLockStaleAfter: runLockStaleAfter}
}

func openStateProjectionStore(ctx context.Context, workspaceRoot string) (*state.Store, string, error) {
	return stateprojection.OpenStore(ctx, workspaceRoot)
}

func commandScopedStateStore(ctx context.Context, workspaceRoot, commandName string) (*state.Store, string) {
	store, dbPath, err := openStateProjectionStore(ctx, workspaceRoot)
	if err != nil {
		if dbPath != "" {
			log("SQLite %s mirror degraded: open path=%s error=%q", commandName, dbPath, err.Error())
		} else {
			log("SQLite %s mirror degraded: %v", commandName, err)
		}
		return nil, dbPath
	}
	return store, dbPath
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func stateID(states []workflowState, name string) string {
	for _, state := range states {
		if state.Name == name {
			return state.ID
		}
	}
	return ""
}

func renderPrompt(template string, issue issue, attempt int) string {
	replacer := strings.NewReplacer("{{issue.identifier}}", issue.Identifier, "{{issue.title}}", issue.Title, "{{issue.description}}", issue.Description, "{{issue.url}}", issue.URL, "{{issue.state}}", issue.State.Name, "{{attempt}}", fmt.Sprint(attempt))
	return replacer.Replace(template)
}
