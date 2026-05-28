package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sh "github.com/weskor/agent-machine/internal/shell"
	"github.com/weskor/agent-machine/internal/state"
)

type failingReconciliationReader struct{ err error }

func (f failingReconciliationReader) ReconciliationFacts(context.Context, string) (state.ReconciliationFacts, bool, error) {
	return state.ReconciliationFacts{}, false, f.err
}

func (f failingReconciliationReader) Lease(context.Context, string) (state.Lease, bool, error) {
	return state.Lease{}, false, f.err
}

func TestReconcileIssueAllowsFeedbackRetryToSupersedeTerminalArtifact(t *testing.T) {
	root := t.TempDir()
	writeRunRecordFixture(t, root, "CAG-34", `{"status":"success","pr_url":"https://github.com/weskor/agent-machine/pull/434"}`)
	workspace := filepath.Join(root, "CAG-34")
	if err := os.WriteFile(filepath.Join(workspace, ".am-feedback.md"), []byte("unresolved feedback"), 0o600); err != nil {
		t.Fatal(err)
	}

	decision := reconcileIssue(testRunnerConfig(root), testIssue("CAG-34", "Ready for Agent"), nil)

	if !decision.CanRun || !decision.ShouldRetry || decision.Lifecycle != lifecycleFeedbackRetry {
		t.Fatalf("expected feedback retry runnable decision, got %#v", decision)
	}
}

func TestReconcileIssueBlocksTerminalArtifactWithoutNewFeedback(t *testing.T) {
	root := t.TempDir()
	writeRunRecordFixture(t, root, "CAG-34", `{"status":"success","pr_url":"https://github.com/weskor/agent-machine/pull/434"}`)

	decision := reconcileIssue(testRunnerConfig(root), testIssue("CAG-34", "Ready for Agent"), nil)

	if decision.CanRun || decision.Lifecycle != lifecycleHandoffReady || !strings.Contains(strings.Join(decision.Blockers, "; "), "terminal success artifact") {
		t.Fatalf("expected terminal artifact block, got %#v", decision)
	}
}

func TestReconcileIssueFailClosedWhenStateReaderFails(t *testing.T) {
	root := t.TempDir()
	reader := failingReconciliationReader{err: errors.New("database unavailable")}

	decision := newReconciliationModule(reader).ReconcileIssue(testRunnerConfig(root), testIssue("CAG-36", "Ready for Agent"), nil)

	if decision.CanRun || !decision.ReconciliationNeeded || decision.StateStoreError == nil || decision.NextAction != "repair_sqlite_state_store" {
		t.Fatalf("expected fail-closed degraded SQLite reconciliation, got %#v", decision)
	}
}

func TestReconcileIssueContextHonorsCanceledContext(t *testing.T) {
	root := t.TempDir()
	store := openTestStateStore(t, root)
	defer store.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	decision := newReconciliationModule(store).ReconcileIssueContext(ctx, testRunnerConfig(root), testIssue("CAG-36", "Ready for Agent"), nil)

	if decision.CanRun || !decision.ReconciliationNeeded || !errors.Is(decision.StateStoreError, context.Canceled) || decision.NextAction != "retry_when_worker_context_active" {
		t.Fatalf("expected canceled context to fail closed, got %#v", decision)
	}
}

func TestReconcileIssueBlocksPRWithoutSQLiteAttemptState(t *testing.T) {
	root := t.TempDir()
	store := openTestStateStore(t, root)
	defer store.Close()
	config := testRunnerConfig(root)
	config.HandoffState = "Human Review"
	config.BaseBranch = "develop"
	pr := pullRequestSummary{
		Number:            434,
		URL:               "https://github.com/weskor/agent-machine/pull/434",
		BaseRefName:       "develop",
		HeadRefName:       expectedWorkspaceBranch("CAG-34"),
		Author:            prAuthor{Login: githubAppPRAuthorLogin},
		ReviewDecision:    "APPROVED",
		Mergeable:         "MERGEABLE",
		StatusCheckRollup: []statusCheck{{Typename: "CheckRun", Status: "COMPLETED", Conclusion: "SUCCESS", Name: "build"}},
	}

	decision := newReconciliationModule(store).ReconcileIssue(config, testIssue("CAG-34", "Human Review"), &pr)

	if decision.CanMerge || !decision.ReconciliationNeeded || decision.NextAction != "repair_sqlite_state_store" || !strings.Contains(strings.Join(decision.Blockers, "; "), "SQLite has no attempt state") {
		t.Fatalf("expected missing SQLite attempt state to block PR decision, got %#v", decision)
	}
}

func TestReconcileIssueDoesNotFallbackToArtifactWhenSQLiteStatusMissing(t *testing.T) {
	root := t.TempDir()
	store := openTestStateStore(t, root)
	defer store.Close()
	config := testRunnerConfig(root)
	config.HandoffState = "Human Review"
	config.BaseBranch = "develop"
	workspace := filepath.Join(root, "CAG-37")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := sh.Run("git init -q", workspace); err != nil {
		t.Fatal(err)
	}
	writeRunRecordFixture(t, root, "CAG-37", `{"status":"success","review_status":"passed","pr_url":"https://github.com/weskor/agent-machine/pull/437"}`)
	if err := store.UpsertAttemptResult(context.Background(), state.AttemptResult{
		IssueKey:      "CAG-37",
		IssueID:       "issue-id",
		Attempt:       1,
		WorkspacePath: workspace,
		BranchName:    expectedWorkspaceBranch("CAG-37"),
		BaseBranch:    "develop",
		PRURL:         "https://github.com/weskor/agent-machine/pull/437",
		ReviewStatus:  "passed",
		ReviewPassed:  true,
		UpdatedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	pr := pullRequestSummary{
		Number:            437,
		URL:               "https://github.com/weskor/agent-machine/pull/437",
		BaseRefName:       "develop",
		HeadRefName:       expectedWorkspaceBranch("CAG-37"),
		Author:            prAuthor{Login: githubAppPRAuthorLogin},
		ReviewDecision:    "APPROVED",
		Mergeable:         "MERGEABLE",
		StatusCheckRollup: []statusCheck{{Typename: "CheckRun", Status: "COMPLETED", Conclusion: "SUCCESS", Name: "build"}},
	}

	decision := newReconciliationModule(store).ReconcileIssue(config, testIssue("CAG-37", "Human Review"), &pr)

	if decision.CanMerge || !decision.ReconciliationNeeded || decision.NextAction != "repair_sqlite_state_store" || !strings.Contains(strings.Join(decision.Blockers, "; "), "SQLite attempt status is missing") {
		t.Fatalf("expected missing SQLite status to block merge without artifact fallback, got %#v", decision)
	}
}

func TestReconcileIssueAllowsChangesRequestedToSupersedeReviewFailedArtifact(t *testing.T) {
	root := t.TempDir()
	writeRunRecordFixture(t, root, "CAG-35", `{"status":"review_failed","review_status":"failed","pr_url":"https://github.com/weskor/agent-machine/pull/440"}`)
	config := testRunnerConfig(root)
	config.BaseBranch = "develop"
	pr := pullRequestSummary{Number: 440, URL: "https://github.com/weskor/agent-machine/pull/440", BaseRefName: "develop", HeadRefName: expectedWorkspaceBranch("CAG-35"), Author: prAuthor{Login: githubAppPRAuthorLogin}, ReviewDecision: "CHANGES_REQUESTED"}

	decision := reconcileIssue(config, testIssue("CAG-35", "Ready for Agent"), &pr)

	if !decision.CanRun || !decision.ShouldRetry || decision.Lifecycle != lifecycleFeedbackRetry || decision.NextAction != "capture_feedback_and_retry" {
		t.Fatalf("expected CHANGES_REQUESTED to be runnable feedback retry, got %#v", decision)
	}
}

func TestReconcileIssueAllowsReviewReadinessRetryAfterChecksSucceed(t *testing.T) {
	root := t.TempDir()
	config := testRunnerConfig(root)
	config.BaseBranch = "develop"
	store := openTestStateStore(t, root)
	prURL := "https://github.com/weskor/agent-machine/pull/522"
	upsertReviewNotReadyAttempt(t, store, testIssue("CAG-122", "In Progress"), filepath.Join(root, "CAG-122"), prURL)
	pr := pullRequestSummary{Number: 522, URL: prURL, BaseRefName: "develop", HeadRefName: expectedWorkspaceBranch("CAG-122"), Author: prAuthor{Login: githubAppPRAuthorLogin}, StatusCheckRollup: []statusCheck{{Typename: "CheckRun", Status: "COMPLETED", Conclusion: "SUCCESS", Name: "build"}}}

	decision := newReconciliationModule(store).ReconcileIssue(config, testIssue("CAG-122", "In Progress"), &pr)

	if !decision.CanRun || !decision.ShouldRetry || decision.NextAction != "run_semantic_review_after_checks_ready" {
		t.Fatalf("expected review readiness retry, got %#v", decision)
	}
}

func TestReconcileIssueKeepsFailedChecksBlockedBeforeReview(t *testing.T) {
	root := t.TempDir()
	config := testRunnerConfig(root)
	config.BaseBranch = "develop"
	store := openTestStateStore(t, root)
	prURL := "https://github.com/weskor/agent-machine/pull/522"
	upsertReviewNotReadyAttempt(t, store, testIssue("CAG-122", "In Progress"), filepath.Join(root, "CAG-122"), prURL)
	pr := pullRequestSummary{Number: 522, URL: prURL, BaseRefName: "develop", HeadRefName: expectedWorkspaceBranch("CAG-122"), Author: prAuthor{Login: githubAppPRAuthorLogin}, StatusCheckRollup: []statusCheck{{Typename: "CheckRun", Status: "COMPLETED", Conclusion: "FAILURE", Name: "build"}}}

	decision := newReconciliationModule(store).ReconcileIssue(config, testIssue("CAG-122", "In Progress"), &pr)

	if decision.CanRun || decision.NextAction == "run_semantic_review_after_checks_ready" {
		t.Fatalf("expected failed checks to remain blocked, got %#v", decision)
	}
}

func TestReconcileIssueIgnoresStaleFailedArtifactWhenCurrentPRIsRetryable(t *testing.T) {
	root := t.TempDir()
	writeRunRecordFixture(t, root, "CAG-110", `{"status":"review_failed","pr_url":"https://github.com/weskor/agent-machine/pull/110"}`)
	config := testRunnerConfig(root)
	config.BaseBranch = "develop"
	pr := pullRequestSummary{Number: 110, URL: "https://github.com/weskor/agent-machine/pull/110", BaseRefName: "develop", HeadRefName: expectedWorkspaceBranch("CAG-110"), Author: prAuthor{Login: githubAppPRAuthorLogin}, ReviewDecision: "CHANGES_REQUESTED"}

	decision := reconcileIssue(config, testIssue("CAG-110", "Ready for Agent"), &pr)

	if !decision.CanRun || !decision.ShouldRetry || decision.Lifecycle != lifecycleFeedbackRetry {
		t.Fatalf("expected current PR review facts to supersede stale failed artifact, got %#v", decision)
	}
}

func TestReconcileIssueIgnoresReviewFailedArtifactWhenStateBacked(t *testing.T) {
	root := t.TempDir()
	writeRunRecordFixture(t, root, "CAG-115", `{"status":"review_failed","pr_url":"https://github.com/weskor/agent-machine/pull/115"}`)
	store := openTestStateStore(t, root)
	defer store.Close()

	decision := newReconciliationModule(store).ReconcileIssue(testRunnerConfig(root), testIssue("CAG-115", "Ready for Agent"), nil)

	if !decision.CanRun || decision.ReconciliationNeeded || decision.NextAction != "run_agent" {
		t.Fatalf("expected state-backed reconciliation to ignore review_failed artifact, got %#v", decision)
	}
}

func TestReconcileIssueBlocksReviewFailedSQLiteStateWithoutPR(t *testing.T) {
	root := t.TempDir()
	store := openTestStateStore(t, root)
	defer store.Close()
	if err := store.UpsertAttemptResult(context.Background(), state.AttemptResult{
		IssueKey:             "CAG-116",
		Attempt:              1,
		BranchName:           expectedWorkspaceBranch("CAG-116"),
		Status:               runAttemptStatusReviewFailed,
		ReviewStatus:         "failed",
		ReviewClassification: reviewClassificationBehaviorSpecBlocker,
		UpdatedAt:            time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	decision := newReconciliationModule(store).ReconcileIssue(testRunnerConfig(root), testIssue("CAG-116", "Ready for Agent"), nil)

	if decision.CanRun || decision.Lifecycle != lifecycleReviewFailed || decision.NextAction != "repair_review_findings_before_retry" {
		t.Fatalf("expected SQLite review_failed state to block retry without PR, got %#v", decision)
	}
}

func TestReconcileIssueUsesSQLiteStateWhenWorkspaceDeleted(t *testing.T) {
	root := t.TempDir()
	store := openTestStateStore(t, root)
	if err := store.UpsertRunArtifact(context.Background(), state.RunArtifactSnapshot{IssueKey: "CAG-111", Attempt: 1, BranchName: expectedWorkspaceBranch("CAG-111"), BaseBranch: "develop", Status: "success", Repository: "weskor/agent-machine", PRNumber: 111, PRURL: "https://github.com/weskor/agent-machine/pull/111", TerminalOutcome: "handoff_ready"}); err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	decision := newReconciliationModule(store).ReconcileIssue(testRunnerConfig(root), testIssue("CAG-111", "Ready for Agent"), nil)

	if decision.Lifecycle != lifecycleBlocked || !decision.ReconciliationNeeded || !strings.Contains(strings.Join(decision.Blockers, "; "), "SQLite PR mapping") {
		t.Fatalf("expected durable missing-PR mapping reconciliation, got %#v", decision)
	}
}

func TestReconcileIssueBlocksActiveSQLiteLease(t *testing.T) {
	root := t.TempDir()
	store := openTestStateStore(t, root)
	now := time.Now().UTC()
	if err := store.UpsertLease(context.Background(), state.Lease{Name: "run:CAG-112", Scope: root, Owner: "daemon", AcquiredAt: now, RenewedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	decision := newReconciliationModule(store).ReconcileIssue(testRunnerConfig(root), testIssue("CAG-112", "Ready for Agent"), nil)

	if decision.CanRun || decision.Lifecycle != lifecycleRunning || !strings.Contains(strings.Join(decision.Blockers, "; "), "SQLite run lease") {
		t.Fatalf("expected active SQLite lease block, got %#v", decision)
	}
}

func TestReconcileIssueReportsClosedOrMissingPRMapping(t *testing.T) {
	root := t.TempDir()
	store := openTestStateStore(t, root)
	if err := store.UpsertRunArtifact(context.Background(), state.RunArtifactSnapshot{IssueKey: "CAG-113", Attempt: 1, BranchName: expectedWorkspaceBranch("CAG-113"), BaseBranch: "develop", Status: "success", Repository: "weskor/agent-machine", PRNumber: 113, PRURL: "https://github.com/weskor/agent-machine/pull/113", TerminalOutcome: "handoff_ready"}); err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	decision := newReconciliationModule(store).ReconcileIssue(testRunnerConfig(root), testIssue("CAG-113", "Human Review"), nil)

	if decision.CanRun || !decision.ReconciliationNeeded || decision.NextAction != "reconcile_missing_or_closed_pr_mapping" {
		t.Fatalf("expected missing current PR to require reconciliation, got %#v", decision)
	}
}

func TestRunReconciliationScanRecordsReconciliationNeededEvent(t *testing.T) {
	root := t.TempDir()
	store := openTestStateStore(t, root)
	candidate := testIssue("CAG-182", "Ready for Agent")
	if err := store.UpsertRunArtifact(context.Background(), state.RunArtifactSnapshot{IssueKey: candidate.Identifier, IssueID: candidate.ID, Attempt: 1, BranchName: expectedWorkspaceBranch(candidate.Identifier), Status: "success", PRURL: "https://github.com/weskor/agent-machine/pull/182", TerminalOutcome: "handoff_ready"}); err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	oldCandidates := candidatesForReconciliationWorker
	oldOpenPRs := openPRsForReconciliationWorker
	oldArtifacts := artifactSummariesForReconciliationWorker
	t.Cleanup(func() {
		candidatesForReconciliationWorker = oldCandidates
		openPRsForReconciliationWorker = oldOpenPRs
		artifactSummariesForReconciliationWorker = oldArtifacts
	})
	candidatesForReconciliationWorker = func(client linearClient, config runnerConfig) ([]issue, error) {
		return []issue{candidate}, nil
	}
	openPRsForReconciliationWorker = func(runnerConfig) ([]pullRequestSummary, error) {
		return nil, nil
	}
	artifactSummariesForReconciliationWorker = func(workspaceRoot string) ([]artifactSummary, error) {
		return nil, nil
	}

	didWork, err := runReconciliationScan(linearClient{}, testRunnerConfig(root), store)
	if err != nil {
		t.Fatalf("runReconciliationScan() error = %v", err)
	}
	if !didWork {
		t.Fatal("runReconciliationScan() didWork=false; want scan to run")
	}
	events, err := store.Events(context.Background(), state.EventFilter{IssueKey: candidate.Identifier, Type: state.EventReconciliationNeeded})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Source != "reconciliation" || events[0].IssueID != candidate.ID || events[0].Attempt != 1 {
		t.Fatalf("events = %+v; want one reconciliation-needed event with issue and attempt", events)
	}
	var payload map[string]any
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["next_action"] != "reconcile_missing_or_closed_pr_mapping" || payload["reconciliation_needed"] != true || payload["sqlite_pr_url"] != "https://github.com/weskor/agent-machine/pull/182" {
		t.Fatalf("payload = %+v; want reconciliation-needed evidence", payload)
	}
}

func TestRunReconciliationScanContextHonorsCanceledContext(t *testing.T) {
	root := t.TempDir()
	store := openTestStateStore(t, root)
	defer store.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	called := false
	oldCandidates := candidatesForReconciliationWorker
	t.Cleanup(func() { candidatesForReconciliationWorker = oldCandidates })
	candidatesForReconciliationWorker = func(client linearClient, config runnerConfig) ([]issue, error) {
		called = true
		return nil, nil
	}

	didWork, err := runReconciliationScanContext(ctx, linearClient{}, testRunnerConfig(root), store)
	if !errors.Is(err, context.Canceled) || didWork {
		t.Fatalf("runReconciliationScanContext() = (%t, %v), want canceled no work", didWork, err)
	}
	if called {
		t.Fatal("reconciliation scan fetched candidates after context cancellation")
	}
}

func TestReconcileIssueTerminalSQLiteOutcomeIgnoresImplicitStaleArtifact(t *testing.T) {
	root := t.TempDir()
	writeRunRecordFixture(t, root, "CAG-114", `{"status":"review_failed"}`)
	store := openTestStateStore(t, root)
	if err := store.UpsertRunArtifact(context.Background(), state.RunArtifactSnapshot{IssueKey: "CAG-114", Attempt: 1, BranchName: expectedWorkspaceBranch("CAG-114"), Status: "merged", TerminalOutcome: "merged"}); err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	decision := newReconciliationModule(store).ReconcileIssue(testRunnerConfig(root), testIssue("CAG-114", "Ready for Agent"), nil)

	if decision.Lifecycle != lifecycleDone || decision.NextAction != "cleanup_workspace" || decision.ReconciliationNeeded {
		t.Fatalf("expected implicit stale artifact to be ignored by state-backed decision, got %#v", decision)
	}
}

func TestReconcileIssueWithArtifactReportsExplicitStaleArtifactConflict(t *testing.T) {
	root := t.TempDir()
	store := openTestStateStore(t, root)
	if err := store.UpsertRunArtifact(context.Background(), state.RunArtifactSnapshot{IssueKey: "CAG-114", Attempt: 1, BranchName: expectedWorkspaceBranch("CAG-114"), Status: "merged", TerminalOutcome: "merged"}); err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	artifact := artifactSummary{Issue: "CAG-114", HasArtifact: true, Status: "review_failed"}
	decision := newReconciliationModule(store).ReconcileIssueWithArtifact(testRunnerConfig(root), testIssue("CAG-114", "Ready for Agent"), nil, &artifact)

	if decision.Lifecycle != lifecycleBlocked || decision.NextAction != "repair_artifact_or_sqlite_state" || !decision.ReconciliationNeeded {
		t.Fatalf("expected explicit stale artifact conflict to require repair, got %#v", decision)
	}
}

func openTestStateStore(t *testing.T, workspaceRoot string) *state.Store {
	t.Helper()
	store, err := state.Open(context.Background(), state.DefaultDBPath(workspaceRoot))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestReconcileIssueQuarantinesWrongBaseAuthorAndHead(t *testing.T) {
	root := t.TempDir()
	config := testRunnerConfig(root)
	config.HandoffState = "Human Review"
	config.BaseBranch = "develop"
	writeRunRecordFixture(t, root, "CAG-34", `{"status":"success","review_status":"passed","pr_url":"https://github.com/weskor/agent-machine/pull/434"}`)
	pr := pullRequestSummary{
		Number:            434,
		URL:               "https://github.com/weskor/agent-machine/pull/434",
		BaseRefName:       "main",
		HeadRefName:       "feature/CAG-34",
		Author:            prAuthor{Login: "human"},
		ReviewDecision:    "APPROVED",
		Mergeable:         "MERGEABLE",
		StatusCheckRollup: []statusCheck{{Typename: "CheckRun", Status: "COMPLETED", Conclusion: "SUCCESS", Name: "build"}},
	}

	decision := reconcileIssue(config, testIssue("CAG-34", "Human Review"), &pr)

	joined := strings.Join(decision.Blockers, "; ")
	for _, expected := range []string{"base branch", "head branch", "PR author"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("expected %q in blockers %q", expected, joined)
		}
	}
	if !decision.ShouldQuarantine || decision.CanMerge || decision.Lifecycle != lifecycleQuarantined {
		t.Fatalf("expected quarantined non-mergeable decision, got %#v", decision)
	}
}

func TestReconcileIssueApprovesMergeOnlyWithCleanInvariants(t *testing.T) {
	root := t.TempDir()
	config := testRunnerConfig(root)
	config.HandoffState = "Human Review"
	config.BaseBranch = "develop"
	workspace := filepath.Join(root, "CAG-34")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := sh.Run("git init -q", workspace); err != nil {
		t.Fatal(err)
	}
	writeRunRecordFixture(t, root, "CAG-34", `{"status":"success","review_status":"passed","pr_url":"https://github.com/weskor/agent-machine/pull/434"}`)
	pr := pullRequestSummary{
		Number:            434,
		URL:               "https://github.com/weskor/agent-machine/pull/434",
		BaseRefName:       "develop",
		HeadRefName:       expectedWorkspaceBranch("CAG-34"),
		Author:            prAuthor{Login: githubAppPRAuthorLogin},
		ReviewDecision:    "APPROVED",
		Mergeable:         "MERGEABLE",
		StatusCheckRollup: []statusCheck{{Typename: "CheckRun", Status: "COMPLETED", Conclusion: "SUCCESS", Name: "build"}},
	}

	decision := reconcileIssue(config, testIssue("CAG-34", "Human Review"), &pr)

	if !decision.CanMerge || decision.Lifecycle != lifecycleMergeReady || len(decision.Blockers) != 0 {
		t.Fatalf("expected merge-ready decision, got %#v", decision)
	}
}

func TestReconcileIssueDistinguishesPRAuthorFromCommitAuthor(t *testing.T) {
	root := t.TempDir()
	config := testRunnerConfig(root)
	config.HandoffState = "Human Review"
	config.BaseBranch = "develop"
	workspace := filepath.Join(root, "CAG-34")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := sh.Run("git init -q", workspace); err != nil {
		t.Fatal(err)
	}
	writeRunRecordFixture(t, root, "CAG-34", `{"status":"success","review_status":"passed","pr_url":"https://github.com/weskor/agent-machine/pull/434"}`)

	validPR := pullRequestSummary{
		Number:            434,
		URL:               "https://github.com/weskor/agent-machine/pull/434",
		BaseRefName:       "develop",
		HeadRefName:       expectedWorkspaceBranch("CAG-34"),
		Author:            prAuthor{Login: githubAppPRAuthorLogin},
		Commits:           []prCommit{{Author: prCommitAuthor{Name: githubAppBotName, Email: githubAppBotEmail}}},
		ReviewDecision:    "APPROVED",
		Mergeable:         "MERGEABLE",
		StatusCheckRollup: []statusCheck{{Typename: "CheckRun", Status: "COMPLETED", Conclusion: "SUCCESS", Name: "build"}},
	}

	if decision := reconcileIssue(config, testIssue("CAG-34", "Human Review"), &validPR); !decision.CanMerge {
		t.Fatalf("expected GitHub App PR author login to be merge-ready, got %#v", decision)
	}

	restPR := validPR
	restPR.Author = prAuthor{Login: githubAppRESTPRAuthorLogin}
	if decision := reconcileIssue(config, testIssue("CAG-34", "Human Review"), &restPR); !decision.CanMerge {
		t.Fatalf("expected REST GitHub App bot PR author login to be merge-ready, got %#v", decision)
	}

	humanPR := validPR
	humanPR.Author = prAuthor{Login: "weskor"}
	decision := reconcileIssue(config, testIssue("CAG-34", "Human Review"), &humanPR)
	if !decision.ShouldQuarantine || !strings.Contains(strings.Join(decision.Blockers, "; "), "PR author") {
		t.Fatalf("expected human PR author to be quarantined, got %#v", decision)
	}

	unrelatedBotPR := validPR
	unrelatedBotPR.Author = prAuthor{Login: "unrelated-bot[bot]"}
	decision = reconcileIssue(config, testIssue("CAG-34", "Human Review"), &unrelatedBotPR)
	if !decision.ShouldQuarantine || !strings.Contains(strings.Join(decision.Blockers, "; "), "PR author") {
		t.Fatalf("expected unrelated bot PR author to be rejected, got %#v", decision)
	}

	wrongCommitAuthorPR := validPR
	wrongCommitAuthorPR.Commits = []prCommit{{Author: prCommitAuthor{Name: "Wes", Email: "wes@example.com"}}}
	decision = reconcileIssue(config, testIssue("CAG-34", "Human Review"), &wrongCommitAuthorPR)
	if !decision.ShouldQuarantine || !strings.Contains(strings.Join(decision.Blockers, "; "), "commit author") {
		t.Fatalf("expected wrong commit author to be rejected, got %#v", decision)
	}
}

func TestReconcileIssueDerivesGitHubAppIdentityFromConfiguredAppSlug(t *testing.T) {
	root := t.TempDir()
	config := testRunnerConfig(root)
	config.HandoffState = "Human Review"
	config.BaseBranch = "develop"
	config.GitHubAppSlug = "custom-bot"
	workspace := filepath.Join(root, "CAG-84")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := sh.Run("git init -q", workspace); err != nil {
		t.Fatal(err)
	}
	writeRunRecordFixture(t, root, "CAG-84", `{"status":"success","review_status":"passed","pr_url":"https://github.com/weskor/agent-machine/pull/84"}`)

	validPR := pullRequestSummary{
		Number:            84,
		URL:               "https://github.com/weskor/agent-machine/pull/84",
		BaseRefName:       "develop",
		HeadRefName:       expectedWorkspaceBranch("CAG-84"),
		Author:            prAuthor{Login: "custom-bot[bot]"},
		Commits:           []prCommit{{Author: prCommitAuthor{Name: "custom-bot[bot]", Email: "custom-bot[bot]@users.noreply.github.com"}}},
		ReviewDecision:    "APPROVED",
		Mergeable:         "MERGEABLE",
		StatusCheckRollup: []statusCheck{{Typename: "CheckRun", Status: "COMPLETED", Conclusion: "SUCCESS", Name: "build"}},
	}

	if decision := reconcileIssue(config, testIssue("CAG-84", "Human Review"), &validPR); !decision.CanMerge {
		t.Fatalf("expected configured GitHub App bot PR author to be merge-ready, got %#v", decision)
	}

	appPR := validPR
	appPR.Author = prAuthor{Login: "app/custom-bot"}
	if decision := reconcileIssue(config, testIssue("CAG-84", "Human Review"), &appPR); !decision.CanMerge {
		t.Fatalf("expected configured GitHub App REST PR author to be merge-ready, got %#v", decision)
	}

	wrongCommitAuthorPR := validPR
	wrongCommitAuthorPR.Commits = []prCommit{{Author: prCommitAuthor{Name: "agent-machine-bot[bot]", Email: "agent-machine-bot[bot]@users.noreply.github.com"}}}
	decision := reconcileIssue(config, testIssue("CAG-84", "Human Review"), &wrongCommitAuthorPR)
	if !decision.ShouldQuarantine || !strings.Contains(strings.Join(decision.Blockers, "; "), "expected custom-bot[bot] <custom-bot[bot]@users.noreply.github.com>") {
		t.Fatalf("expected non-custom commit author to be rejected, got %#v", decision)
	}

	missingIdentityConfig := config
	missingIdentityConfig.GitHubAppSlug = ""
	t.Setenv("GITHUB_APP_SLUG", "")
	decision = reconcileIssue(missingIdentityConfig, testIssue("CAG-84", "Human Review"), &validPR)
	if !decision.ShouldQuarantine || !strings.Contains(strings.Join(decision.Blockers, "; "), "no expected GitHub App PR author could be derived") {
		t.Fatalf("expected missing app identity to fail closed, got %#v", decision)
	}

	overrideConfig := missingIdentityConfig
	overrideConfig.GitHubPRAuthorOverride = "custom-bot[bot]"
	overridePR := validPR
	overridePR.Commits = nil
	if decision := reconcileIssue(overrideConfig, testIssue("CAG-84", "Human Review"), &overridePR); !decision.CanMerge {
		t.Fatalf("expected explicit PR author override to pass, got %#v", decision)
	}
}
