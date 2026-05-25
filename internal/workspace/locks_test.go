package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weskor/agent-machine/internal/domain"
	"github.com/weskor/agent-machine/internal/state"
)

func TestLockManagerLifecycleTable(t *testing.T) {
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		run  func(*testing.T, string, time.Time)
	}{
		{name: "release removes active lock", run: func(t *testing.T, workspace string, now time.Time) {
			_, release, err := (LockManager{}).Acquire(workspace, &domain.Issue{Identifier: "CAG-1", ID: "issue-id"}, "am/CAG-1-workspace", now)
			if err != nil {
				t.Fatalf("Acquire error = %v", err)
			}
			release()
			if _, err := os.Stat(Path(workspace)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("lock exists after release: %v", err)
			}
		}},
		{name: "active lock rejection", run: func(t *testing.T, workspace string, now time.Time) {
			_, _, err := (LockManager{}).Acquire(workspace, &domain.Issue{Identifier: "CAG-1", ID: "issue-id"}, "am/CAG-1-workspace", now)
			if err != nil {
				t.Fatalf("first Acquire error = %v", err)
			}
			_, _, err = (LockManager{}).Acquire(workspace, &domain.Issue{Identifier: "CAG-1", ID: "issue-id"}, "am/CAG-1-workspace", now)
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

func TestLockManagerAcquireContextHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	workspace := filepath.Join(t.TempDir(), "CAG-199")

	_, _, err := (LockManager{}).AcquireContext(ctx, workspace, &domain.Issue{Identifier: "CAG-199", ID: "issue-id"}, "am/CAG-199-workspace", time.Now())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("AcquireContext() error = %v; want context.Canceled", err)
	}
	if _, statErr := os.Stat(workspace); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("workspace stat error = %v; want workspace not created", statErr)
	}
}

func TestLockManagerMirrorAcquireContextHonorsCanceledContext(t *testing.T) {
	store, err := state.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	lock := domain.RunLock{Owner: Owner(), PID: os.Getpid(), Host: Hostname(), IssueIdentifier: "CAG-199", IssueID: "issue-id", Branch: "am/CAG-199-workspace", Workspace: filepath.Join(t.TempDir(), "CAG-199"), StartedAt: now, HeartbeatAt: now}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	(LockManager{StateStore: store}).MirrorAcquireContext(ctx, lock)

	if _, ok, err := store.Lease(context.Background(), RunLockLeaseName(lock)); err != nil || ok {
		t.Fatalf("Lease() ok=%t err=%v; want no mirrored lease after cancellation", ok, err)
	}
}

func TestLockManagerCleanupStaleContextHonorsCanceledContext(t *testing.T) {
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	workspace := filepath.Join(t.TempDir(), "CAG-199")
	writeLockFixture(t, workspace, domain.RunLock{Workspace: workspace, IssueIdentifier: "CAG-199", Host: "other", PID: os.Getpid(), HeartbeatAt: now.Add(-RunLockStaleAfter - time.Second)})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	removed, err := (LockManager{}).CleanupStaleContext(ctx, filepath.Dir(workspace), now)

	if !errors.Is(err, context.Canceled) || removed != 0 {
		t.Fatalf("CleanupStaleContext() = (%d, %v), want canceled no removals", removed, err)
	}
	if _, err := os.Stat(Path(workspace)); err != nil {
		t.Fatalf("lock file stat after canceled cleanup = %v; want lock retained", err)
	}
}

func TestDescribeExistingStaleLockUsesCurrentRepairCommand(t *testing.T) {
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	workspace := filepath.Join(t.TempDir(), "CAG-199")
	writeLockFixture(t, workspace, domain.RunLock{
		Workspace:       workspace,
		IssueIdentifier: "CAG-199",
		Host:            "other",
		PID:             os.Getpid(),
		HeartbeatAt:     now.Add(-RunLockStaleAfter - time.Second),
		StartedAt:       now.Add(-RunLockStaleAfter - time.Second),
		Owner:           "owner",
		Branch:          "am/CAG-199-workspace",
	})

	err := DescribeExisting(Path(workspace), now)

	if err == nil || !strings.Contains(err.Error(), "go run . repair-artifacts --config <path>") {
		t.Fatalf("DescribeExisting() error = %v, want current repair command", err)
	}
	if strings.Contains(err.Error(), "bun run am:pi:repair-artifacts") {
		t.Fatalf("DescribeExisting() error = %v, contains stale repair command", err)
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
	oldLock := domain.RunLock{Owner: Owner(), PID: 999999, Host: Hostname(), IssueIdentifier: "CAG-113", IssueID: "issue-id", Branch: "am/CAG-113-workspace", Workspace: workspace, StartedAt: now.Add(-time.Hour), HeartbeatAt: now.Add(-time.Hour)}
	if acquired, err := store.AcquireLease(ctx, RunLockLease(oldLock, oldLock.HeartbeatAt), oldLock.HeartbeatAt); err != nil || !acquired {
		t.Fatalf("seed AcquireLease acquired=%v err=%v", acquired, err)
	}
	lock, release, err := (LockManager{StateStore: store}).Acquire(workspace, &domain.Issue{Identifier: "CAG-113", ID: "issue-id"}, "am/CAG-113-workspace", now)
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
	oldLock := domain.RunLock{Owner: Owner(), PID: os.Getpid(), Host: Hostname(), IssueIdentifier: "CAG-113", IssueID: "issue-id", Branch: "am/CAG-113-workspace", Workspace: workspace, StartedAt: now.Add(-time.Hour), HeartbeatAt: now.Add(-time.Hour)}
	if acquired, err := store.AcquireLease(ctx, RunLockLease(oldLock, oldLock.HeartbeatAt), oldLock.HeartbeatAt); err != nil || !acquired {
		t.Fatalf("seed AcquireLease acquired=%v err=%v", acquired, err)
	}
	_, _, err = (LockManager{StateStore: store}).Acquire(workspace, &domain.Issue{Identifier: "CAG-113", ID: "issue-id"}, "am/CAG-113-workspace", now)
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
