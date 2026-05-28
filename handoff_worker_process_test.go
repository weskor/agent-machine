package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sh "github.com/weskor/agent-machine/internal/shell"
	"github.com/weskor/agent-machine/internal/state"
)

func TestRunHandoffPendingAttemptConsumesPRHandoffPayloadAndQueuesReview(t *testing.T) {
	t.Cleanup(resetHandoffWorkerHooks)
	t.Cleanup(resetReviewWorkerHooks)
	t.Setenv("GITHUB_REPOSITORY", "weskor/agent-machine")
	root := t.TempDir()
	workspace := runnerHandoffGitWorkspace(t, "main")
	candidate := &issue{ID: "issue-180", Identifier: "CAG-180", Title: "Pending PR handoff", URL: "https://linear.app/acme/issue/CAG-180"}
	candidate.Team.ID = "team-180"
	branch := expectedWorkspaceBranch(candidate.Identifier)
	if err := sh.Run("git switch -q -C "+sh.Quote(branch)+" && echo change > handoff.go", workspace); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	input := prHandoffInput{
		Candidate:        candidate,
		Workspace:        workspace,
		AgentPRURL:       "",
		ProgressStarted:  time.Now().Add(-2 * time.Minute),
		AttemptStartedAt: time.Now().Add(-time.Minute),
		RuntimeUsage:     &usage{TotalTokens: 180},
		ScopeResult:      scopeGuardResult{Checked: true},
		Validation:       []string{"go test ./..."},
		GitHubAuth:       "github_app_installation",
		StateStore:       store,
	}
	if err := writePRHandoffPendingState(runnerConfig{WorkspaceRoot: root, PiCommand: "pi run", GitHubAppSlug: "agent-machine-bot"}, input); err != nil {
		t.Fatal(err)
	}
	githubAppEnvFromEnvironmentForHandoffWorker = func() (map[string]string, string, error) {
		return map[string]string{"GITHUB_TOKEN": "token"}, "github_app_installation", nil
	}
	t.Cleanup(func() { githubAppEnvFromEnvironmentForHandoffWorker = githubAppEnvFromEnvironment })
	withFakeGitHubAPI(t, fakeGitHubAPI{})

	didWork, err := runHandoffPendingAttempt(linearClient{}, runnerConfig{WorkspaceRoot: root, PiCommand: "pi run", ReviewCommand: "pi review", BaseBranch: "main", GitHubAppSlug: "agent-machine-bot"}, store)
	if err != nil {
		t.Fatalf("runHandoffPendingAttempt() error = %v", err)
	}
	if !didWork {
		t.Fatal("runHandoffPendingAttempt() didWork=false; want PR handoff consumed")
	}
	progress, err := readRunProgress(root, candidate.Identifier)
	if err != nil {
		t.Fatal(err)
	}
	if progress.Phase != runProgressPhaseReviewPending || progress.PRURL != "https://github.com/weskor/agent-machine/pull/900" || progress.ReviewPayloadPath == "" {
		t.Fatalf("progress = %+v; want review_pending after PR handoff", progress)
	}
	reviewPayload, err := readReviewPendingPayload(root, candidate.Identifier)
	if err != nil {
		t.Fatal(err)
	}
	if reviewPayload.PRURL != progress.PRURL || reviewPayload.RuntimeUsage.TotalTokens != 180 || len(reviewPayload.Validation) != 1 || reviewPayload.GitHubAuth != "github_app_installation" {
		t.Fatalf("review payload = %+v; want PR handoff continuation facts", reviewPayload)
	}
	lease, ok, err := store.Lease(context.Background(), "run:"+candidate.Identifier)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || lease.ReleasedAt.IsZero() {
		t.Fatalf("run lease = %+v ok=%t; want released PR handoff claim lease", lease, ok)
	}
}

func TestWritePRHandoffPendingStateContextHonorsCanceledContextBeforeExports(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-180")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	candidate := &issue{ID: "issue-180", Identifier: "CAG-180", Title: "Pending PR handoff"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = writePRHandoffPendingStateContext(ctx, runnerConfig{WorkspaceRoot: root, PiCommand: "pi run"}, prHandoffInput{
		Candidate:        candidate,
		Workspace:        workspace,
		ProgressStarted:  time.Now().Add(-2 * time.Minute),
		AttemptStartedAt: time.Now().Add(-time.Minute),
		StateStore:       store,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("writePRHandoffPendingStateContext() error = %v, want canceled", err)
	}
	path, err := prHandoffPendingPayloadPath(root, candidate.Identifier)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("PR handoff pending payload stat err=%v, want missing", err)
	}
	refs, err := store.PendingWorkerPayloadRefs(context.Background(), handoffWorkerRole, runProgressPhasePRHandoffPending)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 0 {
		t.Fatalf("PR handoff pending refs = %+v, want none", refs)
	}
}

func TestClaimNextPRHandoffPendingAttemptUsesSQLitePayloadRefWithoutProgressSnapshot(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-185")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	candidate := &issue{ID: "issue-185", Identifier: "CAG-185", Title: "State PR handoff"}
	branch := expectedWorkspaceBranch(candidate.Identifier)
	payload := prHandoffPendingPayloadFromInput(prHandoffInput{Candidate: candidate, Workspace: workspace, ProgressStarted: time.Now().Add(-time.Minute), AttemptStartedAt: time.Now().Add(-time.Minute)})
	payload.Branch = branch
	if err := writePRHandoffPendingPayload(root, payload); err != nil {
		t.Fatal(err)
	}
	payloadPath, err := prHandoffPendingPayloadPath(root, candidate.Identifier)
	if err != nil {
		t.Fatal(err)
	}
	if err := recordPRHandoffPendingPayloadRef(store, payload, payloadPath); err != nil {
		t.Fatal(err)
	}
	intent, ok, err := store.PRHandoffIntent(context.Background(), candidate.Identifier, 1)
	if err != nil || !ok {
		t.Fatalf("PRHandoffIntent() ok=%t err=%v", ok, err)
	}
	if intent.Status != state.PRHandoffIntentStatusPending || intent.PayloadPath != payloadPath {
		t.Fatalf("intent = %+v; want pending PR handoff intent", intent)
	}

	claim, didWork, err := claimNextPRHandoffPendingAttempt(runnerConfig{WorkspaceRoot: root, PiCommand: "pi run"}, store)
	if err != nil {
		t.Fatalf("claimNextPRHandoffPendingAttempt() error = %v", err)
	}
	if !didWork || claim == nil {
		t.Fatalf("claim = %+v didWork=%t; want SQLite PR handoff claim", claim, didWork)
	}
	defer claim.ReleaseLock()
	if claim.Payload.IssueIdentifier != candidate.Identifier || claim.Payload.Workspace != workspace || claim.PayloadRef == nil {
		t.Fatalf("claim = %+v; want PR handoff payload from SQLite ref", claim)
	}
	if _, err := readRunProgress(root, candidate.Identifier); err == nil {
		t.Fatal("readRunProgress() succeeded; test should not create a progress snapshot for discovery")
	}
}

func TestRunHandoffPendingAttemptSkipsPRHandoffWithActiveRunLock(t *testing.T) {
	t.Cleanup(resetHandoffWorkerHooks)
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-181")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	candidate := &issue{ID: "issue-181", Identifier: "CAG-181", Title: "Active PR handoff"}
	branch := expectedWorkspaceBranch(candidate.Identifier)
	if err := writePRHandoffPendingState(runnerConfig{WorkspaceRoot: root, PiCommand: "pi run"}, prHandoffInput{Candidate: candidate, Workspace: workspace, ProgressStarted: time.Now().Add(-time.Minute), AttemptStartedAt: time.Now().Add(-time.Minute), StateStore: store}); err != nil {
		t.Fatal(err)
	}
	_, releaseLock, err := acquireRunLockWithState(store, workspace, candidate, branch, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer releaseLock()
	githubAppEnvFromEnvironmentForHandoffWorker = func() (map[string]string, string, error) {
		t.Fatal("handoff worker should not consume a pending PR handoff with an active run lock")
		return nil, "", nil
	}
	t.Cleanup(func() { githubAppEnvFromEnvironmentForHandoffWorker = githubAppEnvFromEnvironment })

	didWork, err := runHandoffPendingAttempt(linearClient{}, runnerConfig{WorkspaceRoot: root, PiCommand: "pi run"}, store)
	if err != nil {
		t.Fatalf("runHandoffPendingAttempt() error = %v", err)
	}
	if didWork {
		t.Fatal("runHandoffPendingAttempt() didWork=true; want idle while inline run lock is active")
	}
}

func TestClaimNextHandoffPendingAttemptUsesSQLitePayloadRefWithoutProgressSnapshot(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-186")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	candidate := &issue{ID: "issue-186", Identifier: "CAG-186", Title: "State final handoff"}
	payload := handoffPendingPayloadFromCompletion(handoffCompletion{
		config:          runnerConfig{WorkspaceRoot: root, PiCommand: "pi run"},
		candidate:       candidate,
		workspace:       workspace,
		branch:          expectedWorkspaceBranch(candidate.Identifier),
		progressStarted: time.Now().Add(-time.Minute),
		startedAt:       time.Now().Add(-time.Minute),
		review:          &reviewResult{Status: "passed"},
		prURL:           "https://github.com/acme/repo/pull/186",
	})
	if err := writeHandoffPendingPayload(root, payload); err != nil {
		t.Fatal(err)
	}
	payloadPath, err := handoffPendingPayloadPath(root, candidate.Identifier)
	if err != nil {
		t.Fatal(err)
	}
	if err := recordHandoffPendingPayloadRef(store, payload, payloadPath); err != nil {
		t.Fatal(err)
	}

	claim, didWork, err := claimNextHandoffPendingAttempt(runnerConfig{WorkspaceRoot: root, PiCommand: "pi run"}, store)
	if err != nil {
		t.Fatalf("claimNextHandoffPendingAttempt() error = %v", err)
	}
	if !didWork || claim == nil {
		t.Fatalf("claim = %+v didWork=%t; want SQLite handoff claim", claim, didWork)
	}
	defer claim.ReleaseLock()
	if claim.Payload.PRURL != payload.PRURL || claim.Payload.IssueIdentifier != candidate.Identifier || claim.PayloadRef == nil {
		t.Fatalf("claim = %+v; want handoff payload from SQLite ref", claim)
	}
	if _, err := readRunProgress(root, candidate.Identifier); err == nil {
		t.Fatal("readRunProgress() succeeded; test should not create a progress snapshot for discovery")
	}
}

func TestWriteHandoffPendingStateContextHonorsCanceledContextBeforeExports(t *testing.T) {
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
	candidate := &issue{ID: "issue-171", Identifier: "CAG-171", Title: "Pending handoff"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	writeHandoffPendingStateContext(ctx, handoffCompletion{
		config:          runnerConfig{WorkspaceRoot: root, PiCommand: "pi run"},
		stateStore:      store,
		candidate:       candidate,
		workspace:       workspace,
		branch:          expectedWorkspaceBranch(candidate.Identifier),
		progressStarted: time.Now().Add(-2 * time.Minute),
		startedAt:       time.Now().Add(-time.Minute),
		prURL:           "https://github.com/acme/repo/pull/171",
	})
	path, err := handoffPendingPayloadPath(root, candidate.Identifier)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("handoff pending payload stat err=%v, want missing", err)
	}
	refs, err := store.PendingWorkerPayloadRefs(context.Background(), handoffWorkerRole, runProgressPhaseHandoffPending)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 0 {
		t.Fatalf("handoff pending refs = %+v, want none", refs)
	}
}

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
		stateStore:      store,
		candidate:       candidate,
		workspace:       workspace,
		branch:          expectedWorkspaceBranch(candidate.Identifier),
		progressStarted: time.Now().Add(-2 * time.Minute),
		startedAt:       time.Now().Add(-time.Minute),
		runtimeUsage:    &usage{TotalTokens: 171},
		review:          &reviewResult{Status: "passed", Classification: "clean"},
		prURL:           prURL,
		validation:      []string{"go test ./..."},
		githubAuth:      "github_app_installation",
	}
	writeHandoffPendingState(input)

	var postedSummary handoffSummary
	var commentIssueID, commentBody string
	updatePRHandoffBodyForWorker = func(summary handoffSummary) error {
		postedSummary = summary
		return nil
	}
	createCommentForLinearStatusWorker = func(ctx context.Context, client linearClient, issueID, body string) error {
		commentIssueID = issueID
		commentBody = body
		return nil
	}
	updateIssueStateForLinearStatusWorker = func(context.Context, linearClient, string, string) error {
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
	updatePRHandoffBodyForWorker = func(handoffSummary) error {
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
		stateStore:      store,
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
	updatePRHandoffBodyForWorker = func(handoffSummary) error {
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
