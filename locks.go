package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/weskor/pi-symphony/internal/state"
)

const (
	runLockFile       = ".pi-symphony-lock.json"
	runLockDir        = ".pi-symphony-locks"
	runLockStaleAfter = 4 * time.Hour
)

var errRunLocked = errors.New("pi symphony run is locked")

func acquireRunLock(workspace string, candidate *issue, branch string, now time.Time) (*runLock, func(), error) {
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return nil, nil, err
	}
	lock := runLock{
		Owner:           runLockOwner(),
		PID:             os.Getpid(),
		Host:            hostname(),
		IssueIdentifier: candidate.Identifier,
		IssueID:         candidate.ID,
		Branch:          branch,
		Workspace:       workspace,
		StartedAt:       now,
		HeartbeatAt:     now,
	}
	data, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	path := runLockPath(workspace)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, nil, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, nil, describeExistingRunLock(path, now)
		}
		return nil, nil, err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, nil, err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return nil, nil, err
	}
	log("acquired run lock: %s", path)
	mirrorRunLockAcquire(lock)
	release := func() {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			log("failed to release run lock %s: %v", path, err)
		}
		mirrorRunLockRelease(lock, time.Now(), "released")
	}
	return &lock, release, nil
}

func heartbeatRunLock(workspace string, at time.Time) {
	path := runLockPath(workspace)
	data, err := os.ReadFile(path)
	if err != nil {
		log("failed to read run lock for heartbeat: %v", err)
		return
	}
	var lock runLock
	if err := json.Unmarshal(data, &lock); err != nil {
		log("failed to decode run lock for heartbeat: %v", err)
		return
	}
	lock.HeartbeatAt = at
	updated, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		log("failed to encode run lock heartbeat: %v", err)
		return
	}
	if err := os.WriteFile(path, append(updated, '\n'), 0o600); err != nil {
		log("failed to write run lock heartbeat: %v", err)
		return
	}
	mirrorRunLockRenew(lock)
}

func describeExistingRunLock(path string, now time.Time) error {
	lock, err := readRunLock(path)
	if err != nil {
		return fmt.Errorf("%w: unreadable lock at %s; inspect and remove only if no run is active", errRunLocked, path)
	}
	age := now.Sub(lock.HeartbeatAt)
	if age < 0 {
		age = 0
	}
	staleNote := ""
	if age > runLockStaleAfter {
		staleNote = fmt.Sprintf("; heartbeat is stale (%s old). Run `bun run symphony:pi:repair-artifacts` after confirming no owner process is active", age.Round(time.Second))
	}
	return fmt.Errorf("%w: %s is owned by %s pid=%d host=%s issue=%s branch=%s heartbeat=%s%s", errRunLocked, path, lock.Owner, lock.PID, lock.Host, lock.IssueIdentifier, lock.Branch, lock.HeartbeatAt.Format(time.RFC3339), staleNote)
}

func cleanupStaleRunLocks(workspaceRoot string, now time.Time) (int, error) {
	paths, err := filepath.Glob(filepath.Join(workspaceRoot, runLockDir, "*.json"))
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, path := range paths {
		lock, err := readRunLock(path)
		if err != nil {
			log("leaving unreadable lock for manual inspection: %s", path)
			continue
		}
		deadOwner := sameHost(lock.Host) && lock.PID > 0 && !processAlive(lock.PID)
		if now.Sub(lock.HeartbeatAt) <= runLockStaleAfter && !deadOwner {
			continue
		}
		if err := os.Remove(path); err != nil {
			return removed, err
		}
		removed++
		if deadOwner {
			log("removed dead-owner run lock: %s", path)
			mirrorRunLockRelease(lock, now, "dead_owner")
		} else {
			log("removed stale run lock: %s", path)
			mirrorRunLockRelease(lock, now, "stale")
		}
	}
	return removed, nil
}

func mirrorRunLockAcquire(lock runLock) {
	withRunLockStateStore(lock.Workspace, func(store *state.Store) error {
		return store.UpsertLease(context.Background(), runLockLeaseSnapshot(lock, lock.StartedAt))
	})
}

func mirrorRunLockRenew(lock runLock) {
	withRunLockStateStore(lock.Workspace, func(store *state.Store) error {
		if err := store.UpsertLease(context.Background(), runLockLeaseSnapshot(lock, lock.HeartbeatAt)); err != nil {
			return err
		}
		return store.RenewLease(context.Background(), runLockLeaseName(lock), lock.HeartbeatAt, lock.HeartbeatAt.Add(runLockStaleAfter))
	})
}

func mirrorRunLockRelease(lock runLock, at time.Time, reason string) {
	withRunLockStateStore(lock.Workspace, func(store *state.Store) error {
		if err := store.UpsertLease(context.Background(), runLockLeaseSnapshot(lock, at)); err != nil {
			return err
		}
		return store.ReleaseLease(context.Background(), runLockLeaseName(lock), at, reason)
	})
}

func runLockLeaseSnapshot(lock runLock, observedAt time.Time) state.Lease {
	owner := strings.TrimSpace(lock.Owner)
	if owner == "" {
		owner = "unknown"
	}
	acquiredAt := lock.StartedAt
	if acquiredAt.IsZero() {
		acquiredAt = lock.HeartbeatAt
	}
	if acquiredAt.IsZero() {
		acquiredAt = observedAt
	}
	renewedAt := lock.HeartbeatAt
	if renewedAt.IsZero() {
		renewedAt = acquiredAt
	}
	lease := state.Lease{
		Name:       runLockLeaseName(lock),
		Scope:      runLockLeaseScope(lock),
		Owner:      owner,
		AcquiredAt: acquiredAt,
		RenewedAt:  renewedAt,
	}
	if !renewedAt.IsZero() {
		lease.ExpiresAt = renewedAt.Add(runLockStaleAfter)
	}
	return lease
}

func withRunLockStateStore(workspace string, fn func(*state.Store) error) {
	if strings.TrimSpace(workspace) == "" {
		log("skipping sqlite lease mirror: workspace path is empty")
		return
	}
	dbPath := state.DefaultDBPath(filepath.Dir(workspace))
	if dbPath == "" {
		log("skipping sqlite lease mirror: state db path is empty")
		return
	}
	store, err := state.Open(context.Background(), dbPath)
	if err != nil {
		log("skipping sqlite lease mirror: %v", err)
		return
	}
	defer store.Close()
	if err := fn(store); err != nil {
		log("skipping sqlite lease mirror: %v", err)
	}
}

func runLockLeaseName(lock runLock) string {
	name := strings.TrimSpace(lock.IssueIdentifier)
	if name == "" {
		name = filepath.Base(lock.Workspace)
	}
	return "run:" + name
}

func runLockLeaseScope(lock runLock) string {
	return filepath.Dir(lock.Workspace)
}

func sameHost(host string) bool {
	return strings.TrimSpace(host) != "" && strings.EqualFold(strings.TrimSpace(host), hostname())
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func hasRunLock(workspace string) bool {
	_, err := os.Stat(runLockPath(workspace))
	return err == nil
}

func isEmptyIgnoringRunLock(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.Name() != runLockFile {
			return false
		}
	}
	return true
}

func readRunLock(path string) (runLock, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return runLock{}, err
	}
	var lock runLock
	if err := json.Unmarshal(data, &lock); err != nil {
		return runLock{}, err
	}
	return lock, nil
}

func runLockPath(workspace string) string {
	return filepath.Join(filepath.Dir(workspace), runLockDir, filepath.Base(workspace)+".json")
}

func runLockOwner() string {
	if value := strings.TrimSpace(os.Getenv("USER")); value != "" {
		return value
	}
	return "unknown"
}

func hostname() string {
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		return "unknown"
	}
	return host
}
