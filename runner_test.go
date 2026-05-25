package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	sh "github.com/weskor/agent-machine/internal/shell"
	"github.com/weskor/agent-machine/internal/state"
)

func init() {
	openPRsByIssueForSelection = func(runnerConfig) (map[string]*pullRequestSummary, error) {
		return map[string]*pullRequestSummary{}, nil
	}
}

func TestHasUnresolvedReviewFailure(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-1")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	record := `{"status":"review_failed","pr_url":"https://github.com/weskor/agent-machine/pull/1"}`
	if err := os.WriteFile(filepath.Join(workspace, ".am-run.json"), []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
	if !hasUnresolvedReviewFailure(root, "CAG-1") {
		t.Fatal("expected unresolved review failure")
	}
}

func TestHasUnresolvedReviewFailureIgnoresSuccessfulRuns(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-2")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	record := `{"status":"success","pr_url":"https://github.com/weskor/agent-machine/pull/2"}`
	if err := os.WriteFile(filepath.Join(workspace, ".am-run.json"), []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
	if hasUnresolvedReviewFailure(root, "CAG-2") {
		t.Fatal("did not expect successful run to count as unresolved review failure")
	}
}

func TestNextRunnableCandidatePrefersReadyCandidate(t *testing.T) {
	root := t.TempDir()
	client := linearClientWithCandidates(t, []issue{
		testIssue("CAG-1", "In Progress"),
		testIssue("CAG-2", "Ready for Agent"),
	})

	candidate, _, err := nextRunnableCandidate(client, testRunnerConfig(root), nil)
	if err != nil {
		t.Fatal(err)
	}
	if candidate == nil || candidate.Identifier != "CAG-2" {
		t.Fatalf("expected ready candidate CAG-2, got %#v", candidate)
	}
}

func TestNextRunnableCandidateRecordsSelectedAndSkippedEvents(t *testing.T) {
	root := t.TempDir()
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.UpsertAttemptResult(context.Background(), state.AttemptResult{
		IssueKey:             "CAG-1",
		Attempt:              1,
		BranchName:           expectedWorkspaceBranch("CAG-1"),
		Status:               runAttemptStatusReviewFailed,
		ReviewStatus:         "failed",
		ReviewClassification: reviewClassificationBehaviorSpecBlocker,
		UpdatedAt:            time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	client := linearClientWithCandidates(t, []issue{
		testIssue("CAG-1", "Ready for Agent"),
		testIssue("CAG-2", "Ready for Agent"),
	})

	candidate, _, err := nextRunnableCandidate(client, testRunnerConfig(root), store)
	if err != nil {
		t.Fatal(err)
	}
	if candidate == nil || candidate.Identifier != "CAG-2" {
		t.Fatalf("expected ready candidate CAG-2, got %#v", candidate)
	}
	events, err := store.Events(context.Background(), state.EventFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := candidateEventTypes(events); !reflect.DeepEqual(got, []string{"CAG-1:" + state.EventCandidateSkipped, "CAG-2:" + state.EventCandidateSelected}) {
		t.Fatalf("candidate events = %#v; all=%+v", got, events)
	}
}

func TestNextRunnableCandidateDoesNotRecordSkipForFallbackSelection(t *testing.T) {
	root := t.TempDir()
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	client := linearClientWithCandidates(t, []issue{testIssue("CAG-1", "In Progress")})

	candidate, _, err := nextRunnableCandidate(client, testRunnerConfig(root), store)
	if err != nil {
		t.Fatal(err)
	}
	if candidate == nil || candidate.Identifier != "CAG-1" {
		t.Fatalf("expected fallback candidate CAG-1, got %#v", candidate)
	}
	events, err := store.Events(context.Background(), state.EventFilter{IssueKey: "CAG-1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := candidateEventTypes(events); !reflect.DeepEqual(got, []string{"CAG-1:" + state.EventCandidateSelected}) {
		t.Fatalf("candidate events = %#v; want selected without prior skipped; all=%+v", got, events)
	}
}

func TestNextRunnableCandidateSkipsUnresolvedReviewFailures(t *testing.T) {
	root := t.TempDir()
	writeRunRecordFixture(t, root, "CAG-1", `{"status":"review_failed","pr_url":"https://github.com/weskor/agent-machine/pull/1"}`)
	client := linearClientWithCandidates(t, []issue{
		testIssue("CAG-1", "Ready for Agent"),
		testIssue("CAG-2", "In Progress"),
	})

	candidate, _, err := nextRunnableCandidate(client, testRunnerConfig(root), nil)
	if err != nil {
		t.Fatal(err)
	}
	if candidate == nil || candidate.Identifier != "CAG-2" {
		t.Fatalf("expected non-blocked candidate CAG-2, got %#v", candidate)
	}
}

func TestNextRunnableCandidateOrdersBySafetyPriorityAndAge(t *testing.T) {
	root := t.TempDir()
	feature := testIssue("CAG-1", "Ready for Agent")
	feature.Priority = 1
	feature.CreatedAt = "2026-01-01T00:00:00Z"
	harness := testIssue("CAG-2", "Ready for Agent")
	harness.Priority = 2
	harness.CreatedAt = "2026-02-01T00:00:00Z"
	addLabels(&harness, "harness")
	client := linearClientWithCandidates(t, []issue{feature, harness})

	candidate, _, err := nextRunnableCandidate(client, testRunnerConfig(root), nil)
	if err != nil {
		t.Fatal(err)
	}
	if candidate == nil || candidate.Identifier != "CAG-2" {
		t.Fatalf("expected harness candidate CAG-2, got %#v", candidate)
	}
}

func TestNextRunnableCandidateOrdersPriorityBeforeAge(t *testing.T) {
	root := t.TempDir()
	older := testIssue("CAG-1", "Ready for Agent")
	older.Priority = 3
	older.CreatedAt = "2026-01-01T00:00:00Z"
	newerHighPriority := testIssue("CAG-2", "Ready for Agent")
	newerHighPriority.Priority = 1
	newerHighPriority.CreatedAt = "2026-02-01T00:00:00Z"
	client := linearClientWithCandidates(t, []issue{older, newerHighPriority})

	candidate, _, err := nextRunnableCandidate(client, testRunnerConfig(root), nil)
	if err != nil {
		t.Fatal(err)
	}
	if candidate == nil || candidate.Identifier != "CAG-2" {
		t.Fatalf("expected priority candidate CAG-2, got %#v", candidate)
	}
}

func TestNextRunnableCandidateUsesAgeAsTieBreaker(t *testing.T) {
	root := t.TempDir()
	newer := testIssue("CAG-2", "Ready for Agent")
	newer.Priority = 2
	newer.CreatedAt = "2026-02-01T00:00:00Z"
	older := testIssue("CAG-1", "Ready for Agent")
	older.Priority = 2
	older.CreatedAt = "2026-01-01T00:00:00Z"
	client := linearClientWithCandidates(t, []issue{newer, older})

	candidate, _, err := nextRunnableCandidate(client, testRunnerConfig(root), nil)
	if err != nil {
		t.Fatal(err)
	}
	if candidate == nil || candidate.Identifier != "CAG-1" {
		t.Fatalf("expected older candidate CAG-1, got %#v", candidate)
	}
}

func TestNextRunnableCandidateSkipsBlockedLabel(t *testing.T) {
	root := t.TempDir()
	blocked := testIssue("CAG-1", "Ready for Agent")
	blocked.Priority = 1
	addLabels(&blocked, "blocked")
	available := testIssue("CAG-2", "Ready for Agent")
	available.Priority = 2
	client := linearClientWithCandidates(t, []issue{blocked, available})

	candidate, _, err := nextRunnableCandidate(client, testRunnerConfig(root), nil)
	if err != nil {
		t.Fatal(err)
	}
	if candidate == nil || candidate.Identifier != "CAG-2" {
		t.Fatalf("expected unblocked candidate CAG-2, got %#v", candidate)
	}
}

func TestNextRunnableCandidateReturnsNilWhenAllCandidatesHaveUnresolvedReviewFailures(t *testing.T) {
	root := t.TempDir()
	writeRunRecordFixture(t, root, "CAG-1", `{"status":"review_failed","pr_url":"https://github.com/weskor/agent-machine/pull/1"}`)
	writeRunRecordFixture(t, root, "CAG-2", `{"status":"review_failed","pr_url":"https://github.com/weskor/agent-machine/pull/2"}`)
	client := linearClientWithCandidates(t, []issue{
		testIssue("CAG-1", "Ready for Agent"),
		testIssue("CAG-2", "In Progress"),
	})

	candidate, _, err := nextRunnableCandidate(client, testRunnerConfig(root), nil)
	if err != nil {
		t.Fatal(err)
	}
	if candidate != nil {
		t.Fatalf("expected no candidate, got %#v", candidate)
	}
}

func TestNextRunnableCandidateSkipsActiveLocks(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-1")
	candidate := testIssue("CAG-1", "Ready for Agent")
	_, release, err := acquireRunLock(workspace, &candidate, "branch-a", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	client := linearClientWithCandidates(t, []issue{candidate, testIssue("CAG-2", "In Progress")})

	selected, _, err := nextRunnableCandidate(client, testRunnerConfig(root), nil)
	if err != nil {
		t.Fatal(err)
	}
	if selected == nil || selected.Identifier != "CAG-2" {
		t.Fatalf("expected unlocked candidate CAG-2, got %#v", selected)
	}
}

func TestClaimNextRunAttemptClaimsDistinctCandidatesBeforeExecution(t *testing.T) {
	root := t.TempDir()
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	client := linearClientWithCandidates(t, []issue{
		testIssue("CAG-1", "Ready for Agent"),
		testIssue("CAG-2", "Ready for Agent"),
	})
	config := testRunnerConfig(root)
	config.PiCommand = "sh"
	proj := project{Prompt: "# Test project"}

	first, didWork, err := claimNextRunAttempt(client, proj, config, store)
	if err != nil {
		t.Fatal(err)
	}
	if !didWork || first == nil || first.Candidate.Identifier != "CAG-1" {
		t.Fatalf("first claim = %#v didWork=%t, want CAG-1", first, didWork)
	}
	defer first.ReleaseLock()

	second, didWork, err := claimNextRunAttempt(client, proj, config, store)
	if err != nil {
		t.Fatal(err)
	}
	if !didWork || second == nil || second.Candidate.Identifier != "CAG-2" {
		t.Fatalf("second claim = %#v didWork=%t, want CAG-2", second, didWork)
	}
	defer second.ReleaseLock()

	if first.Candidate.Identifier == second.Candidate.Identifier {
		t.Fatalf("claims should be distinct, both selected %s", first.Candidate.Identifier)
	}
	if !hasRunLock(first.Workspace) || !hasRunLock(second.Workspace) {
		t.Fatalf("expected both claimed workspaces to hold run locks")
	}
}

func TestNextRunnableCandidateSkipsExistingSuccessfulPRArtifact(t *testing.T) {
	root := t.TempDir()
	writeRunRecordFixture(t, root, "CAG-1", `{"status":"success","pr_url":"https://github.com/weskor/agent-machine/pull/21"}`)
	client := linearClientWithCandidates(t, []issue{testIssue("CAG-1", "Ready for Agent"), testIssue("CAG-2", "In Progress")})

	selected, _, err := nextRunnableCandidate(client, testRunnerConfig(root), nil)
	if err != nil {
		t.Fatal(err)
	}
	if selected == nil || selected.Identifier != "CAG-2" {
		t.Fatalf("expected candidate without existing PR artifact, got %#v", selected)
	}
}

func TestNextRunnableCandidateAllowsReadyFeedbackRetryWithTerminalArtifact(t *testing.T) {
	root := t.TempDir()
	writeRunRecordFixture(t, root, "CAG-1", `{"status":"success","pr_url":"https://github.com/weskor/agent-machine/pull/429"}`)
	if err := os.WriteFile(filepath.Join(root, "CAG-1", ".am-feedback.md"), []byte("# PR feedback\n\nTest should be unit test."), 0o600); err != nil {
		t.Fatal(err)
	}
	client := linearClientWithCandidates(t, []issue{testIssue("CAG-1", "Ready for Agent"), testIssue("CAG-2", "In Progress")})

	selected, _, err := nextRunnableCandidate(client, testRunnerConfig(root), nil)
	if err != nil {
		t.Fatal(err)
	}
	if selected == nil || selected.Identifier != "CAG-1" {
		t.Fatalf("expected feedback retry candidate CAG-1, got %#v", selected)
	}
}

func TestNextRunnableCandidateDoesNotRetryTerminalArtifactWithoutFeedback(t *testing.T) {
	root := t.TempDir()
	writeRunRecordFixture(t, root, "CAG-1", `{"status":"success","pr_url":"https://github.com/weskor/agent-machine/pull/429"}`)
	client := linearClientWithCandidates(t, []issue{testIssue("CAG-1", "Ready for Agent"), testIssue("CAG-2", "In Progress")})

	selected, _, err := nextRunnableCandidate(client, testRunnerConfig(root), nil)
	if err != nil {
		t.Fatal(err)
	}
	if selected == nil || selected.Identifier != "CAG-2" {
		t.Fatalf("expected non-terminal-artifact candidate CAG-2, got %#v", selected)
	}
}

func TestNextRunnableCandidateRetriesFailedArtifactAfterPersistedBackoff(t *testing.T) {
	root := t.TempDir()
	writeRunRecordFixture(t, root, "CAG-99", `{"status":"failed","error":"test failure"}`)
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.UpsertRunArtifact(context.Background(), state.RunArtifactSnapshot{
		IssueKey:         "CAG-99",
		Attempt:          1,
		WorkspacePath:    filepath.Join(root, "CAG-99"),
		BranchName:       expectedWorkspaceBranch("CAG-99"),
		BaseBranch:       "main",
		Status:           "failed",
		RetryNextState:   "retry_after_backoff",
		RetryBudgetState: "available",
		RetryReason:      "test failure",
		TerminalOutcome:  "operational_failure",
		StartedAt:        time.Now().Add(-2 * time.Minute),
		UpdatedAt:        time.Now().Add(-2 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	config := testRunnerConfig(root)
	config.ConfigPath = writeRetryConfig(t, 10000)
	client := linearClientWithCandidates(t, []issue{testIssue("CAG-99", "Ready for Agent"), testIssue("CAG-100", "In Progress")})

	selected, _, err := nextRunnableCandidate(client, config, store)
	if err != nil {
		t.Fatal(err)
	}
	if selected == nil || selected.Identifier != "CAG-99" {
		t.Fatalf("expected persisted failed candidate after backoff, got %#v", selected)
	}
}

func TestNextRunnableCandidateSelectsChangesRequestedReviewFailure(t *testing.T) {
	root := t.TempDir()
	writeRunRecordFixture(t, root, "CAG-35", `{"status":"review_failed","review_status":"failed","pr_url":"https://github.com/weskor/agent-machine/pull/440"}`)
	client := linearClientWithCandidates(t, []issue{testIssue("CAG-35", "Ready for Agent"), testIssue("CAG-36", "Ready for Agent")})
	original := openPRsByIssueForSelection
	openPRsByIssueForSelection = func(runnerConfig) (map[string]*pullRequestSummary, error) {
		pr := &pullRequestSummary{Number: 440, URL: "https://github.com/weskor/agent-machine/pull/440", BaseRefName: "develop", HeadRefName: "am/CAG-35-workspace", Author: prAuthor{Login: githubAppPRAuthorLogin}, ReviewDecision: "CHANGES_REQUESTED"}
		return map[string]*pullRequestSummary{"CAG-35": pr}, nil
	}
	t.Cleanup(func() { openPRsByIssueForSelection = original })

	selected, pr, err := nextRunnableCandidate(client, testRunnerConfig(root), nil)
	if err != nil {
		t.Fatal(err)
	}
	if selected == nil || selected.Identifier != "CAG-35" || pr == nil || pr.Number != 440 {
		t.Fatalf("expected CAG-35 feedback retry with PR #440, got selected=%#v pr=%#v", selected, pr)
	}
}

func TestNextRunnableCandidateDoesNotSelectNeedsInfoIssues(t *testing.T) {
	root := t.TempDir()
	client := linearClientWithCandidates(t, []issue{
		testIssue("CAG-1", "Needs Info"),
	})
	config := testRunnerConfig(root)
	config.ActiveStates = []string{"Ready for Agent", "In Progress"}

	candidate, _, err := nextRunnableCandidate(client, config, nil)
	if err != nil {
		t.Fatal(err)
	}
	if candidate != nil {
		t.Fatalf("expected Needs Info issue to be excluded by active states, got %#v", candidate)
	}
}

func TestNextRunnableCandidateDoesNotSelectHumanReviewDoneOrCanceledIssues(t *testing.T) {
	root := t.TempDir()
	client := linearClientWithCandidates(t, []issue{
		testIssue("CAG-1", "Human Review"),
		testIssue("CAG-2", "Done"),
		testIssue("CAG-3", "Canceled"),
		testIssue("CAG-4", "Ready for Agent"),
	})

	candidate, _, err := nextRunnableCandidate(client, testRunnerConfig(root), nil)
	if err != nil {
		t.Fatal(err)
	}
	if candidate == nil || candidate.Identifier != "CAG-4" {
		t.Fatalf("expected only active candidate CAG-4, got %#v", candidate)
	}
}

func TestParseNeedsInfoExtractsNumberedQuestions(t *testing.T) {
	output := "NEEDS_INFO\n\n1. Which Linear team should own this state?\n2) Should existing issues be migrated?\n- not numbered\n"
	result := parseNeedsInfo(output)
	if !result.NeedsInfo {
		t.Fatal("expected NEEDS_INFO marker")
	}
	want := []string{"1. Which Linear team should own this state?", "2) Should existing issues be migrated?"}
	if len(result.Questions) != len(want) {
		t.Fatalf("questions = %#v, want %#v", result.Questions, want)
	}
	for i := range want {
		if result.Questions[i] != want[i] {
			t.Fatalf("questions = %#v, want %#v", result.Questions, want)
		}
	}
}

func TestParseNeedsInfoIgnoresIncidentalMentions(t *testing.T) {
	output := strings.Join([]string{
		"Implemented CAG-72 and opened PR:",
		"https://github.com/weskor/agent-machine/pull/20",
		"- no-PR/no-NEEDS_INFO path now fails explicitly",
		"- existing NEEDS_INFO behavior remains covered",
	}, "\n")
	result := parseNeedsInfo(output)
	if result.NeedsInfo {
		t.Fatalf("incidental NEEDS_INFO mention should not request info: %#v", result)
	}
}

func TestRenderNeedsInfoCommentNumbersQuestions(t *testing.T) {
	comment := renderNeedsInfoComment([]string{"1. Which state name should be used?", "2) Who can answer?"})
	if !strings.Contains(comment, "move the issue back to Ready for Agent") {
		t.Fatalf("comment missing operator instructions: %s", comment)
	}
	if !strings.Contains(comment, "1. Which state name should be used?") || !strings.Contains(comment, "2. Who can answer?") {
		t.Fatalf("comment did not renumber questions: %s", comment)
	}
}

func TestEnsureIsolatedWorkspaceSwitchesFromBaseBranch(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-31")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := sh.Run("git init -q && git checkout -q -b develop", workspace); err != nil {
		t.Fatal(err)
	}
	if err := ensureIsolatedWorkspace(root, workspace, "CAG-31"); err != nil {
		t.Fatal(err)
	}
	branch, err := currentGitBranch(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if branch != "am/CAG-31-workspace" {
		t.Fatalf("branch = %q", branch)
	}
}

func TestEnsureIsolatedWorkspaceRefusesSharedCheckout(t *testing.T) {
	parent := t.TempDir()
	if err := sh.Run("git init -q", parent); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, ".am", "workspaces")
	workspace := filepath.Join(root, "CAG-32")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensureIsolatedWorkspace(root, workspace, "CAG-32"); err == nil || !strings.Contains(err.Error(), "shared git checkout") {
		t.Fatalf("expected shared checkout refusal, got %v", err)
	}
}

func TestEnsureIsolatedWorkspaceRefusesOtherAgentMachineBranch(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-33")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := sh.Run("git init -q && git checkout -q -b am/CAG-99-workspace", workspace); err != nil {
		t.Fatal(err)
	}
	if err := ensureIsolatedWorkspace(root, workspace, "CAG-33"); err == nil || !strings.Contains(err.Error(), "unexpected Agent Machine branch") {
		t.Fatalf("expected branch refusal, got %v", err)
	}
}

func TestRunRecordCapturesWorkspaceIsolationFields(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-34")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := sh.Run("git init -q && git checkout -q -b am/CAG-34-workspace", workspace); err != nil {
		t.Fatal(err)
	}
	issue := testIssue("CAG-34", "In Progress")
	record := runRecordFor(&issue, workspace, "pi", "", nowFixture(), nowFixture(), nil, nil, "", "success", "", nil, "")
	if record.WorkspaceRoot != root || record.ExpectedBranch != "am/CAG-34-workspace" || record.Branch != record.ExpectedBranch {
		t.Fatalf("unexpected isolation fields: %#v", record)
	}
}

func TestPRHandoffBlockReasonRejectsWrongBaseAndBroadDiff(t *testing.T) {
	config := testRunnerConfig(t.TempDir())
	config.BaseBranch = "develop"
	candidate := testIssue("CAG-31", "In Progress")
	details := prHandoffDetails{BaseRefName: "main", HeadRefName: "am/CAG-31-workspace", ChangedFiles: 228, Additions: 12617}

	reason := prHandoffBlockReason(config, &candidate, details)

	for _, expected := range []string{"base branch", "main", "develop", "228 files", "12617 lines"} {
		if !strings.Contains(reason, expected) {
			t.Fatalf("reason %q missing %q", reason, expected)
		}
	}
}

func TestPRHandoffBlockReasonRejectsUnexpectedHeadBranch(t *testing.T) {
	config := testRunnerConfig(t.TempDir())
	config.BaseBranch = "develop"
	candidate := testIssue("CAG-32", "In Progress")
	details := prHandoffDetails{BaseRefName: "develop", HeadRefName: "feature/random", ChangedFiles: 3, Additions: 120}

	reason := prHandoffBlockReason(config, &candidate, details)

	if !strings.Contains(reason, "head branch") || !strings.Contains(reason, "am/CAG-32-workspace") {
		t.Fatalf("unexpected reason: %q", reason)
	}
}

func TestPRHandoffBlockReasonAllowsScopedDevelopPR(t *testing.T) {
	config := testRunnerConfig(t.TempDir())
	config.BaseBranch = "develop"
	candidate := testIssue("CAG-33", "In Progress")
	details := prHandoffDetails{BaseRefName: "develop", HeadRefName: "am/CAG-33-workspace", ChangedFiles: 6, Additions: 240}

	if reason := prHandoffBlockReason(config, &candidate, details); reason != "" {
		t.Fatalf("expected no block reason, got %q", reason)
	}
}

func nowFixture() time.Time {
	return time.Date(2026, 5, 17, 0, 0, 0, 0, time.UTC)
}

func TestRunOneMissingPiCommandFailsBeforeClaimOrWorkspaceMutation(t *testing.T) {
	root := t.TempDir()
	var updatedStates []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch {
		case strings.Contains(request.Query, "issues(first"):
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"issues": map[string]any{"nodes": []issue{testIssue("CAG-117", "Ready for Agent")}}}})
		case strings.Contains(request.Query, "issueUpdate"):
			updatedStates = append(updatedStates, request.Variables["stateId"].(string))
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"issueUpdate": map[string]any{"success": true}}})
		default:
			t.Fatalf("unexpected Linear query after preflight should have failed: %s", request.Query)
		}
	}))
	defer server.Close()

	config := testRunnerConfig(root)
	config.PiCommand = "missing-implementation-binary"
	didWork, err := runOne(linearClient{apiKey: "test-key", endpoint: server.URL}, project{Prompt: "# Test project"}, config)
	if err == nil || !strings.Contains(err.Error(), "pi_cli") || !strings.Contains(err.Error(), "missing-implementation-binary") {
		t.Fatalf("expected actionable pi_cli error, got %v", err)
	}
	if !didWork {
		t.Fatal("expected selected issue to count as work")
	}
	if len(updatedStates) != 0 {
		t.Fatalf("preflight mutated Linear state: %#v", updatedStates)
	}
	if _, err := os.Stat(filepath.Join(root, "CAG-117")); !os.IsNotExist(err) {
		t.Fatalf("workspace was created before preflight failure: %v", err)
	}
}

func TestRunOneMissingReviewCommandFailsBeforeClaimOrWorkspaceMutation(t *testing.T) {
	root := t.TempDir()
	var updatedStates []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch {
		case strings.Contains(request.Query, "issues(first"):
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"issues": map[string]any{"nodes": []issue{testIssue("CAG-118", "Ready for Agent")}}}})
		case strings.Contains(request.Query, "issueUpdate"):
			updatedStates = append(updatedStates, request.Variables["stateId"].(string))
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"issueUpdate": map[string]any{"success": true}}})
		default:
			t.Fatalf("unexpected Linear query after preflight should have failed: %s", request.Query)
		}
	}))
	defer server.Close()

	config := testRunnerConfig(root)
	config.PiCommand = "sh"
	config.ReviewCommand = "missing-review-binary"
	didWork, err := runOne(linearClient{apiKey: "test-key", endpoint: server.URL}, project{Prompt: "# Test project"}, config)
	if err == nil || !strings.Contains(err.Error(), "pi_cli") || !strings.Contains(err.Error(), "missing-review-binary") {
		t.Fatalf("expected actionable review preflight error, got %v", err)
	}
	if !didWork {
		t.Fatal("expected selected issue to count as work")
	}
	if len(updatedStates) != 0 {
		t.Fatalf("preflight mutated Linear state: %#v", updatedStates)
	}
	if _, err := os.Stat(filepath.Join(root, "CAG-118")); !os.IsNotExist(err) {
		t.Fatalf("workspace was created before preflight failure: %v", err)
	}
}

func TestRunOneMovesNeedsInfoAndCommentsWithoutPRHandoff(t *testing.T) {
	t.Setenv("GITHUB_APP_ID", "")
	t.Setenv("GITHUB_APP_INSTALLATION_ID", "")
	t.Setenv("GITHUB_APP_PRIVATE_KEY_PATH", "")
	t.Setenv("GITHUB_REPOSITORY", "weskor/agent-machine")
	root := t.TempDir()
	var updatedStates []string
	var comments []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch {
		case strings.Contains(request.Query, "issues(first"):
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"issues": map[string]any{"nodes": []issue{testIssue("CAG-9", "Ready for Agent")}}}})
		case strings.Contains(request.Query, "workflowStates"):
			states := []workflowState{{ID: "running-id", Name: "In Progress"}, {ID: "needs-id", Name: "Needs Info"}, {ID: "handoff-id", Name: "Human Review"}}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"workflowStates": map[string]any{"nodes": states}}})
		case strings.Contains(request.Query, "issueUpdate"):
			updatedStates = append(updatedStates, request.Variables["stateId"].(string))
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"issueUpdate": map[string]any{"success": true}}})
		case strings.Contains(request.Query, "commentCreate"):
			comments = append(comments, request.Variables["body"].(string))
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"commentCreate": map[string]any{"success": true}}})
		default:
			t.Fatalf("unexpected Linear query: %s", request.Query)
		}
	}))
	defer server.Close()

	client := linearClient{apiKey: "test-key", endpoint: server.URL}
	config := testRunnerConfig(root)
	config.RunningState = "In Progress"
	config.HandoffState = "Human Review"
	config.AfterCreate = "git init -q && git checkout -q -b develop"
	script := filepath.Join(root, "fake-pi")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf 'pwd=%s args=%s\\n' \"$PWD\" \"$*\" > invocation.txt\nprintf 'NEEDS_INFO\\n1. Which account type should be supported?\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	config.PiCommand = sh.Quote(script)
	proj := project{Prompt: "# Test project"}

	didWork, err := runOne(client, proj, config)
	if err != nil {
		t.Fatal(err)
	}
	if !didWork {
		t.Fatal("expected runOne to process an issue")
	}
	if !reflect.DeepEqual(updatedStates, []string{"running-id", "needs-id"}) {
		t.Fatalf("updated states = %#v", updatedStates)
	}
	if len(comments) != 1 || !strings.Contains(comments[0], "Which account type should be supported?") {
		t.Fatalf("unexpected comments: %#v", comments)
	}
	data, err := os.ReadFile(filepath.Join(root, "CAG-9", ".am-run.json"))
	if err != nil {
		t.Fatal(err)
	}
	var record runRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if record.Status != "needs_info" || record.PRURL != "" {
		t.Fatalf("unexpected run record: %#v", record)
	}
	invocation, err := os.ReadFile(filepath.Join(root, "CAG-9", "invocation.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"pwd=" + filepath.Join(root, "CAG-9"), "args=@" + filepath.Join(root, "CAG-9", ".am-prompt.md")} {
		if !strings.Contains(string(invocation), want) {
			t.Fatalf("invocation %q missing %q", invocation, want)
		}
	}
	prompt, err := os.ReadFile(filepath.Join(root, "CAG-9", ".am-prompt.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Do not create, update, push, or comment on a GitHub PR",
		"the Agent Machine runner will commit, push, create or update exactly one PR",
		"Stop after the scoped diff and validation notes.",
	} {
		if !strings.Contains(string(prompt), want) {
			t.Fatalf("prompt missing runner-owned PR handoff instruction %q:\n%s", want, prompt)
		}
	}
}

func TestRunOneCreatesRunnerOwnedPRWhenPiFinishesWithChangesAndNoPRURL(t *testing.T) {
	t.Setenv("GITHUB_APP_ID", "")
	t.Setenv("GITHUB_APP_INSTALLATION_ID", "")
	t.Setenv("GITHUB_APP_PRIVATE_KEY_PATH", "")
	t.Setenv("GITHUB_REPOSITORY", "weskor/agent-machine")
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	if err := sh.Run("git init -q --bare "+sh.Quote(remote), ""); err != nil {
		t.Fatal(err)
	}
	var updatedStates []string
	var comments []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch {
		case strings.Contains(request.Query, "issues(first"):
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"issues": map[string]any{"nodes": []issue{testIssue("CAG-119", "Ready for Agent")}}}})
		case strings.Contains(request.Query, "workflowStates"):
			states := []workflowState{{ID: "running-id", Name: "In Progress"}, {ID: "handoff-id", Name: "Human Review"}}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"workflowStates": map[string]any{"nodes": states}}})
		case strings.Contains(request.Query, "issueUpdate"):
			updatedStates = append(updatedStates, request.Variables["stateId"].(string))
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"issueUpdate": map[string]any{"success": true}}})
		case strings.Contains(request.Query, "commentCreate"):
			comments = append(comments, request.Variables["body"].(string))
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"commentCreate": map[string]any{"success": true}}})
		default:
			t.Fatalf("unexpected Linear query: %s", request.Query)
		}
	}))
	defer server.Close()

	createdPRComments := map[int]string{}
	withFakeGitHubAPI(t, fakeGitHubAPI{createdComments: createdPRComments})
	client := linearClient{apiKey: "test-key", endpoint: server.URL}
	config := testRunnerConfig(root)
	config.RunningState = "In Progress"
	config.HandoffState = "Human Review"
	config.BaseBranch = "main"
	config.AfterCreate = "git init -q && git config user.email test@example.com && git config user.name Test && git checkout -q -b main && echo base > README.md && git add README.md && git commit -qm base && git remote add origin " + sh.Quote(remote) + " && git push -q -u origin main"
	script := filepath.Join(root, "fake-pi-change")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho runner-owned > runner-owned.txt\nprintf 'validation: focused tests passed\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	config.PiCommand = sh.Quote(script)

	didWork, err := runOne(client, project{Prompt: "# Test project"}, config)
	if err != nil {
		t.Fatalf("runOne returned error: %v", err)
	}
	if !didWork {
		t.Fatal("expected runOne to process an issue")
	}
	if !reflect.DeepEqual(updatedStates, []string{"running-id", "handoff-id"}) {
		t.Fatalf("updated states = %#v", updatedStates)
	}
	if len(comments) != 1 || !strings.Contains(comments[0], "https://github.com/weskor/agent-machine/pull/900") {
		t.Fatalf("expected Linear handoff comment with runner-owned PR URL, got %#v", comments)
	}
	if _, ok := createdPRComments[900]; !ok {
		t.Fatalf("expected deterministic PR handoff comment on runner-created PR, got %#v", createdPRComments)
	}
	if err := sh.Run("git --git-dir "+sh.Quote(remote)+" rev-parse --verify refs/heads/"+sh.Quote(expectedWorkspaceBranch("CAG-119")), ""); err != nil {
		t.Fatalf("expected runner to push handoff branch: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "CAG-119", ".am-run.json"))
	if err != nil {
		t.Fatal(err)
	}
	var record runRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if record.Status != "success" || record.PRURL != "https://github.com/weskor/agent-machine/pull/900" {
		t.Fatalf("unexpected run record: %#v", record)
	}
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	assertRunRecordEvents(t, store, record, true, false, false)
	assertRunOneRuntimeEvents(t, store, record.IssueIdentifier)
}

func TestRunOneFailsClearlyWhenPiFinishesWithoutChangesOrPRURL(t *testing.T) {
	t.Setenv("GITHUB_APP_ID", "")
	t.Setenv("GITHUB_APP_INSTALLATION_ID", "")
	t.Setenv("GITHUB_APP_PRIVATE_KEY_PATH", "")
	root := t.TempDir()
	var updatedStates []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch {
		case strings.Contains(request.Query, "issues(first"):
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"issues": map[string]any{"nodes": []issue{testIssue("CAG-10", "Ready for Agent")}}}})
		case strings.Contains(request.Query, "workflowStates"):
			states := []workflowState{{ID: "running-id", Name: "In Progress"}, {ID: "needs-id", Name: "Needs Info"}, {ID: "handoff-id", Name: "Human Review"}}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"workflowStates": map[string]any{"nodes": states}}})
		case strings.Contains(request.Query, "issueUpdate"):
			updatedStates = append(updatedStates, request.Variables["stateId"].(string))
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"issueUpdate": map[string]any{"success": true}}})
		default:
			t.Fatalf("unexpected Linear query: %s", request.Query)
		}
	}))
	defer server.Close()

	client := linearClient{apiKey: "test-key", endpoint: server.URL}
	config := testRunnerConfig(root)
	config.RunningState = "In Progress"
	config.HandoffState = "Human Review"
	config.BaseBranch = "develop"
	config.AfterCreate = "git init -q && git config user.email test@example.com && git config user.name Test && git checkout -q -b develop && touch README.md && git add README.md && git commit -qm base && git update-ref refs/remotes/origin/develop HEAD"
	t.Setenv("RAW_AGENT_OUTPUT", "completed scoped diff and validation, but no handoff URL\n")
	config.PiCommand = `printf %s "$RAW_AGENT_OUTPUT"`
	proj := project{Prompt: "# Test project"}

	var didWork bool
	var err error
	stdout := captureStdout(t, func() {
		didWork, err = runOne(client, proj, config)
	})
	if err == nil || !strings.Contains(err.Error(), "no branch changes") {
		t.Fatalf("expected no branch changes error, got %v", err)
	}
	if strings.Contains(stdout, "completed scoped diff and validation") {
		t.Fatalf("primary log included raw Pi output: %q", stdout)
	}
	for _, expected := range []string{"run summary:", "issue=CAG-10", "status=failed", "outcome=operational_failure", "next_action=inspect_run_log_and_create_or_repair_pr"} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("expected %q in concise run output %q", expected, stdout)
		}
	}
	if !didWork {
		t.Fatal("expected runOne to process an issue")
	}
	if !reflect.DeepEqual(updatedStates, []string{"running-id"}) {
		t.Fatalf("updated states = %#v", updatedStates)
	}
	data, err := os.ReadFile(filepath.Join(root, "CAG-10", ".am-run.json"))
	if err != nil {
		t.Fatal(err)
	}
	var record runRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if record.Status == "success" || record.Status != "failed" || record.PRURL != "" || !strings.Contains(record.Error, "no branch changes") {
		t.Fatalf("unexpected run record: %#v", record)
	}
	evaluationData, err := os.ReadFile(filepath.Join(root, "CAG-10", ".am-evaluation.json"))
	if err != nil {
		t.Fatal(err)
	}
	var evaluation evaluationArtifact
	if err := json.Unmarshal(evaluationData, &evaluation); err != nil {
		t.Fatal(err)
	}
	if evaluation.FinalStatus == "success" || evaluation.Outcome != "operational_failure" {
		t.Fatalf("unexpected evaluation: %#v", evaluation)
	}
}

func TestRunOneBlocksOutOfScopeDiffBeforeReviewHandoff(t *testing.T) {
	t.Setenv("GITHUB_APP_ID", "")
	t.Setenv("GITHUB_APP_INSTALLATION_ID", "")
	t.Setenv("GITHUB_APP_PRIVATE_KEY_PATH", "")
	t.Setenv("GITHUB_REPOSITORY", "weskor/agent-machine")
	root := t.TempDir()
	var updatedStates []string
	var comments []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch {
		case strings.Contains(request.Query, "issues(first"):
			candidate := testIssue("CAG-34", "Ready for Agent")
			candidate.Description = "Allowed paths:\n\n* `docs/specs/*.md`\n\nOut of scope:\n\n* `state_projection.go`\n"
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"issues": map[string]any{"nodes": []issue{candidate}}}})
		case strings.Contains(request.Query, "workflowStates"):
			states := []workflowState{{ID: "ready-id", Name: "Ready for Agent"}, {ID: "running-id", Name: "In Progress"}, {ID: "handoff-id", Name: "Human Review"}}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"workflowStates": map[string]any{"nodes": states}}})
		case strings.Contains(request.Query, "issueUpdate"):
			updatedStates = append(updatedStates, request.Variables["stateId"].(string))
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"issueUpdate": map[string]any{"success": true}}})
		case strings.Contains(request.Query, "commentCreate"):
			comments = append(comments, request.Variables["body"].(string))
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"commentCreate": map[string]any{"success": true}}})
		default:
			t.Fatalf("unexpected Linear query: %s", request.Query)
		}
	}))
	defer server.Close()

	client := linearClient{apiKey: "test-key", endpoint: server.URL}
	config := testRunnerConfig(root)
	config.RunningState = "In Progress"
	config.ReadyState = "Ready for Agent"
	config.HandoffState = "Human Review"
	config.BaseBranch = "main"
	config.AfterCreate = "git init -q && git config user.email test@example.com && git config user.name Test && git checkout -q -b main && touch README.md && git add README.md && git commit -qm base && git update-ref refs/remotes/origin/main HEAD"
	config.PiCommand = "sh -c 'echo drift > state_projection.go && git add state_projection.go && git commit -qm drift && echo https://github.com/weskor/agent-machine/pull/999'"
	config.ReviewCommand = "sh -c 'echo REVIEW_PASS && exit 1'"
	proj := project{Prompt: "# Test project"}

	didWork, err := runOne(client, proj, config)
	if err != nil {
		t.Fatalf("runOne returned error: %v", err)
	}
	if !didWork {
		t.Fatal("expected runOne to process an issue")
	}
	if !reflect.DeepEqual(updatedStates, []string{"running-id", "ready-id"}) {
		t.Fatalf("updated states = %#v", updatedStates)
	}
	if len(comments) != 1 || !strings.Contains(comments[0], "Scope guard failed") || !strings.Contains(comments[0], "state_projection.go") {
		t.Fatalf("expected scope guard comment, got %#v", comments)
	}
	data, err := os.ReadFile(filepath.Join(root, "CAG-34", ".am-run.json"))
	if err != nil {
		t.Fatal(err)
	}
	var record runRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if record.Status != "review_failed" || record.ReviewStatus != "failed" || record.ReviewClassification != reviewClassificationBehaviorSpecBlocker {
		t.Fatalf("unexpected run record: %#v", record)
	}
	if record.PRURL != "https://github.com/weskor/agent-machine/pull/999" || !strings.Contains(record.ReviewFindings, "state_projection.go") {
		t.Fatalf("scope guard evidence missing from run record: %#v", record)
	}
}

func TestBehaviorContractPreflightPromptCoversGenericReplacementContracts(t *testing.T) {
	prompt := behaviorContractPreflightPrompt()

	for _, expected := range []string{
		"refactors, replacements, and rewrites",
		"CONTEXT.md",
		"LANGUAGE.md",
		"docs/adr/",
		"docs/specs/",
		"code, commands, dependencies, integrations, workflows, or state-machine logic",
		"inputs/outputs, side effects, cleanup, error handling, security/ownership assumptions, state transitions, and hidden operational contracts",
		"Behavior Contract Evidence",
		"cite relevant specs/ADRs",
		"behavior preserved",
		"behavior intentionally changed",
		"unknown behavior that needs clarification",
		"no spec changes were needed",
		"TDD or characterization tests",
		"complexity/LOC budget",
		"expected files touched",
		"what bespoke code is removed",
		"NEEDS_INFO",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("preflight prompt missing %q:\n%s", expected, prompt)
		}
	}
}

func TestRunRecordStoresBehaviorContractEvidence(t *testing.T) {
	workspace := t.TempDir()
	record := runRecordFor(&issue{Identifier: "CAG-38"}, workspace, "pi", "", nowFixture(), nowFixture(), nil, &reviewResult{Status: "failed", Findings: "REVIEW_FAIL missing parity checklist"}, "", "review_failed", "review did not pass", nil, "")

	joined := strings.Join(record.BehaviorContractEvidence, ",")
	for _, expected := range []string{"implementation_prompt_required_behavior_contract_preflight", "review_prompt_required_behavior_contract_parity_check", "review_failed_behavior_contract_or_scope_gate", "findings_recorded_for_behavior_contract_audit"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("behavior contract evidence missing %q in %#v", expected, record.BehaviorContractEvidence)
		}
	}
}

func linearClientWithCandidates(t *testing.T, candidates []issue) linearClient {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if id, ok := request.Variables["id"].(string); ok {
			var found issue
			for _, candidate := range candidates {
				if candidate.Identifier == id || candidate.ID == id {
					found = candidate
					break
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issue": found,
				},
			})
			return
		}
		if request.Variables["projectSlug"] != "CAG" {
			t.Fatalf("unexpected projectSlug: %#v", request.Variables["projectSlug"])
		}
		states, _ := request.Variables["states"].([]any)
		allowed := map[string]bool{}
		for _, state := range states {
			if name, ok := state.(string); ok {
				allowed[name] = true
			}
		}
		filtered := make([]issue, 0, len(candidates))
		for _, candidate := range candidates {
			if allowed[candidate.State.Name] {
				filtered = append(filtered, candidate)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issues": map[string]any{"nodes": filtered},
			},
		})
	}))
	t.Cleanup(server.Close)
	return linearClient{apiKey: "test-key", endpoint: server.URL}
}

func testRunnerConfig(workspaceRoot string) runnerConfig {
	return runnerConfig{
		ProjectSlug:    "CAG",
		WorkspaceRoot:  workspaceRoot,
		ReadyState:     "Ready for Agent",
		NeedsInfoState: "Needs Info",
		ActiveStates:   []string{"Ready for Agent", "In Progress"},
		GitHubAppSlug:  "agent-machine-bot",
	}
}

func testIssue(identifier, state string) issue {
	var out issue
	out.ID = identifier + "-id"
	out.Identifier = identifier
	out.Title = identifier + " title"
	out.State.Name = state
	return out
}

func addLabels(candidate *issue, names ...string) {
	for _, name := range names {
		candidate.Labels.Nodes = append(candidate.Labels.Nodes, struct {
			Name string `json:"name"`
		}{Name: name})
	}
}

func writeRunRecordFixture(t *testing.T, root, identifier, record string) {
	t.Helper()
	workspace := filepath.Join(root, identifier)
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".am-run.json"), []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
}

func candidateEventTypes(events []state.Event) []string {
	out := make([]string, 0, len(events))
	for _, event := range events {
		if event.Type != state.EventCandidateSkipped && event.Type != state.EventCandidateSelected {
			continue
		}
		out = append(out, event.IssueKey+":"+event.Type)
	}
	return out
}

func assertRunOneRuntimeEvents(t *testing.T, store *state.Store, issueKey string) {
	t.Helper()
	events, err := store.Events(context.Background(), state.EventFilter{IssueKey: issueKey, Attempt: 1, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]int{}
	for _, event := range events {
		types[event.Type]++
	}
	for _, eventType := range []string{state.EventAttemptStarted, state.EventRuntimeStarted, state.EventRuntimeFinished, state.EventPRDetected, state.EventAttemptFinished} {
		if types[eventType] != 1 {
			t.Fatalf("%s events = %d, want 1; all=%v", eventType, types[eventType], types)
		}
	}
}
