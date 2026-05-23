package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weskor/pi-symphony/internal/state"
)

func TestRunReviewPendingAttemptConsumesPayloadAndQueuesHandoff(t *testing.T) {
	t.Cleanup(resetReviewWorkerHooks)
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-178")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	candidate := &issue{ID: "issue-178", Identifier: "CAG-178", Title: "Pending review", URL: "https://linear.app/acme/issue/CAG-178"}
	candidate.Team.ID = "team-178"
	prURL := "https://github.com/acme/repo/pull/178"
	input := reviewWorker{
		config:          runnerConfig{WorkspaceRoot: root, PiCommand: "pi run", ReviewCommand: "pi review"},
		candidate:       candidate,
		workspace:       workspace,
		branch:          expectedWorkspaceBranch(candidate.Identifier),
		progressStarted: time.Now().Add(-2 * time.Minute),
		startedAt:       time.Now().Add(-time.Minute),
		piUsage:         &usage{TotalTokens: 178},
		prURL:           prURL,
		scopeResult:     scopeGuardResult{Checked: true},
		validation:      []string{"go test ./..."},
		githubAuth:      "github_app_installation",
	}
	if err := writeReviewPendingState(input); err != nil {
		t.Fatal(err)
	}
	githubAppEnvFromEnvironmentForReviewWorker = func() (map[string]string, string, error) {
		return map[string]string{"GITHUB_TOKEN": "token"}, "github_app_installation", nil
	}
	t.Cleanup(func() { githubAppEnvFromEnvironmentForReviewWorker = githubAppEnvFromEnvironment })
	collectReviewEvidenceForWorker = func(config runnerConfig, gotCandidate *issue, gotWorkspace, gotPRURL string, scopeResult scopeGuardResult, validation []string) (reviewEvidence, error) {
		if gotCandidate.Identifier != candidate.Identifier || gotWorkspace != workspace || gotPRURL != prURL || len(validation) != 1 {
			t.Fatalf("unexpected review evidence input candidate=%+v workspace=%q pr=%q validation=%#v", gotCandidate, gotWorkspace, gotPRURL, validation)
		}
		return reviewEvidence{ChecksStatus: "success", ChecksSummary: "go-ci=COMPLETED/SUCCESS"}, nil
	}
	runReviewForWorker = func(command, gotWorkspace string, gotCandidate *issue, gotPRURL string, env map[string]string, timeout time.Duration, evidence *reviewEvidence) (*reviewResult, error) {
		if command != "pi review" || gotWorkspace != workspace || gotCandidate.Identifier != candidate.Identifier || gotPRURL != prURL || env["GITHUB_TOKEN"] != "token" {
			t.Fatalf("unexpected review command input command=%q workspace=%q candidate=%+v pr=%q env=%+v", command, gotWorkspace, gotCandidate, gotPRURL, env)
		}
		return &reviewResult{Status: "passed", Usage: &usage{TotalTokens: 9}}, nil
	}

	didWork, err := runReviewPendingAttempt(linearClientWithWorkflowStates(t, []workflowState{{ID: "ready-id", Name: "Ready for Agent"}}), runnerConfig{WorkspaceRoot: root, PiCommand: "pi run", ReviewCommand: "pi review"}, store)
	if err != nil {
		t.Fatalf("runReviewPendingAttempt() error = %v", err)
	}
	if !didWork {
		t.Fatal("runReviewPendingAttempt() didWork=false; want pending review consumed")
	}
	progress, err := readRunProgress(root, candidate.Identifier)
	if err != nil {
		t.Fatal(err)
	}
	if progress.Phase != runProgressPhaseHandoffPending || progress.PRURL != prURL {
		t.Fatalf("progress = %+v; want handoff_pending for reviewed PR", progress)
	}
	payload, err := readHandoffPendingPayload(root, candidate.Identifier)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Review == nil || payload.Review.Status != "passed" || payload.PRURL != prURL || payload.PiUsage.TotalTokens != 178 {
		t.Fatalf("handoff payload = %+v; want review result and original attempt facts", payload)
	}
	lease, ok, err := store.Lease(context.Background(), "run:"+candidate.Identifier)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || lease.ReleasedAt.IsZero() {
		t.Fatalf("run lease = %+v ok=%t; want released review claim lease", lease, ok)
	}
}

func TestRunReviewPendingAttemptSkipsPendingProgressWithActiveRunLock(t *testing.T) {
	t.Cleanup(resetReviewWorkerHooks)
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-179")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	candidate := &issue{ID: "issue-179", Identifier: "CAG-179", Title: "Inline review"}
	candidate.Team.ID = "team-179"
	branch := expectedWorkspaceBranch(candidate.Identifier)
	if err := writeReviewPendingState(reviewWorker{
		config:          runnerConfig{WorkspaceRoot: root, PiCommand: "pi run", ReviewCommand: "pi review"},
		candidate:       candidate,
		workspace:       workspace,
		branch:          branch,
		progressStarted: time.Now().Add(-time.Minute),
		startedAt:       time.Now().Add(-time.Minute),
		prURL:           "https://github.com/acme/repo/pull/179",
	}); err != nil {
		t.Fatal(err)
	}
	_, releaseLock, err := acquireRunLockWithState(store, workspace, candidate, branch, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer releaseLock()
	collectReviewEvidenceForWorker = func(runnerConfig, *issue, string, string, scopeGuardResult, []string) (reviewEvidence, error) {
		t.Fatal("review worker should not consume a pending review with an active run lock")
		return reviewEvidence{}, nil
	}

	didWork, err := runReviewPendingAttempt(linearClientWithWorkflowStates(t, nil), runnerConfig{WorkspaceRoot: root, PiCommand: "pi run", ReviewCommand: "pi review"}, store)
	if err != nil {
		t.Fatalf("runReviewPendingAttempt() error = %v", err)
	}
	if didWork {
		t.Fatal("runReviewPendingAttempt() didWork=true; want idle while inline run lock is active")
	}
}

func TestClaimNextReviewReadyAttemptClaimsOnlyReviewNotReadySuccess(t *testing.T) {
	root := t.TempDir()
	store := openCandidateTestStateStore(t)
	reviewCandidate := testIssue("CAG-166", "In Progress")
	freshCandidate := testIssue("CAG-167", "Ready for Agent")
	pr := pullRequestSummary{
		Number:            166,
		URL:               "https://github.com/weskor/pi-symphony/pull/166",
		BaseRefName:       "develop",
		HeadRefName:       expectedWorkspaceBranch(reviewCandidate.Identifier),
		Author:            prAuthor{Login: githubAppPRAuthorLogin},
		ReviewDecision:    "COMMENTED",
		Mergeable:         "MERGEABLE",
		MergeStateStatus:  "CLEAN",
		StatusCheckRollup: []statusCheck{{Typename: "CheckRun", Status: "COMPLETED", Conclusion: "SUCCESS"}},
	}
	writeRunRecordFixture(t, root, reviewCandidate.Identifier, fmt.Sprintf(`{"status":%q,"pr_url":%q}`, runAttemptStatusReviewNotReady, pr.URL))
	writeRunProgress(root, runProgressSnapshot{IssueIdentifier: reviewCandidate.Identifier, Phase: "review_not_ready", PRURL: pr.URL, StartedAt: time.Now().Add(-time.Minute)})
	original := openPRsByIssueForSelection
	openPRsByIssueForSelection = func(runnerConfig) (map[string]*pullRequestSummary, error) {
		return map[string]*pullRequestSummary{reviewCandidate.Identifier: &pr}, nil
	}
	t.Cleanup(func() { openPRsByIssueForSelection = original })

	client := linearClientWithCandidates(t, []issue{freshCandidate, reviewCandidate})
	config := testRunnerConfig(root)
	config.BaseBranch = "develop"
	config.PiCommand = "true"
	config.ReviewCommand = "true"
	wf := workflow{YAML: "agent:\n  max_turns: 1\n"}

	claim, didWork, err := claimNextReviewReadyAttempt(client, wf, config, store)
	if err != nil {
		t.Fatal(err)
	}
	if !didWork || claim == nil {
		t.Fatalf("claim = %#v didWork=%t; want review-ready claim", claim, didWork)
	}
	defer claim.ReleaseLock()
	if claim.Candidate.Identifier != reviewCandidate.Identifier {
		t.Fatalf("claimed %s; want %s", claim.Candidate.Identifier, reviewCandidate.Identifier)
	}
	if claim.SelectedPR == nil || claim.SelectedPR.URL != pr.URL {
		t.Fatalf("selected PR = %#v; want %s", claim.SelectedPR, pr.URL)
	}
	if !hasRunLock(filepath.Join(root, reviewCandidate.Identifier)) {
		t.Fatalf("expected review claim to hold a run lock")
	}

	tasks, err := store.WorkerTasks(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Fatalf("claim helper should not create process worker tasks directly, got %+v", tasks)
	}
}

func linearClientWithWorkflowStates(t *testing.T, states []workflowState) linearClient {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if !strings.Contains(request.Query, "workflowStates") {
			t.Fatalf("unexpected Linear query: %s", request.Query)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"workflowStates": map[string]any{"nodes": states}}})
	}))
	t.Cleanup(server.Close)
	return linearClient{apiKey: "test-key", endpoint: server.URL}
}
