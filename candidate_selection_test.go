package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weskor/agent-machine/internal/state"
)

func TestRetryBackoffDecisionFirstFailureWaitsThenRuns(t *testing.T) {
	store := openCandidateTestStateStore(t)
	candidate := issue{Identifier: "CAG-99", State: struct {
		Name string `json:"name"`
	}{Name: "Ready for Agent"}}
	now := time.Date(2026, 5, 21, 10, 0, 0, 0, time.UTC)
	upsertRetrySnapshot(t, store, candidate.Identifier, 1, now)

	config := runnerConfig{ConfigPath: writeRetryConfig(t, 10000), ReadyState: "Ready for Agent", NeedsInfoState: "Needs Info", DoneState: "Done"}
	decision, ok := retryBackoffDecision(context.Background(), store, candidate, config, now.Add(500*time.Millisecond))
	if !ok || decision.runnable {
		t.Fatalf("decision before backoff = %+v, %v; want blocked", decision, ok)
	}
	decision, ok = retryBackoffDecision(context.Background(), store, candidate, config, now.Add(time.Second))
	if !ok || !decision.runnable {
		t.Fatalf("decision after backoff = %+v, %v; want runnable", decision, ok)
	}
}

func TestRetryBackoffDecisionAllowsRetryableTerminalOutcomeAfterBackoff(t *testing.T) {
	store := openCandidateTestStateStore(t)
	candidate := issue{Identifier: "CAG-99", State: struct {
		Name string `json:"name"`
	}{Name: "Ready for Agent"}}
	now := time.Date(2026, 5, 21, 10, 0, 0, 0, time.UTC)
	if err := store.UpsertRunArtifact(context.Background(), state.RunArtifactSnapshot{
		IssueKey:         candidate.Identifier,
		Attempt:          1,
		BranchName:       candidate.Identifier,
		BaseBranch:       "main",
		Status:           "failed",
		RetryNextState:   "retry_after_backoff",
		RetryBudgetState: "available",
		RetryReason:      "test failure",
		TerminalOutcome:  "operational_failure",
		StartedAt:        now.Add(-time.Minute),
		UpdatedAt:        now,
	}); err != nil {
		t.Fatalf("UpsertRunArtifact() error = %v", err)
	}

	config := runnerConfig{ConfigPath: writeRetryConfig(t, 10000), ReadyState: "Ready for Agent", NeedsInfoState: "Needs Info", DoneState: "Done"}
	decision, ok := retryBackoffDecision(context.Background(), store, candidate, config, now.Add(time.Second))
	if !ok || !decision.runnable {
		t.Fatalf("decision after terminal retryable failure = %+v, %v; want runnable", decision, ok)
	}
}

func TestRetryBackoffDecisionBlocksNoRetryState(t *testing.T) {
	store := openCandidateTestStateStore(t)
	candidate := issue{Identifier: "CAG-99", State: struct {
		Name string `json:"name"`
	}{Name: "Ready for Agent"}}
	now := time.Date(2026, 5, 21, 10, 0, 0, 0, time.UTC)
	if err := store.UpsertRunArtifact(context.Background(), state.RunArtifactSnapshot{
		IssueKey:        candidate.Identifier,
		Attempt:         1,
		BranchName:      candidate.Identifier,
		BaseBranch:      "main",
		Status:          "failed",
		RetryNextState:  "no_retry",
		RetryReason:     "terminal",
		TerminalOutcome: "operational_failure",
		StartedAt:       now.Add(-time.Minute),
		UpdatedAt:       now,
	}); err != nil {
		t.Fatalf("UpsertRunArtifact() error = %v", err)
	}

	config := runnerConfig{ConfigPath: writeRetryConfig(t, 10000), ReadyState: "Ready for Agent", NeedsInfoState: "Needs Info", DoneState: "Done"}
	decision, ok := retryBackoffDecision(context.Background(), store, candidate, config, now.Add(time.Second))
	if !ok || decision.runnable {
		t.Fatalf("decision for no_retry = %+v, %v; want blocked", decision, ok)
	}
}

func TestRetryBackoffDecisionRepeatedFailurePersistsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := state.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	candidate := issue{Identifier: "CAG-99", State: struct {
		Name string `json:"name"`
	}{Name: "Ready for Agent"}}
	now := time.Date(2026, 5, 21, 10, 0, 0, 0, time.UTC)
	upsertRetrySnapshot(t, store, candidate.Identifier, 1, now.Add(-time.Minute))
	upsertRetrySnapshot(t, store, candidate.Identifier, 1, now)
	_ = store.Close()

	restarted, err := state.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("reopen state store: %v", err)
	}
	defer restarted.Close()
	config := runnerConfig{ConfigPath: writeRetryConfig(t, 10000), ReadyState: "Ready for Agent"}
	decision, ok := retryBackoffDecision(context.Background(), restarted, candidate, config, now.Add(time.Second))
	if !ok || decision.runnable {
		t.Fatalf("decision after repeated persisted failure = %+v, %v; want blocked for doubled backoff", decision, ok)
	}
}

func TestRetryBackoffDurationRespectsConfiguredMax(t *testing.T) {
	if got, want := retryBackoffDuration(10, 5*time.Second), 5*time.Second; got != want {
		t.Fatalf("retryBackoffDuration() = %v, want %v", got, want)
	}
}

func TestRetryBackoffDecisionSkipsNeedsInfoAndTerminal(t *testing.T) {
	store := openCandidateTestStateStore(t)
	now := time.Date(2026, 5, 21, 10, 0, 0, 0, time.UTC)
	upsertRetrySnapshot(t, store, "CAG-99", 1, now)
	config := runnerConfig{ConfigPath: writeRetryConfig(t, 1000), NeedsInfoState: "Needs Info", DoneState: "Done"}

	for _, stateName := range []string{"Needs Info", "Done"} {
		candidate := issue{Identifier: "CAG-99", State: struct {
			Name string `json:"name"`
		}{Name: stateName}}
		decision, ok := retryBackoffDecision(context.Background(), store, candidate, config, now.Add(2*time.Second))
		if !ok || decision.runnable {
			t.Fatalf("decision for state %q = %+v, %v; want blocked", stateName, decision, ok)
		}
	}
}

func TestReconcileCandidateForSelectionAllowsRepairableReviewFailedPR(t *testing.T) {
	root := t.TempDir()
	config := testRunnerConfig(root)
	config.BaseBranch = "develop"
	candidate := testIssue("CAG-141", "Ready for Agent")
	pr := seedRepairableReviewFailedPR(t, root, candidate.Identifier, "https://github.com/weskor/agent-machine/pull/93")

	decision := reconcileCandidateForSelection(config, candidate, &pr, nil)

	if !decision.CanRun || !decision.ShouldRetry || decision.NextAction != repairReviewFindingsNextAction || decision.ReconciliationNeeded {
		t.Fatalf("expected repairable review-failed PR to be runnable, got %#v", decision)
	}
	if strings.Contains(strings.Join(decision.Blockers, "; "), "PR exists while Linear state") {
		t.Fatalf("expected PR-exists reconciliation blocker to be cleared, got %#v", decision)
	}
}

func TestReconcileCandidateForSelectionIgnoresStaleRepairArtifact(t *testing.T) {
	root := t.TempDir()
	store := openCandidateTestStateStore(t)
	config := testRunnerConfig(root)
	config.BaseBranch = "develop"
	candidate := testIssue("CAG-141", "Ready for Agent")
	pr := seedRepairableReviewFailedPR(t, root, candidate.Identifier, "https://github.com/weskor/agent-machine/pull/93")
	if err := store.UpsertRunArtifact(context.Background(), state.RunArtifactSnapshot{
		IssueKey:        candidate.Identifier,
		Attempt:         2,
		BranchName:      expectedWorkspaceBranch(candidate.Identifier),
		BaseBranch:      "develop",
		Status:          "success",
		Repository:      "weskor/agent-machine",
		PRNumber:        93,
		PRURL:           pr.URL,
		TerminalOutcome: "handoff_ready",
		UpdatedAt:       time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("UpsertRunArtifact() error = %v", err)
	}

	decision := reconcileCandidateForSelection(config, candidate, &pr, store)

	if decision.ReconciliationNeeded || decision.NextAction == repairReviewFindingsNextAction {
		t.Fatalf("expected state-backed selection to ignore stale repair artifact, got %#v", decision)
	}
	if !strings.Contains(strings.Join(decision.Blockers, "; "), "PR exists while Linear state") {
		t.Fatalf("expected current SQLite/PR state to drive blocker, got %#v", decision)
	}
}

func TestRepairableReviewFailedPRDoesNotFallbackToArtifactWhenSQLiteFactsExist(t *testing.T) {
	root := t.TempDir()
	config := testRunnerConfig(root)
	config.BaseBranch = "develop"
	candidate := testIssue("CAG-143", "Ready for Agent")
	pr := seedRepairableReviewFailedPR(t, root, candidate.Identifier, "https://github.com/weskor/agent-machine/pull/143")
	decision := reconciliationDecision{
		NextAction: "reconcile_linear_state",
		DBFacts: &state.ReconciliationFacts{
			IssueKey: candidate.Identifier,
			Attempt:  1,
			PRURL:    pr.URL,
		},
	}

	if repairableReviewFailedPR(config, candidate, &pr, decision) {
		t.Fatal("repairableReviewFailedPR() used artifact fallback despite present SQLite facts")
	}
}

func TestReconcileCandidateForSelectionIgnoresArtifactOnlyState(t *testing.T) {
	root := t.TempDir()
	store := openCandidateTestStateStore(t)
	config := testRunnerConfig(root)
	candidate := testIssue("CAG-142", "Ready for Agent")
	writeRunRecordFixture(t, root, candidate.Identifier, `{"status":"success","pr_url":"https://github.com/weskor/agent-machine/pull/142"}`)

	decision := reconcileCandidateForSelection(config, candidate, nil, store)

	if !decision.CanRun || decision.ReconciliationNeeded || decision.NextAction != "run_agent" {
		t.Fatalf("expected state-backed selection to ignore artifact-only state, got %#v", decision)
	}
}

func TestClaimNextRunAttemptDispatchesRepairableReviewFailedPR(t *testing.T) {
	root := t.TempDir()
	store := openCandidateTestStateStore(t)
	candidate := testIssue("CAG-141", "Ready for Agent")
	prURL := "https://github.com/weskor/agent-machine/pull/93"
	pr := pullRequestSummary{Number: 93, URL: prURL, BaseRefName: "develop", HeadRefName: expectedWorkspaceBranch(candidate.Identifier), Author: prAuthor{Login: githubAppPRAuthorLogin}, ReviewDecision: "COMMENTED"}
	upsertRepairableReviewFailedAttempt(t, store, candidate, filepath.Join(root, candidate.Identifier), prURL)
	original := openPRsByIssueForSelection
	openPRsByIssueForSelection = func(runnerConfig) (map[string]*pullRequestSummary, error) {
		return map[string]*pullRequestSummary{candidate.Identifier: &pr}, nil
	}
	t.Cleanup(func() { openPRsByIssueForSelection = original })
	client := linearClientWithCandidates(t, []issue{candidate})
	config := testRunnerConfig(root)
	config.BaseBranch = "develop"
	config.PiCommand = "sh"
	proj := project{Prompt: "# Test project"}

	claim, didWork, err := claimNextRunAttempt(client, proj, config, store)
	if err != nil {
		t.Fatal(err)
	}
	if !didWork || claim == nil || claim.Candidate.Identifier != candidate.Identifier {
		t.Fatalf("claim = %#v didWork=%t, want repair claim for %s", claim, didWork, candidate.Identifier)
	}
	defer claim.ReleaseLock()
	if claim.SelectedPR == nil || claim.SelectedPR.URL != pr.URL {
		t.Fatalf("expected repair claim to reuse PR %s, got %#v", pr.URL, claim.SelectedPR)
	}
	if !hasRunLock(claim.Workspace) {
		t.Fatalf("expected repair claim to hold a run lock")
	}
}

func seedRepairableReviewFailedPR(t *testing.T, root, identifier, prURL string) pullRequestSummary {
	t.Helper()
	writeRunRecordFixture(t, root, identifier, fmt.Sprintf(`{"status":"review_failed","review_status":"failed","review_classification":"%s","review_findings":"REVIEW_FAIL behavior mismatch","pr_url":%q}`, reviewClassificationBehaviorSpecBlocker, prURL))
	workspace := filepath.Join(root, identifier)
	writeJSON(t, filepath.Join(workspace, evaluationArtifactName), evaluationArtifact{IssueIdentifier: identifier, PRURL: prURL, Outcome: runAttemptStatusReviewFailed, ReviewStatus: "failed", ReviewClassification: reviewClassificationBehaviorSpecBlocker, ShouldRetry: true, NextAction: repairReviewFindingsNextAction})
	return pullRequestSummary{Number: 93, URL: prURL, BaseRefName: "develop", HeadRefName: expectedWorkspaceBranch(identifier), Author: prAuthor{Login: githubAppPRAuthorLogin}, ReviewDecision: "COMMENTED"}
}

func upsertRepairableReviewFailedAttempt(t *testing.T, store *state.Store, candidate issue, workspace, prURL string) {
	t.Helper()
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertAttemptResult(context.Background(), state.AttemptResult{
		IssueKey:             candidate.Identifier,
		IssueID:              candidate.ID,
		Attempt:              1,
		WorkspacePath:        workspace,
		BranchName:           expectedWorkspaceBranch(candidate.Identifier),
		BaseBranch:           "develop",
		Status:               runAttemptStatusReviewFailed,
		Repository:           "weskor/agent-machine",
		PRNumber:             93,
		PRURL:                prURL,
		ReviewStatus:         "failed",
		ReviewClassification: reviewClassificationBehaviorSpecBlocker,
		UpdatedAt:            time.Now().UTC(),
	}); err != nil {
		t.Fatalf("UpsertAttemptResult(review_failed) error = %v", err)
	}
}

func writeRetryConfig(t *testing.T, maxRetryBackoffMS int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "am.yaml")
	content := `
tracker:
  project_slug: CAG
workspace:
  root: /tmp/workspaces
agent:
  max_retry_backoff_ms: ` + fmt.Sprint(maxRetryBackoffMS) + `
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write project: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "am.agent.md"), []byte("# Test project\n"), 0o600); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	return path
}

func openCandidateTestStateStore(t *testing.T) *state.Store {
	t.Helper()
	store, err := state.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func upsertRetrySnapshot(t *testing.T, store *state.Store, issueKey string, attempt int, at time.Time) {
	t.Helper()
	if err := store.UpsertRunArtifact(context.Background(), state.RunArtifactSnapshot{
		IssueKey:         issueKey,
		Attempt:          attempt,
		BranchName:       issueKey,
		BaseBranch:       "main",
		Status:           "failed",
		RetryNextState:   "retry_after_backoff",
		RetryBudgetState: "available",
		RetryReason:      "test failure",
		StartedAt:        at.Add(-time.Minute),
		UpdatedAt:        at,
	}); err != nil {
		t.Fatalf("UpsertRunArtifact() error = %v", err)
	}
}
