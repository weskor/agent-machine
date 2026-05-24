package main

import (
	"context"
	"time"

	"github.com/weskor/pi-symphony/internal/state"
	ws "github.com/weskor/pi-symphony/internal/workspace"
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
