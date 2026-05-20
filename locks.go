package main

import (
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

func heartbeatRunLock(workspace string, at time.Time) { runLockManager().Heartbeat(workspace, at) }

func describeExistingRunLock(path string, now time.Time) error { return ws.DescribeExisting(path, now) }

func cleanupStaleRunLocks(workspaceRoot string, now time.Time) (int, error) {
	return runLockManager().CleanupStale(workspaceRoot, now)
}

func cleanupStaleRunLocksWithState(store *state.Store, workspaceRoot string, now time.Time) (int, error) {
	return runLockManagerWithState(store).CleanupStale(workspaceRoot, now)
}

func mirrorRunLockAcquire(lock runLock) { runLockManager().MirrorAcquire(lock) }

func mirrorRunLockRenew(lock runLock) { runLockManager().MirrorRenew(lock) }

func mirrorRunLockRelease(lock runLock, at time.Time, reason string) {
	runLockManager().MirrorRelease(lock, at, reason)
}

func sameHost(host string) bool { return ws.SameHost(host) }

func processAlive(pid int) bool { return ws.ProcessAlive(pid) }

func hasRunLock(workspace string) bool { return ws.HasLock(workspace) }

func isEmptyIgnoringRunLock(dir string) bool { return ws.IsEmptyIgnoringLock(dir) }

func readRunLock(path string) (runLock, error) { return ws.Read(path) }

func runLockPath(workspace string) string { return ws.Path(workspace) }

func runLockOwner() string { return ws.Owner() }

func hostname() string { return ws.Hostname() }
