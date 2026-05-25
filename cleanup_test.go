package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sh "github.com/weskor/agent-machine/internal/shell"
	"github.com/weskor/agent-machine/internal/state"
)

func TestCleanupDecisionDeletesDoneIssueWorkspace(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-1")
	writeCleanRunArtifact(t, workspace, "success")

	decision, err := cleanupDecision(workspace, map[string]bool{"CAG-1": true})
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Delete || decision.Category != "completed" {
		t.Fatalf("expected Done issue workspace to be deleted, got %+v", decision)
	}
}

func TestCleanupDecisionKeepsTerminalArtifactsUntilIssueIsDone(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-2")
	writeCleanRunArtifact(t, workspace, "success")

	decision, err := cleanupDecision(workspace, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Delete || decision.Category != "not-done" {
		t.Fatalf("expected non-Done terminal workspace to be kept, got %+v", decision)
	}
}

func TestCleanupWorkspacesSkipsHiddenLockDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".am-locks"), 0o755); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "CAG-1")
	writeCleanRunArtifact(t, workspace, "success")

	if err := cleanupWorkspaces(root, cleanupOptions{DoneIssues: map[string]bool{"CAG-1": true}}); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupWorkspacesMirrorsDeletedState(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".am", "workspaces")
	workspace := filepath.Join(root, "CAG-65")
	writeCleanRunArtifact(t, workspace, "success")
	seedCleanupAttempt(t, root, workspace, "CAG-65", "success")

	if err := cleanupWorkspaces(root, cleanupOptions{Apply: true, DoneIssues: map[string]bool{"CAG-65": true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatalf("workspace still exists after cleanup: %v", err)
	}

	row := readCleanupState(t, root, "CAG-65")
	if row.workspaceExists != 0 || row.eligible != 1 || row.decision != "completed" || row.deletionResult != "deleted" || row.blockedReason != "" {
		t.Fatalf("unexpected cleanup mirror row: %+v", row)
	}
	if row.artifactRef != "" {
		t.Fatalf("artifact_ref = %q; want no artifact dependency for SQLite-backed cleanup", row.artifactRef)
	}
}

func TestCleanupWorkerDeletesDoneWorkspace(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".am", "workspaces")
	workspace := filepath.Join(root, "CAG-159")
	writeCleanRunArtifact(t, workspace, "success")
	seedCleanupAttempt(t, root, workspace, "CAG-159", "success")
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	safeRoot, err := safeWorkspaceRoot(root)
	if err != nil {
		t.Fatal(err)
	}

	if err := (cleanupWorker{workspaceRoot: root, safeRoot: safeRoot, options: cleanupOptions{Apply: true, DoneIssues: map[string]bool{"CAG-159": true}}, store: store}).Execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatalf("workspace still exists after cleanup worker: %v", err)
	}
	row := readCleanupState(t, root, "CAG-159")
	if row.workspaceExists != 0 || row.eligible != 1 || row.decision != "completed" || row.deletionResult != "deleted" {
		t.Fatalf("unexpected cleanup worker mirror row: %+v", row)
	}
}

func TestScheduleCleanupWorkerTasksEnqueuesWorkspacesWithoutDeleting(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".am", "workspaces")
	workspace := filepath.Join(root, "CAG-160")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	didWork, err := scheduleCleanupWorkerTasks(context.Background(), runnerConfig{WorkspaceRoot: root}, store)
	if err != nil {
		t.Fatal(err)
	}
	if !didWork {
		t.Fatal("didWork=false; want cleanup task enqueued")
	}
	tasks, err := store.WorkerTasks(context.Background(), cleanupWorkerRole)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].TaskKey != cleanupWorkerTaskKey("CAG-160") || tasks[0].Status != "queued" {
		t.Fatalf("cleanup tasks = %+v; want queued workspace task", tasks)
	}
	if _, err := os.Stat(workspace); err != nil {
		t.Fatalf("scheduler should not delete workspace: %v", err)
	}
}

func TestRunQueuedCleanupWorkerTaskClaimsTaskAndRefreshesDoneIssues(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".am", "workspaces")
	workspace := filepath.Join(root, "CAG-161")
	writeCleanRunArtifact(t, workspace, "success")
	seedCleanupAttempt(t, root, workspace, "CAG-161", "success")
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, enqueued, err := enqueueCleanupWorkerTask(context.Background(), store, "CAG-161", workspace, time.Now().UTC()); err != nil || !enqueued {
		t.Fatalf("enqueueCleanupWorkerTask() enqueued=%v err=%v", enqueued, err)
	}
	oldIssueIdentifiers := issueIdentifiersByStateForContinuousCleanup
	issueIdentifiersByStateForContinuousCleanup = func(ctx context.Context, client linearClient, projectSlug, stateName string) (map[string]bool, error) {
		if projectSlug != "CAG" || stateName != "Done" {
			t.Fatalf("Done issue refresh = project %q state %q; want CAG/Done", projectSlug, stateName)
		}
		return map[string]bool{"CAG-161": true}, nil
	}
	t.Cleanup(func() { issueIdentifiersByStateForContinuousCleanup = oldIssueIdentifiers })

	didWork, err := runQueuedCleanupWorkerTask(linearClient{}, runnerConfig{ProjectSlug: "CAG", DoneState: "Done", WorkspaceRoot: root}, store)
	if err != nil {
		t.Fatal(err)
	}
	if !didWork {
		t.Fatal("didWork=false; want queued cleanup task processed")
	}
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatalf("workspace still exists after queued cleanup: %v", err)
	}
	tasks, err := store.WorkerTasks(context.Background(), cleanupWorkerRole)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Status != "completed" {
		t.Fatalf("cleanup tasks = %+v; want completed queued task", tasks)
	}
	results, err := store.WorkerResults(context.Background(), cleanupWorkerRole)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Reason != "cleanup_deleted" {
		t.Fatalf("cleanup results = %+v; want cleanup_deleted result", results)
	}
}

func TestCleanupWorkspacesFailsClosedWhenCommandStateStoreUnavailable(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".am", "workspaces")
	workspace := filepath.Join(root, "CAG-107")
	writeCleanRunArtifact(t, workspace, "success")
	if err := os.WriteFile(filepath.Join(filepath.Dir(root), "state"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	store, _ := commandScopedStateStore(context.Background(), root, "cleanup-test")
	if store != nil {
		defer store.Close()
		t.Fatal("commandScopedStateStore succeeded; want degraded nil store")
	}
	if err := cleanupWorkspaces(root, cleanupOptions{Apply: true, DoneIssues: map[string]bool{"CAG-107": true}, StateStore: store}); err == nil || !strings.Contains(err.Error(), "requires SQLite") {
		t.Fatalf("expected fail-closed SQLite error, got %v", err)
	}
	if _, err := os.Stat(workspace); err != nil {
		t.Fatalf("workspace was deleted despite unavailable state store: %v", err)
	}
}

func TestCleanupWorkspacesDryRunDegradesWhenStateStoreUnavailable(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".am", "workspaces")
	workspace := filepath.Join(root, "CAG-108")
	writeCleanRunArtifact(t, workspace, "success")
	if err := os.WriteFile(filepath.Join(filepath.Dir(root), "state"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := cleanupWorkspaces(root, cleanupOptions{DoneIssues: map[string]bool{"CAG-108": true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(workspace); err != nil {
		t.Fatalf("dry-run degraded cleanup deleted workspace: %v", err)
	}
}

func TestCleanupWorkspacesMirrorsDryRunAndKeptState(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".am", "workspaces")
	dryRunWorkspace := filepath.Join(root, "CAG-66")
	keptWorkspace := filepath.Join(root, "CAG-67")
	writeCleanRunArtifact(t, dryRunWorkspace, "success")
	writeCleanRunArtifact(t, keptWorkspace, "success")
	seedCleanupAttempt(t, root, dryRunWorkspace, "CAG-66", "success")
	seedCleanupAttempt(t, root, keptWorkspace, "CAG-67", "success")

	if err := cleanupWorkspaces(root, cleanupOptions{DoneIssues: map[string]bool{"CAG-66": true}}); err != nil {
		t.Fatal(err)
	}

	dryRun := readCleanupState(t, root, "CAG-66")
	if dryRun.workspaceExists != 1 || dryRun.eligible != 1 || dryRun.decision != "completed" || dryRun.deletionResult != "dry_run" || dryRun.blockedReason != "" {
		t.Fatalf("unexpected dry-run cleanup mirror row: %+v", dryRun)
	}
	kept := readCleanupState(t, root, "CAG-67")
	if kept.workspaceExists != 1 || kept.eligible != 0 || kept.decision != "not-done" || kept.deletionResult != "kept" || !strings.Contains(kept.blockedReason, "not Done") {
		t.Fatalf("unexpected kept cleanup mirror row: %+v", kept)
	}
}

func TestCleanupDecisionFromSQLiteKeepsMissingDBRowForReconciliation(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".am", "workspaces")
	workspace := filepath.Join(root, "CAG-109")
	writeCleanRunArtifact(t, workspace, "success")
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	artifactDecision, err := cleanupDecisionForRoot(root, workspace, map[string]bool{"CAG-109": true})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := cleanupDecisionFromSQLite(context.Background(), store, root, workspace, map[string]bool{"CAG-109": true}, artifactDecision)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Delete || decision.Category != "reconciliation-needed" || !strings.Contains(decision.Reason, "no issue attempt row") {
		t.Fatalf("expected missing DB row reconciliation keep, got %+v", decision)
	}
}

func TestCleanupDecisionFromSQLiteTreatsStaleArtifactConflictAsReconciliation(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".am", "workspaces")
	workspace := filepath.Join(root, "CAG-110")
	writeCleanRunArtifact(t, workspace, "success")
	seedCleanupAttempt(t, root, workspace, "CAG-110", "failed")
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	artifactDecision := cleanupResult{IssueIdentifier: "CAG-other", Category: "completed", ArtifactRef: filepath.Join(workspace, ".am-run.json")}
	decision, err := cleanupDecisionFromSQLite(context.Background(), store, root, workspace, map[string]bool{"CAG-110": true}, artifactDecision)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Delete || decision.Category != "reconciliation-needed" || !strings.Contains(decision.Reason, "conflicts") {
		t.Fatalf("expected conflict reconciliation keep, got %+v", decision)
	}
}

func TestCleanupWorkspacesUsesSQLiteStatusWhenArtifactIsStale(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".am", "workspaces")
	workspace := filepath.Join(root, "CAG-111")
	writeCleanRunArtifact(t, workspace, "success")
	seedCleanupAttempt(t, root, workspace, "CAG-111", "failed")

	if err := cleanupWorkspaces(root, cleanupOptions{DoneIssues: map[string]bool{"CAG-111": true}}); err != nil {
		t.Fatal(err)
	}
	row := readCleanupState(t, root, "CAG-111")
	if row.decision != "failed" || row.deletionResult != "dry_run" {
		t.Fatalf("expected SQLite failed status to drive dry-run decision, got %+v", row)
	}
}

func TestCleanupWorkspacesUsesSQLiteStatusWhenArtifactIsMissing(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".am", "workspaces")
	workspace := filepath.Join(root, "CAG-162")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := sh.Run("git init -q", workspace); err != nil {
		t.Fatal(err)
	}
	seedCleanupAttempt(t, root, workspace, "CAG-162", "success")

	if err := cleanupWorkspaces(root, cleanupOptions{Apply: true, DoneIssues: map[string]bool{"CAG-162": true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatalf("workspace still exists after SQLite-backed cleanup without artifact: %v", err)
	}
	row := readCleanupState(t, root, "CAG-162")
	if row.decision != "completed" || row.deletionResult != "deleted" {
		t.Fatalf("expected SQLite success status to drive cleanup without artifact, got %+v", row)
	}
}

func TestCleanupWorkspacesIgnoresStaleArtifactIdentityWhenSQLiteFactsAreTerminal(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".am", "workspaces")
	workspace := filepath.Join(root, "CAG-163")
	writeCleanRunArtifact(t, workspace, "success")
	artifactPath := filepath.Join(workspace, ".am-run.json")
	data, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	stale := strings.Replace(string(data), `"issue_identifier":"CAG-163"`, `"issue_identifier":"CAG-other"`, 1)
	if err := os.WriteFile(artifactPath, []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	seedCleanupAttempt(t, root, workspace, "CAG-163", "success")

	if err := cleanupWorkspaces(root, cleanupOptions{Apply: true, DoneIssues: map[string]bool{"CAG-163": true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatalf("workspace still exists after SQLite-backed cleanup with stale artifact identity: %v", err)
	}
	row := readCleanupState(t, root, "CAG-163")
	if row.decision != "completed" || row.deletionResult != "deleted" {
		t.Fatalf("expected terminal SQLite facts to drive cleanup despite stale artifact identity, got %+v", row)
	}
}

func TestCleanupWorkspacesUsesSQLiteRunStatusWhenTerminalOutcomeIsHandoffReady(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".am", "workspaces")
	workspace := filepath.Join(root, "CAG-114")
	writeCleanRunArtifact(t, workspace, "success")
	seedCleanupAttemptWithOutcome(t, root, workspace, "CAG-114", "success", "handoff_ready")

	if err := cleanupWorkspaces(root, cleanupOptions{DoneIssues: map[string]bool{"CAG-114": true}}); err != nil {
		t.Fatal(err)
	}
	row := readCleanupState(t, root, "CAG-114")
	if row.decision != "completed" || row.deletionResult != "dry_run" {
		t.Fatalf("expected SQLite run status success to drive dry-run decision, got %+v", row)
	}
}

func TestCleanupWorkspacesEmitsSkippedAndStartedEvents(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".am", "workspaces")
	workspace := filepath.Join(root, "CAG-104")
	writeCleanRunArtifact(t, workspace, "success")
	seedCleanupAttemptWithOutcome(t, root, workspace, "CAG-104", "success", "handoff_ready")

	if err := cleanupWorkspaces(root, cleanupOptions{DoneIssues: map[string]bool{"CAG-104": true}}); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	events, err := store.Events(context.Background(), state.EventFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]int{}
	for _, event := range events {
		types[event.Type]++
	}
	if types[state.EventCleanupStarted] != 1 || types[state.EventCleanupCandidateFound] != 1 || types[state.EventCleanupSkipped] != 1 || types[state.EventCleanupCompleted] != 0 {
		t.Fatalf("cleanup event counts = %#v, want started, candidate_found, skipped, and no completed for dry-run", types)
	}
}

func TestCleanupWorkspacesEmitsCompletedEventOnApply(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".am", "workspaces")
	workspace := filepath.Join(root, "CAG-105")
	writeCleanRunArtifact(t, workspace, "success")
	seedCleanupAttemptWithOutcome(t, root, workspace, "CAG-105", "success", "handoff_ready")

	if err := cleanupWorkspaces(root, cleanupOptions{Apply: true, DoneIssues: map[string]bool{"CAG-105": true}}); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	events, err := store.Events(context.Background(), state.EventFilter{IssueKey: "CAG-105", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	types := eventTypeCounts(events)
	for _, eventType := range []string{state.EventCleanupCandidateFound, state.EventCleanupDeletionAttempted, state.EventCleanupDeletionSucceeded, state.EventCleanupCompleted} {
		if types[eventType] != 1 {
			t.Fatalf("%s events = %d, want 1; all=%#v", eventType, types[eventType], types)
		}
	}
	if types[state.EventCleanupSkipped] != 0 || types[state.EventCleanupDeletionFailed] != 0 {
		t.Fatalf("unexpected skip/failure cleanup events: %#v", types)
	}
}

func TestCleanupWorkspacesUsesSQLiteRunStatusWhenTerminalOutcomeIsOperationalFailure(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".am", "workspaces")
	workspace := filepath.Join(root, "CAG-115")
	writeCleanRunArtifact(t, workspace, "failed")
	seedCleanupAttemptWithOutcome(t, root, workspace, "CAG-115", "failed", "operational_failure")

	if err := cleanupWorkspaces(root, cleanupOptions{DoneIssues: map[string]bool{"CAG-115": true}}); err != nil {
		t.Fatal(err)
	}
	row := readCleanupState(t, root, "CAG-115")
	if row.decision != "failed" || row.deletionResult != "dry_run" {
		t.Fatalf("expected SQLite run status failed to drive dry-run decision, got %+v", row)
	}
}

func TestCleanupWorkspacesDeletesDoneMergedPRWithNonTerminalSQLiteStatus(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".am", "workspaces")
	workspace := filepath.Join(root, "CAG-125")
	writeCleanRunArtifact(t, workspace, "review_not_ready")
	seedCleanupAttemptWithPR(t, root, workspace, "CAG-125", "review_not_ready", "https://github.com/weskor/agent-machine/pull/125", expectedWorkspaceBranch("CAG-125"))
	stubCleanupPRFactsForURL(t, func(prURL string) (cleanupPRFacts, error) {
		if prURL != "https://github.com/weskor/agent-machine/pull/125" {
			t.Fatalf("unexpected PR URL lookup: %s", prURL)
		}
		return cleanupPRFacts{State: "MERGED", Merged: true, HeadRefName: expectedWorkspaceBranch("CAG-125"), BaseRefName: "main"}, nil
	})

	if err := cleanupWorkspaces(root, cleanupOptions{Apply: true, DoneIssues: map[string]bool{"CAG-125": true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatalf("expected merged Done workspace to be deleted, stat err=%v", err)
	}
	row := readCleanupState(t, root, "CAG-125")
	if row.decision != "merged-pr" || row.deletionResult != "deleted" {
		t.Fatalf("expected merged PR cleanup state, got %+v", row)
	}
}

func TestCleanupDecisionKeepsDoneNonTerminalClosedUnmergedPRForReconciliation(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".am", "workspaces")
	workspace := filepath.Join(root, "CAG-126")
	writeCleanRunArtifact(t, workspace, "review_not_ready")
	seedCleanupAttemptWithPR(t, root, workspace, "CAG-126", "review_not_ready", "https://github.com/weskor/agent-machine/pull/126", expectedWorkspaceBranch("CAG-126"))
	stubCleanupPRFactsForURL(t, func(string) (cleanupPRFacts, error) {
		return cleanupPRFacts{State: "CLOSED", HeadRefName: expectedWorkspaceBranch("CAG-126"), BaseRefName: "main"}, nil
	})
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	decision, err := cleanupDecisionFromSQLite(context.Background(), store, root, workspace, map[string]bool{"CAG-126": true}, cleanupResult{IssueIdentifier: "CAG-126", Category: "non-terminal"})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Delete || decision.Category != "reconciliation-needed" || !strings.Contains(decision.Reason, "state CLOSED") {
		t.Fatalf("expected closed-unmerged PR to require reconciliation, got %+v", decision)
	}
}

func TestCleanupDecisionKeepsDoneNonTerminalMissingPRMappingForReconciliation(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".am", "workspaces")
	workspace := filepath.Join(root, "CAG-127")
	writeCleanRunArtifact(t, workspace, "review_not_ready")
	seedCleanupAttemptWithPR(t, root, workspace, "CAG-127", "review_not_ready", "", expectedWorkspaceBranch("CAG-127"))
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	decision, err := cleanupDecisionFromSQLite(context.Background(), store, root, workspace, map[string]bool{"CAG-127": true}, cleanupResult{IssueIdentifier: "CAG-127", Category: "non-terminal"})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Delete || decision.Category != "reconciliation-needed" || !strings.Contains(decision.Reason, "no PR mapping") {
		t.Fatalf("expected missing PR mapping to require reconciliation, got %+v", decision)
	}
}

func TestCleanupDecisionKeepsDoneNonTerminalWrongBranchPRForReconciliation(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".am", "workspaces")
	workspace := filepath.Join(root, "CAG-128")
	writeCleanRunArtifact(t, workspace, "review_not_ready")
	seedCleanupAttemptWithPR(t, root, workspace, "CAG-128", "review_not_ready", "https://github.com/weskor/agent-machine/pull/128", "am/other-workspace")
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	decision, err := cleanupDecisionFromSQLite(context.Background(), store, root, workspace, map[string]bool{"CAG-128": true}, cleanupResult{IssueIdentifier: "CAG-128", Category: "non-terminal"})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Delete || decision.Category != "reconciliation-needed" || !strings.Contains(decision.Reason, "does not match expected branch") {
		t.Fatalf("expected wrong branch PR mapping to require reconciliation, got %+v", decision)
	}
}

func TestCleanupDecisionKeepsDoneNonTerminalWrongPRHeadForReconciliation(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".am", "workspaces")
	workspace := filepath.Join(root, "CAG-129")
	writeCleanRunArtifact(t, workspace, "review_not_ready")
	seedCleanupAttemptWithPR(t, root, workspace, "CAG-129", "review_not_ready", "https://github.com/weskor/agent-machine/pull/129", expectedWorkspaceBranch("CAG-129"))
	stubCleanupPRFactsForURL(t, func(string) (cleanupPRFacts, error) {
		return cleanupPRFacts{State: "MERGED", Merged: true, HeadRefName: "am/other-workspace", BaseRefName: "main"}, nil
	})
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	decision, err := cleanupDecisionFromSQLite(context.Background(), store, root, workspace, map[string]bool{"CAG-129": true}, cleanupResult{IssueIdentifier: "CAG-129", Category: "non-terminal"})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Delete || decision.Category != "reconciliation-needed" || !strings.Contains(decision.Reason, "head branch") {
		t.Fatalf("expected wrong PR head branch to require reconciliation, got %+v", decision)
	}
}

func TestCleanupDecisionKeepsDoneNonTerminalPRLookupFailureForReconciliation(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".am", "workspaces")
	workspace := filepath.Join(root, "CAG-130")
	writeCleanRunArtifact(t, workspace, "review_not_ready")
	seedCleanupAttemptWithPR(t, root, workspace, "CAG-130", "review_not_ready", "https://github.com/weskor/agent-machine/pull/130", expectedWorkspaceBranch("CAG-130"))
	stubCleanupPRFactsForURL(t, func(string) (cleanupPRFacts, error) {
		return cleanupPRFacts{}, errors.New("GitHub unavailable")
	})
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	decision, err := cleanupDecisionFromSQLite(context.Background(), store, root, workspace, map[string]bool{"CAG-130": true}, cleanupResult{IssueIdentifier: "CAG-130", Category: "non-terminal"})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Delete || decision.Category != "reconciliation-needed" || !strings.Contains(decision.Reason, "lookup failed") {
		t.Fatalf("expected PR lookup failure to require reconciliation, got %+v", decision)
	}
}

func TestCleanupDecisionPreservesDirtyDoneWorkspaces(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-3")
	writeCleanRunArtifact(t, workspace, "success")
	if err := os.WriteFile(filepath.Join(workspace, "tracked.txt"), []byte("dirty"), 0o600); err != nil {
		t.Fatal(err)
	}

	decision, err := cleanupDecision(workspace, map[string]bool{"CAG-3": true})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Delete || decision.Category != "dirty" {
		t.Fatalf("expected dirty Done workspace to be preserved, got %+v", decision)
	}
}

func TestCleanupDecisionKeepsMissingAndInsufficientArtifacts(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-4")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := sh.Run("git init -q", workspace); err != nil {
		t.Fatal(err)
	}

	decision, err := cleanupDecision(workspace, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Delete || decision.Reason != "missing run artifact" {
		t.Fatalf("expected missing artifact keep, got %+v", decision)
	}

	if err := os.WriteFile(filepath.Join(workspace, ".am-run.json"), []byte(`{"status":"success","ended_at":"2026-05-01T00:00:00Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	decision, err = cleanupDecision(workspace, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Delete || !strings.Contains(decision.Reason, "missing required") {
		t.Fatalf("expected insufficient artifact keep, got %+v", decision)
	}
}

func TestCleanupDecisionRequiresReviewEvidenceForFailedReview(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-5")
	writeCleanRunArtifact(t, workspace, "review_failed")

	decision, err := cleanupDecision(workspace, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Delete || !strings.Contains(decision.Reason, "review evidence") {
		t.Fatalf("expected missing review evidence keep, got %+v", decision)
	}

	record := runArtifactJSON(workspace, "review_failed")
	record = strings.TrimSuffix(record, "}") + `,"pr_url":"https://github.com/acme/repo/pull/1","review_status":"failed","review_findings":"needs changes"}`
	if err := os.WriteFile(filepath.Join(workspace, ".am-run.json"), []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
	decision, err = cleanupDecision(workspace, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Delete || decision.Category != "not-done" {
		t.Fatalf("expected complete failed-review artifact to wait for Done, got %+v", decision)
	}
}

func TestCleanupRefusesUnsafeDeletePaths(t *testing.T) {
	root := t.TempDir()
	if _, err := safeWorkspacePath(root, "../outside"); err == nil {
		t.Fatal("expected path traversal workspace name to be refused")
	}
	if err := assertSafeDeletePath(root, root); err == nil {
		t.Fatal("expected root delete path to be refused")
	}
	if err := assertSafeDeletePath(root, filepath.Dir(root)); err == nil {
		t.Fatal("expected parent delete path to be refused")
	}
	workspace, err := safeWorkspacePath(root, "CAG-6")
	if err != nil {
		t.Fatal(err)
	}
	if workspace != filepath.Join(root, "CAG-6") {
		t.Fatalf("unexpected safe workspace path %s", workspace)
	}
}

func TestTerminalRunStatusIncludesArtifactPolicyStatuses(t *testing.T) {
	for _, status := range []string{"success", "review_failed", "failed", "github_app_error", "canceled", "cancelled", "needs_info", "timeout", "budget_exceeded", "merged", "superseded", "manual_repair"} {
		if !terminalRunStatus(status) {
			t.Fatalf("expected %s to be terminal", status)
		}
	}
}

func TestCleanupDecisionCategoriesTerminalStatuses(t *testing.T) {
	statuses := map[string]string{"success": "completed", "canceled": "canceled", "cancelled": "canceled", "failed": "failed", "github_app_error": "failed", "needs_info": "needs-info", "timeout": "timeout", "budget_exceeded": "budget-exceeded", "merged": "merged", "superseded": "superseded", "manual_repair": "manual-repair"}
	for status, category := range statuses {
		if got := cleanupCategoryForTerminalStatus(status); got != category {
			t.Fatalf("category for %s = %s, want %s", status, got, category)
		}
	}
}

func TestCleanupDecisionRequiresTerminalDoneArtifact(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-41")
	writeCleanRunArtifact(t, workspace, "handoff_failed")
	decision, err := cleanupDecision(workspace, map[string]bool{"CAG-41": true})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Delete || decision.Category != "non-terminal" {
		t.Fatalf("expected non-terminal Done workspace to be preserved, got %+v", decision)
	}
}

func TestCleanupDecisionRequiresMatchingWorkspaceRootAndBranch(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-40")
	writeCleanRunArtifact(t, workspace, "success")
	record := runArtifactJSON(workspace, "success")
	record = strings.TrimSuffix(record, "}") + `,"workspace_root":"/tmp/not-this-root","expected_branch":"am/CAG-40-workspace","branch":"am/other"}`
	if err := os.WriteFile(filepath.Join(workspace, ".am-run.json"), []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
	decision, err := cleanupDecision(workspace, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Delete || !strings.Contains(decision.Reason, "workspace root mismatch") {
		t.Fatalf("expected workspace root mismatch keep, got %+v", decision)
	}
}

func writeCleanRunArtifact(t *testing.T, workspace, status string) {
	t.Helper()
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := sh.Run("git init -q", workspace); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".am-run.json"), []byte(runArtifactJSON(workspace, status)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runArtifactJSON(workspace, status string) string {
	return `{"issue_identifier":"` + filepath.Base(workspace) + `","issue_id":"issue-id","issue_title":"Title","issue_url":"https://linear.app/acme/issue/` + filepath.Base(workspace) + `/title","workspace":"` + filepath.ToSlash(workspace) + `","branch":"am/` + filepath.Base(workspace) + `","status":"` + status + `","ended_at":"2026-05-01T00:00:00Z"}`
}

func seedCleanupAttempt(t *testing.T, workspaceRoot, workspace, issueKey, status string) {
	seedCleanupAttemptWithOutcome(t, workspaceRoot, workspace, issueKey, status, "")
}

func seedCleanupAttemptWithOutcome(t *testing.T, workspaceRoot, workspace, issueKey, status, outcome string) {
	t.Helper()
	seedCleanupAttemptSnapshot(t, workspaceRoot, state.RunArtifactSnapshot{IssueKey: issueKey, Attempt: 1, WorkspacePath: workspace, BranchName: "am/" + issueKey, BaseBranch: "main", Status: status, TerminalOutcome: outcome, TerminalReason: "test terminal outcome", RunArtifactRef: filepath.Join(workspace, ".am-run.json")})
}

func seedCleanupAttemptWithPR(t *testing.T, workspaceRoot, workspace, issueKey, status, prURL, branch string) {
	t.Helper()
	seedCleanupAttemptSnapshot(t, workspaceRoot, state.RunArtifactSnapshot{IssueKey: issueKey, Attempt: 1, WorkspacePath: workspace, BranchName: branch, BaseBranch: "main", Status: status, PRURL: prURL, RunArtifactRef: filepath.Join(workspace, ".am-run.json")})
}

func seedCleanupAttemptSnapshot(t *testing.T, workspaceRoot string, snap state.RunArtifactSnapshot) {
	t.Helper()
	ctx := context.Background()
	store, err := state.Open(ctx, state.DefaultDBPath(workspaceRoot))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	if snap.StartedAt.IsZero() {
		snap.StartedAt = now
	}
	if snap.UpdatedAt.IsZero() {
		snap.UpdatedAt = now
	}
	if err := store.UpsertRunArtifact(ctx, snap); err != nil {
		t.Fatal(err)
	}
}

func stubCleanupPRFactsForURL(t *testing.T, fn func(string) (cleanupPRFacts, error)) {
	t.Helper()
	original := cleanupPRFactsForURL
	cleanupPRFactsForURL = fn
	t.Cleanup(func() { cleanupPRFactsForURL = original })
}

type cleanupStateFixture struct {
	workspaceExists int
	eligible        int
	decision        string
	deletionResult  string
	artifactRef     string
	blockedReason   string
}

func readCleanupState(t *testing.T, workspaceRoot, issueKey string) cleanupStateFixture {
	t.Helper()
	db, err := sql.Open("sqlite", state.DefaultDBPath(workspaceRoot))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var row cleanupStateFixture
	if err := db.QueryRow(`SELECT c.workspace_exists, c.eligible, c.decision, c.deletion_result, c.artifact_ref, c.blocked_reason FROM cleanup_states c JOIN issue_attempts a ON a.id = c.attempt_id WHERE a.issue_key = ?`, issueKey).Scan(&row.workspaceExists, &row.eligible, &row.decision, &row.deletionResult, &row.artifactRef, &row.blockedReason); err != nil {
		t.Fatal(err)
	}
	return row
}
