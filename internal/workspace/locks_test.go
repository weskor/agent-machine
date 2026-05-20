package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/weskor/pi-symphony/internal/domain"
	"github.com/weskor/pi-symphony/internal/state"
)

func TestLockManagerLifecycleTable(t *testing.T) {
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		run  func(*testing.T, string, time.Time)
	}{
		{name: "release removes active lock", run: func(t *testing.T, workspace string, now time.Time) {
			_, release, err := (LockManager{}).Acquire(workspace, &domain.Issue{Identifier: "CAG-1", ID: "issue-id"}, "symphony/CAG-1-workspace", now)
			if err != nil {
				t.Fatalf("Acquire error = %v", err)
			}
			release()
			if _, err := os.Stat(Path(workspace)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("lock exists after release: %v", err)
			}
		}},
		{name: "active lock rejection", run: func(t *testing.T, workspace string, now time.Time) {
			_, _, err := (LockManager{}).Acquire(workspace, &domain.Issue{Identifier: "CAG-1", ID: "issue-id"}, "symphony/CAG-1-workspace", now)
			if err != nil {
				t.Fatalf("first Acquire error = %v", err)
			}
			_, _, err = (LockManager{}).Acquire(workspace, &domain.Issue{Identifier: "CAG-1", ID: "issue-id"}, "symphony/CAG-1-workspace", now)
			if !errors.Is(err, ErrRunLocked) {
				t.Fatalf("second Acquire error = %v, want ErrRunLocked", err)
			}
		}},
		{name: "stale lock cleanup", run: func(t *testing.T, workspace string, now time.Time) {
			writeLockFixture(t, workspace, domain.RunLock{Workspace: workspace, IssueIdentifier: "CAG-1", Host: "other", PID: os.Getpid(), HeartbeatAt: now.Add(-RunLockStaleAfter - time.Second)})
			removed, err := (LockManager{}).CleanupStale(filepath.Dir(workspace), now)
			if err != nil || removed != 1 {
				t.Fatalf("CleanupStale = %d, %v; want 1, nil", removed, err)
			}
		}},
		{name: "dead owner cleanup", run: func(t *testing.T, workspace string, now time.Time) {
			writeLockFixture(t, workspace, domain.RunLock{Workspace: workspace, IssueIdentifier: "CAG-1", Host: Hostname(), PID: 999999, HeartbeatAt: now})
			removed, err := (LockManager{}).CleanupStale(filepath.Dir(workspace), now)
			if err != nil || removed != 1 {
				t.Fatalf("CleanupStale = %d, %v; want 1, nil", removed, err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) { tc.run(t, filepath.Join(t.TempDir(), "CAG-1"), now) })
	}
}

func TestLockManagerReclaimsDeadOwnerSQLiteLease(t *testing.T) {
	ctx := context.Background()
	store, err := state.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	workspace := filepath.Join(t.TempDir(), "CAG-113")
	oldLock := domain.RunLock{Owner: Owner(), PID: 999999, Host: Hostname(), IssueIdentifier: "CAG-113", IssueID: "issue-id", Branch: "symphony/CAG-113-workspace", Workspace: workspace, StartedAt: now.Add(-time.Hour), HeartbeatAt: now.Add(-time.Hour)}
	if acquired, err := store.AcquireLease(ctx, RunLockLease(oldLock, oldLock.HeartbeatAt), oldLock.HeartbeatAt); err != nil || !acquired {
		t.Fatalf("seed AcquireLease acquired=%v err=%v", acquired, err)
	}
	lock, release, err := (LockManager{StateStore: store}).Acquire(workspace, &domain.Issue{Identifier: "CAG-113", ID: "issue-id"}, "symphony/CAG-113-workspace", now)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	defer release()
	if lock == nil || lock.PID == 999999 {
		t.Fatalf("Acquire() lock = %+v", lock)
	}
	lease, ok, err := store.Lease(ctx, "run:CAG-113")
	if err != nil || !ok {
		t.Fatalf("Lease ok=%v err=%v", ok, err)
	}
	if lease.Owner == RunLockLease(oldLock, oldLock.HeartbeatAt).Owner {
		t.Fatalf("lease owner was not reclaimed: %+v", lease)
	}
}

func TestLockManagerDoesNotReclaimLiveOwnerSQLiteLease(t *testing.T) {
	ctx := context.Background()
	store, err := state.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	workspace := filepath.Join(t.TempDir(), "CAG-113")
	oldLock := domain.RunLock{Owner: Owner(), PID: os.Getpid(), Host: Hostname(), IssueIdentifier: "CAG-113", IssueID: "issue-id", Branch: "symphony/CAG-113-workspace", Workspace: workspace, StartedAt: now.Add(-time.Hour), HeartbeatAt: now.Add(-time.Hour)}
	if acquired, err := store.AcquireLease(ctx, RunLockLease(oldLock, oldLock.HeartbeatAt), oldLock.HeartbeatAt); err != nil || !acquired {
		t.Fatalf("seed AcquireLease acquired=%v err=%v", acquired, err)
	}
	_, _, err = (LockManager{StateStore: store}).Acquire(workspace, &domain.Issue{Identifier: "CAG-113", ID: "issue-id"}, "symphony/CAG-113-workspace", now)
	if !errors.Is(err, ErrRunLocked) {
		t.Fatalf("Acquire() error = %v, want ErrRunLocked", err)
	}
}

func writeLockFixture(t *testing.T, workspace string, lock domain.RunLock) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(Path(workspace)), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(lock)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(workspace), data, 0o600); err != nil {
		t.Fatal(err)
	}
}
