package workspace

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

	"github.com/weskor/pi-symphony/internal/domain"
	"github.com/weskor/pi-symphony/internal/state"
)

const (
	RunLockFile       = ".pi-symphony-lock.json"
	RunLockDir        = ".pi-symphony-locks"
	RunLockStaleAfter = 4 * time.Hour
)

var ErrRunLocked = errors.New("pi symphony run is locked")

type Logger func(string, ...any)

type LockManager struct {
	Logf       Logger
	StateStore *state.Store
}

func (m LockManager) logf(format string, args ...any) {
	if m.Logf != nil {
		m.Logf(format, args...)
	}
}

func (m LockManager) Acquire(workspace string, candidate *domain.Issue, branch string, now time.Time) (*domain.RunLock, func(), error) {
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return nil, nil, err
	}
	lock := domain.RunLock{Owner: Owner(), PID: os.Getpid(), Host: Hostname(), IssueIdentifier: candidate.Identifier, IssueID: candidate.ID, Branch: branch, Workspace: workspace, StartedAt: now, HeartbeatAt: now}
	if m.StateStore != nil {
		return m.acquireSQLiteLease(workspace, lock, now)
	}
	data, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	path := Path(workspace)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, nil, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, nil, DescribeExisting(path, now)
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
	m.logf("acquired run lock: %s", path)
	m.MirrorAcquire(lock)
	release := func() {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			m.logf("failed to release run lock %s: %v", path, err)
		}
		m.MirrorRelease(lock, time.Now(), "released")
	}
	return &lock, release, nil
}

func (m LockManager) acquireSQLiteLease(workspace string, lock domain.RunLock, now time.Time) (*domain.RunLock, func(), error) {
	acquired, err := m.StateStore.AcquireLease(context.Background(), RunLockLease(lock, now), now)
	if err != nil {
		return nil, nil, err
	}
	if !acquired {
		return nil, nil, fmt.Errorf("%w: active SQLite lease %s", ErrRunLocked, RunLockLeaseName(lock))
	}
	path := Path(workspace)
	if err := writeExportedLock(path, lock); err != nil {
		m.logf("failed to write run lock export %s: %v", path, err)
	}
	m.logf("acquired SQLite run lease: %s", RunLockLeaseName(lock))
	release := func() {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			m.logf("failed to release run lock export %s: %v", path, err)
		}
		if err := m.StateStore.ReleaseLease(context.Background(), RunLockLeaseName(lock), time.Now(), "released"); err != nil {
			m.logf("failed to release SQLite run lease %s: %v", RunLockLeaseName(lock), err)
		}
	}
	return &lock, release, nil
}

func writeExportedLock(path string, lock domain.RunLock) error {
	data, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func (m LockManager) Heartbeat(workspace string, at time.Time) {
	path := Path(workspace)
	data, err := os.ReadFile(path)
	if err != nil && m.StateStore == nil {
		m.logf("failed to read run lock for heartbeat: %v", err)
		return
	}
	var lock domain.RunLock
	if err == nil {
		if err := json.Unmarshal(data, &lock); err != nil && m.StateStore == nil {
			m.logf("failed to decode run lock for heartbeat: %v", err)
			return
		}
	}
	if lock.Workspace == "" {
		lock = domain.RunLock{Owner: Owner(), PID: os.Getpid(), Host: Hostname(), IssueIdentifier: filepath.Base(workspace), Workspace: workspace, StartedAt: at}
	}
	if m.StateStore != nil {
		lock.HeartbeatAt = at
		if err := m.StateStore.RenewLease(context.Background(), RunLockLeaseName(lock), at, at.Add(RunLockStaleAfter)); err != nil {
			m.logf("failed to renew SQLite run lease %s: %v", RunLockLeaseName(lock), err)
			return
		}
		if err := writeExportedLock(path, lock); err != nil {
			m.logf("failed to write run lock heartbeat export: %v", err)
		}
		return
	}
	if err := json.Unmarshal(data, &lock); err != nil {
		m.logf("failed to decode run lock for heartbeat: %v", err)
		return
	}
	lock.HeartbeatAt = at
	updated, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		m.logf("failed to encode run lock heartbeat: %v", err)
		return
	}
	if err := os.WriteFile(path, append(updated, '\n'), 0o600); err != nil {
		m.logf("failed to write run lock heartbeat: %v", err)
		return
	}
	m.MirrorRenew(lock)
}

func DescribeExisting(path string, now time.Time) error {
	lock, err := Read(path)
	if err != nil {
		return fmt.Errorf("%w: unreadable lock at %s; inspect and remove only if no run is active", ErrRunLocked, path)
	}
	age := now.Sub(lock.HeartbeatAt)
	if age < 0 {
		age = 0
	}
	staleNote := ""
	if age > RunLockStaleAfter {
		staleNote = fmt.Sprintf("; heartbeat is stale (%s old). Run `bun run symphony:pi:repair-artifacts` after confirming no owner process is active", age.Round(time.Second))
	}
	return fmt.Errorf("%w: %s is owned by %s pid=%d host=%s issue=%s branch=%s heartbeat=%s%s", ErrRunLocked, path, lock.Owner, lock.PID, lock.Host, lock.IssueIdentifier, lock.Branch, lock.HeartbeatAt.Format(time.RFC3339), staleNote)
}

func (m LockManager) CleanupStale(root string, now time.Time) (int, error) {
	paths, err := filepath.Glob(filepath.Join(root, RunLockDir, "*.json"))
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, path := range paths {
		lock, err := Read(path)
		if err != nil {
			m.logf("leaving unreadable lock for manual inspection: %s", path)
			continue
		}
		deadOwner := SameHost(lock.Host) && lock.PID > 0 && !ProcessAlive(lock.PID)
		if now.Sub(lock.HeartbeatAt) <= RunLockStaleAfter && !deadOwner {
			continue
		}
		if err := os.Remove(path); err != nil {
			return removed, err
		}
		removed++
		if m.StateStore != nil {
			if deadOwner {
				m.logf("removed dead-owner run lock export: %s", path)
			} else {
				m.logf("removed stale run lock export: %s", path)
			}
			continue
		}
		if deadOwner {
			m.logf("removed dead-owner run lock: %s", path)
			m.MirrorRelease(lock, now, "dead_owner")
		} else {
			m.logf("removed stale run lock: %s", path)
			m.MirrorRelease(lock, now, "stale")
		}
	}
	return removed, nil
}

func (m LockManager) MirrorAcquire(lock domain.RunLock) {
	m.withStateStore(lock.Workspace, func(store *state.Store) error {
		return store.UpsertLease(context.Background(), RunLockLease(lock, lock.StartedAt))
	})
}
func (m LockManager) MirrorRenew(lock domain.RunLock) {
	m.withStateStore(lock.Workspace, func(store *state.Store) error {
		if err := store.UpsertLease(context.Background(), RunLockLease(lock, lock.HeartbeatAt)); err != nil {
			return err
		}
		return store.RenewLease(context.Background(), RunLockLeaseName(lock), lock.HeartbeatAt, lock.HeartbeatAt.Add(RunLockStaleAfter))
	})
}
func (m LockManager) MirrorRelease(lock domain.RunLock, at time.Time, reason string) {
	m.withStateStore(lock.Workspace, func(store *state.Store) error {
		if err := store.UpsertLease(context.Background(), RunLockLease(lock, at)); err != nil {
			return err
		}
		return store.ReleaseLease(context.Background(), RunLockLeaseName(lock), at, reason)
	})
}

func (m LockManager) withStateStore(workspace string, fn func(*state.Store) error) {
	if m.StateStore != nil {
		if err := fn(m.StateStore); err != nil {
			m.logf("skipping sqlite lease mirror: %v", err)
		}
		return
	}
	if strings.TrimSpace(workspace) == "" {
		m.logf("skipping sqlite lease mirror: workspace path is empty")
		return
	}
	dbPath := state.DefaultDBPath(filepath.Dir(workspace))
	if dbPath == "" {
		m.logf("skipping sqlite lease mirror: state db path is empty")
		return
	}
	store, err := state.Open(context.Background(), dbPath)
	if err != nil {
		m.logf("skipping sqlite lease mirror: %v", err)
		return
	}
	defer store.Close()
	if err := fn(store); err != nil {
		m.logf("skipping sqlite lease mirror: %v", err)
	}
}

func RunLockLease(lock domain.RunLock, observedAt time.Time) state.Lease {
	owner := strings.TrimSpace(lock.Owner)
	if owner == "" {
		owner = "unknown"
	}
	if host := strings.TrimSpace(lock.Host); host != "" && host != "unknown" {
		owner = owner + "@" + host
	}
	if lock.PID > 0 {
		owner = fmt.Sprintf("%s#%d", owner, lock.PID)
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
	lease := state.Lease{Name: RunLockLeaseName(lock), Scope: RunLockLeaseScope(lock), Owner: owner, AcquiredAt: acquiredAt, RenewedAt: renewedAt}
	if !renewedAt.IsZero() {
		lease.ExpiresAt = renewedAt.Add(RunLockStaleAfter)
	}
	return lease
}

func RunLockLeaseName(lock domain.RunLock) string {
	name := strings.TrimSpace(lock.IssueIdentifier)
	if name == "" {
		name = filepath.Base(lock.Workspace)
	}
	return "run:" + name
}
func RunLockLeaseScope(lock domain.RunLock) string { return filepath.Dir(lock.Workspace) }
func SameHost(host string) bool {
	return strings.TrimSpace(host) != "" && strings.EqualFold(strings.TrimSpace(host), Hostname())
}
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
func HasLock(workspace string) bool { _, err := os.Stat(Path(workspace)); return err == nil }
func IsEmptyIgnoringLock(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.Name() != RunLockFile {
			return false
		}
	}
	return true
}
func Read(path string) (domain.RunLock, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return domain.RunLock{}, err
	}
	var lock domain.RunLock
	if err := json.Unmarshal(data, &lock); err != nil {
		return domain.RunLock{}, err
	}
	return lock, nil
}
func Path(workspace string) string {
	return filepath.Join(filepath.Dir(workspace), RunLockDir, filepath.Base(workspace)+".json")
}
func Owner() string {
	if value := strings.TrimSpace(os.Getenv("USER")); value != "" {
		return value
	}
	return "unknown"
}
func Hostname() string {
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		return "unknown"
	}
	return host
}
