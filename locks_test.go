package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAcquireRunLockWritesOwnerAndReleaseRemovesLock(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "CAG-21")
	candidate := testIssue("CAG-21", "In Progress")
	now := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)

	lock, release, err := acquireRunLock(workspace, &candidate, "symphony/CAG-21", now)
	if err != nil {
		t.Fatal(err)
	}
	if lock.IssueIdentifier != "CAG-21" || lock.Branch != "symphony/CAG-21" || lock.Workspace != workspace || lock.PID == 0 {
		t.Fatalf("unexpected lock: %#v", lock)
	}
	if _, err := os.Stat(runLockPath(workspace)); err != nil {
		t.Fatalf("expected lock file: %v", err)
	}
	release()
	if _, err := os.Stat(runLockPath(workspace)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected released lock to be removed, err=%v", err)
	}
}

func TestAcquireRunLockDoesNotDirtyFreshWorkspace(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "CAG-21")
	candidate := testIssue("CAG-21", "In Progress")

	_, release, err := acquireRunLock(workspace, &candidate, "symphony/CAG-21", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	entries, err := os.ReadDir(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected fresh workspace to remain empty for git clone bootstrap, got %d entries", len(entries))
	}
}

func TestAcquireRunLockAllowsFreshWorkspaceClone(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	workspace := filepath.Join(root, "CAG-21")
	candidate := testIssue("CAG-21", "In Progress")

	runGit(t, "", "init", source)
	runGit(t, source, "config", "user.email", "agent@example.test")
	runGit(t, source, "config", "user.name", "Pi Agent")
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("ready\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "add", "README.md")
	runGit(t, source, "commit", "-m", "initial")

	_, release, err := acquireRunLock(workspace, &candidate, "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	runGit(t, "", "clone", source, workspace)
	if _, err := os.Stat(filepath.Join(workspace, ".git")); err != nil {
		t.Fatalf("expected cloned workspace: %v", err)
	}
	if _, err := os.Stat(runLockPath(workspace)); err != nil {
		t.Fatalf("expected external run lock to remain after clone: %v", err)
	}
}

func TestAcquireRunLockConflictReturnsOperatorDetails(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "CAG-21")
	candidate := testIssue("CAG-21", "In Progress")
	_, release, err := acquireRunLock(workspace, &candidate, "branch-a", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	_, _, err = acquireRunLock(workspace, &candidate, "branch-b", time.Now())
	if !errors.Is(err, errRunLocked) {
		t.Fatalf("expected errRunLocked, got %v", err)
	}
	if !strings.Contains(err.Error(), "pid=") || !strings.Contains(err.Error(), "CAG-21") {
		t.Fatalf("conflict error missing operator details: %v", err)
	}
}

func TestAcquireRunLockStaleLockIsConservative(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "CAG-21")
	candidate := testIssue("CAG-21", "In Progress")
	stale := time.Now().Add(-runLockStaleAfter - time.Hour)
	lock := runLock{Owner: "agent", PID: 123, Host: "host", IssueIdentifier: "CAG-21", Branch: "branch-a", Workspace: workspace, StartedAt: stale, HeartbeatAt: stale}
	writeRunLockFixture(t, workspace, lock)

	_, _, err := acquireRunLock(workspace, &candidate, "branch-b", time.Now())
	if !errors.Is(err, errRunLocked) {
		t.Fatalf("expected stale lock to remain blocking, got %v", err)
	}
	if !strings.Contains(err.Error(), "repair-artifacts") || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale lock error missing cleanup guidance: %v", err)
	}
}

func TestCleanupStaleRunLocksRemovesOnlyStaleLocks(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	staleWorkspace := filepath.Join(root, "CAG-1")
	activeWorkspace := filepath.Join(root, "CAG-2")
	writeRunLockFixture(t, staleWorkspace, runLock{IssueIdentifier: "CAG-1", Workspace: staleWorkspace, HeartbeatAt: now.Add(-runLockStaleAfter - time.Second)})
	writeRunLockFixture(t, activeWorkspace, runLock{IssueIdentifier: "CAG-2", Workspace: activeWorkspace, HeartbeatAt: now})

	removed, err := cleanupStaleRunLocks(root, now)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed=%d, want 1", removed)
	}
	if _, err := os.Stat(runLockPath(staleWorkspace)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected stale lock removed, err=%v", err)
	}
	if _, err := os.Stat(runLockPath(activeWorkspace)); err != nil {
		t.Fatalf("expected active lock kept: %v", err)
	}
}

func TestCleanupStaleRunLocksRemovesDeadOwnerLocksOnSameHost(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	deadWorkspace := filepath.Join(root, "CAG-33")
	activeWorkspace := filepath.Join(root, "CAG-34")
	writeRunLockFixture(t, deadWorkspace, runLock{IssueIdentifier: "CAG-33", Workspace: deadWorkspace, Host: hostname(), PID: 99999999, HeartbeatAt: now})
	writeRunLockFixture(t, activeWorkspace, runLock{IssueIdentifier: "CAG-34", Workspace: activeWorkspace, Host: hostname(), PID: os.Getpid(), HeartbeatAt: now})

	removed, err := cleanupStaleRunLocks(root, now)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed=%d, want 1", removed)
	}
	if _, err := os.Stat(runLockPath(deadWorkspace)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected dead-owner lock removed, err=%v", err)
	}
	if _, err := os.Stat(runLockPath(activeWorkspace)); err != nil {
		t.Fatalf("expected active current-process lock kept: %v", err)
	}
}

func TestIsEmptyIgnoringRunLockAllowsBootstrap(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "CAG-1")
	candidate := testIssue("CAG-1", "In Progress")
	_, release, err := acquireRunLock(workspace, &candidate, "symphony/CAG-1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if !isEmptyIgnoringRunLock(workspace) {
		t.Fatal("expected workspace with only lock to be bootstrap-eligible")
	}
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	if isEmptyIgnoringRunLock(workspace) {
		t.Fatal("expected workspace with non-lock file to be non-empty")
	}
}

func writeRunLockFixture(t *testing.T, workspace string, lock runLock) {
	t.Helper()
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(runLockPath(workspace)), 0o755); err != nil {
		t.Fatal(err)
	}
	if lock.PID == 0 {
		lock.PID = 123
	}
	data, err := json.Marshal(lock)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runLockPath(workspace), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, output)
	}
}
