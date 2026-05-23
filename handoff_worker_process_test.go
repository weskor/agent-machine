package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weskor/pi-symphony/internal/state"
)

func TestRunHandoffPendingAttemptConsumesPayloadAndCompletesHandoff(t *testing.T) {
	t.Cleanup(resetHandoffWorkerHooks)
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-171")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	candidate := &issue{ID: "issue-171", Identifier: "CAG-171", Title: "Pending handoff", URL: "https://linear.app/acme/issue/CAG-171"}
	prURL := "https://github.com/acme/repo/pull/171"
	input := handoffCompletion{
		config:          runnerConfig{WorkspaceRoot: root, PiCommand: "pi run"},
		candidate:       candidate,
		workspace:       workspace,
		branch:          expectedWorkspaceBranch(candidate.Identifier),
		progressStarted: time.Now().Add(-2 * time.Minute),
		startedAt:       time.Now().Add(-time.Minute),
		piUsage:         &usage{TotalTokens: 171},
		review:          &reviewResult{Status: "passed", Classification: "clean"},
		prURL:           prURL,
		validation:      []string{"go test ./..."},
		githubAuth:      "github_app_installation",
	}
	writeHandoffPendingState(input)

	var postedSummary handoffSummary
	var commentIssueID, commentBody string
	postOrUpdatePRHandoffCommentForWorker = func(summary handoffSummary) error {
		postedSummary = summary
		return nil
	}
	createCommentForLinearStatusWorker = func(client linearClient, issueID, body string) error {
		commentIssueID = issueID
		commentBody = body
		return nil
	}
	updateIssueStateForLinearStatusWorker = func(linearClient, string, string) error {
		t.Fatal("handoff worker should not move Linear state when handoff state is not configured")
		return nil
	}

	didWork, err := runHandoffPendingAttempt(linearClient{}, runnerConfig{WorkspaceRoot: root, PiCommand: "pi run"}, store)
	if err != nil {
		t.Fatalf("runHandoffPendingAttempt() error = %v", err)
	}
	if !didWork {
		t.Fatal("runHandoffPendingAttempt() didWork=false; want pending handoff consumed")
	}
	if postedSummary.PRURL != prURL || postedSummary.IssueIdentifier != candidate.Identifier || postedSummary.Review.Status != "passed" {
		t.Fatalf("posted summary = %+v; want payload handoff facts", postedSummary)
	}
	if commentIssueID != candidate.ID || !strings.Contains(commentBody, prURL) || !strings.Contains(commentBody, "passed") {
		t.Fatalf("Linear comment issue=%q body=%q; want handoff payload comment", commentIssueID, commentBody)
	}
	progress, err := readRunProgress(root, candidate.Identifier)
	if err != nil {
		t.Fatal(err)
	}
	if progress.Phase != "completed" || progress.Status != runAttemptStatusSuccess || progress.PRURL != prURL {
		t.Fatalf("progress = %+v; want completed successful handoff", progress)
	}
	lease, ok, err := store.Lease(context.Background(), "run:"+candidate.Identifier)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || lease.ReleasedAt.IsZero() {
		t.Fatalf("run lease = %+v ok=%t; want released handoff claim lease", lease, ok)
	}
}

func TestRunHandoffPendingAttemptSkipsNonPendingProgress(t *testing.T) {
	t.Cleanup(resetHandoffWorkerHooks)
	root := t.TempDir()
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	candidate := &issue{ID: "issue-172", Identifier: "CAG-172", Title: "Already complete"}
	progress := runProgressForIssue(candidate, filepath.Join(root, candidate.Identifier), "completed", time.Now())
	progress.Status = runAttemptStatusSuccess
	if err := writeRunProgressResult(root, progress); err != nil {
		t.Fatal(err)
	}
	postOrUpdatePRHandoffCommentForWorker = func(handoffSummary) error {
		t.Fatal("handoff worker should skip non-pending progress")
		return nil
	}

	didWork, err := runHandoffPendingAttempt(linearClient{}, runnerConfig{WorkspaceRoot: root, PiCommand: "pi run"}, store)
	if err != nil {
		t.Fatalf("runHandoffPendingAttempt() error = %v", err)
	}
	if didWork {
		t.Fatal("runHandoffPendingAttempt() didWork=true; want idle for non-pending progress")
	}
}

func TestRunHandoffPendingAttemptSkipsPendingProgressWithActiveRunLock(t *testing.T) {
	t.Cleanup(resetHandoffWorkerHooks)
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-173")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	candidate := &issue{ID: "issue-173", Identifier: "CAG-173", Title: "Active inline handoff"}
	branch := expectedWorkspaceBranch(candidate.Identifier)
	writeHandoffPendingState(handoffCompletion{
		config:          runnerConfig{WorkspaceRoot: root, PiCommand: "pi run"},
		candidate:       candidate,
		workspace:       workspace,
		branch:          branch,
		progressStarted: time.Now().Add(-time.Minute),
		startedAt:       time.Now().Add(-time.Minute),
		review:          &reviewResult{Status: "passed"},
		prURL:           "https://github.com/acme/repo/pull/173",
	})
	_, releaseLock, err := acquireRunLockWithState(store, workspace, candidate, branch, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer releaseLock()
	postOrUpdatePRHandoffCommentForWorker = func(handoffSummary) error {
		t.Fatal("handoff worker should not consume a pending handoff with an active run lock")
		return nil
	}

	didWork, err := runHandoffPendingAttempt(linearClient{}, runnerConfig{WorkspaceRoot: root, PiCommand: "pi run"}, store)
	if err != nil {
		t.Fatalf("runHandoffPendingAttempt() error = %v", err)
	}
	if didWork {
		t.Fatal("runHandoffPendingAttempt() didWork=true; want idle while inline run lock is active")
	}
}
