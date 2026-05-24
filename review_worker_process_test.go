package main

import (
	"context"
	"encoding/json"
	"errors"
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
		stateStore:      store,
		candidate:       candidate,
		workspace:       workspace,
		branch:          expectedWorkspaceBranch(candidate.Identifier),
		progressStarted: time.Now().Add(-2 * time.Minute),
		startedAt:       time.Now().Add(-time.Minute),
		runtimeUsage:    &usage{TotalTokens: 178},
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
	collectReviewEvidenceForWorker = func(ctx context.Context, config runnerConfig, gotCandidate *issue, gotWorkspace, gotPRURL string, scopeResult scopeGuardResult, validation []string) (reviewEvidence, error) {
		if gotCandidate.Identifier != candidate.Identifier || gotWorkspace != workspace || gotPRURL != prURL || len(validation) != 1 {
			t.Fatalf("unexpected review evidence input candidate=%+v workspace=%q pr=%q validation=%#v", gotCandidate, gotWorkspace, gotPRURL, validation)
		}
		return reviewEvidence{ChecksStatus: "success", ChecksSummary: "go-ci=COMPLETED/SUCCESS"}, nil
	}
	runReviewForWorker = func(ctx context.Context, provider, command, gotWorkspace string, gotCandidate *issue, gotPRURL string, env map[string]string, timeout time.Duration, evidence *reviewEvidence) (*reviewResult, error) {
		if provider != "" || command != "pi review" || gotWorkspace != workspace || gotCandidate.Identifier != candidate.Identifier || gotPRURL != prURL || env["GITHUB_TOKEN"] != "token" {
			t.Fatalf("unexpected review command input provider=%q command=%q workspace=%q candidate=%+v pr=%q env=%+v", provider, command, gotWorkspace, gotCandidate, gotPRURL, env)
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
	if payload.Review == nil || payload.Review.Status != "passed" || payload.PRURL != prURL || payload.RuntimeUsage.TotalTokens != 178 {
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

func TestWriteReviewPendingStateContextHonorsCanceledContextBeforeExports(t *testing.T) {
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
	candidate := &issue{ID: "issue-178", Identifier: "CAG-178", Title: "Pending review"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = writeReviewPendingStateContext(ctx, reviewWorker{
		config:          runnerConfig{WorkspaceRoot: root, PiCommand: "pi run", ReviewCommand: "pi review"},
		stateStore:      store,
		candidate:       candidate,
		workspace:       workspace,
		branch:          expectedWorkspaceBranch(candidate.Identifier),
		progressStarted: time.Now().Add(-2 * time.Minute),
		startedAt:       time.Now().Add(-time.Minute),
		prURL:           "https://github.com/acme/repo/pull/178",
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("writeReviewPendingStateContext() error = %v, want canceled", err)
	}
	path, err := reviewPendingPayloadPath(root, candidate.Identifier)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("review pending payload stat err=%v, want missing", err)
	}
	refs, err := store.PendingWorkerPayloadRefs(context.Background(), reviewWorkerRole, runProgressPhaseReviewPending)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 0 {
		t.Fatalf("review pending refs = %+v, want none", refs)
	}
}

func TestClaimNextReviewPendingAttemptUsesSQLitePayloadRefWithoutProgressSnapshot(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-184")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	candidate := &issue{ID: "issue-184", Identifier: "CAG-184", Title: "State pending review"}
	candidate.Team.ID = "team-184"
	payload := reviewPendingPayloadFromWorker(reviewWorker{
		config:          runnerConfig{WorkspaceRoot: root, PiCommand: "pi run", ReviewCommand: "pi review"},
		candidate:       candidate,
		workspace:       workspace,
		branch:          expectedWorkspaceBranch(candidate.Identifier),
		progressStarted: time.Now().Add(-time.Minute),
		startedAt:       time.Now().Add(-time.Minute),
		prURL:           "https://github.com/acme/repo/pull/184",
	})
	if err := writeReviewPendingPayload(root, payload); err != nil {
		t.Fatal(err)
	}
	payloadPath, err := reviewPendingPayloadPath(root, candidate.Identifier)
	if err != nil {
		t.Fatal(err)
	}
	if err := recordReviewPendingPayloadRef(store, payload, payloadPath); err != nil {
		t.Fatal(err)
	}

	claim, didWork, err := claimNextReviewPendingAttempt(runnerConfig{WorkspaceRoot: root, PiCommand: "pi run", ReviewCommand: "pi review"}, store)
	if err != nil {
		t.Fatalf("claimNextReviewPendingAttempt() error = %v", err)
	}
	if !didWork || claim == nil {
		t.Fatalf("claim = %+v didWork=%t; want SQLite pending review claim", claim, didWork)
	}
	defer claim.ReleaseLock()
	if claim.Payload.PRURL != payload.PRURL || claim.Payload.IssueIdentifier != candidate.Identifier || claim.PayloadRef == nil {
		t.Fatalf("claim = %+v; want payload from SQLite ref", claim)
	}
	if _, err := readRunProgress(root, candidate.Identifier); err == nil {
		t.Fatal("readRunProgress() succeeded; test should not create a progress snapshot for discovery")
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
		stateStore:      store,
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
	collectReviewEvidenceForWorker = func(context.Context, runnerConfig, *issue, string, string, scopeGuardResult, []string) (reviewEvidence, error) {
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
	workspace := filepath.Join(root, reviewCandidate.Identifier)
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	upsertReviewNotReadyAttempt(t, store, reviewCandidate, workspace, pr.URL)
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
	proj := project{}

	claim, didWork, err := claimNextReviewReadyAttempt(client, proj, config, store)
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

func TestClaimNextReviewReadyAttemptUsesSQLiteReviewNotReadyWithoutProgressSnapshot(t *testing.T) {
	root := t.TempDir()
	store := openCandidateTestStateStore(t)
	reviewCandidate := testIssue("CAG-187", "In Progress")
	workspace := filepath.Join(root, reviewCandidate.Identifier)
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	pr := pullRequestSummary{
		Number:            187,
		URL:               "https://github.com/weskor/pi-symphony/pull/187",
		BaseRefName:       "develop",
		HeadRefName:       expectedWorkspaceBranch(reviewCandidate.Identifier),
		Author:            prAuthor{Login: githubAppPRAuthorLogin},
		ReviewDecision:    "COMMENTED",
		Mergeable:         "MERGEABLE",
		MergeStateStatus:  "CLEAN",
		StatusCheckRollup: []statusCheck{{Typename: "CheckRun", Status: "COMPLETED", Conclusion: "SUCCESS"}},
	}
	upsertReviewNotReadyAttempt(t, store, reviewCandidate, workspace, pr.URL)
	original := openPRsByIssueForSelection
	openPRsByIssueForSelection = func(runnerConfig) (map[string]*pullRequestSummary, error) {
		return map[string]*pullRequestSummary{reviewCandidate.Identifier: &pr}, nil
	}
	t.Cleanup(func() { openPRsByIssueForSelection = original })

	client := linearClientWithCandidates(t, []issue{reviewCandidate})
	config := testRunnerConfig(root)
	config.BaseBranch = "develop"
	config.PiCommand = "true"
	config.ReviewCommand = "true"
	if _, err := readRunProgress(root, reviewCandidate.Identifier); err == nil {
		t.Fatal("readRunProgress() succeeded before claim; test should not create a progress snapshot for resume discovery")
	}

	claim, didWork, err := claimNextReviewReadyAttempt(client, project{}, config, store)
	if err != nil {
		t.Fatal(err)
	}
	if !didWork || claim == nil {
		t.Fatalf("claim = %#v didWork=%t; want SQLite review-ready claim", claim, didWork)
	}
	defer claim.ReleaseLock()
	if claim.Candidate.Identifier != reviewCandidate.Identifier || claim.SelectedPR == nil || claim.SelectedPR.URL != pr.URL {
		t.Fatalf("claim = %#v; want review-ready resume from SQLite facts", claim)
	}
}

func TestClaimNextReviewReadyAttemptContextHonorsCanceledContext(t *testing.T) {
	root := t.TempDir()
	store := openCandidateTestStateStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	claim, didWork, err := claimNextReviewReadyAttemptContext(ctx, linearClient{}, project{}, testRunnerConfig(root), store)
	if !errors.Is(err, context.Canceled) || didWork || claim != nil {
		t.Fatalf("claimNextReviewReadyAttemptContext() = (%#v, %t, %v), want canceled no work", claim, didWork, err)
	}
}

func TestScheduleReviewReadyWorkerTasksEnqueuesSQLiteReviewResumeWithoutProgressSnapshot(t *testing.T) {
	root := t.TempDir()
	store := openCandidateTestStateStore(t)
	reviewCandidate := testIssue("CAG-188", "In Progress")
	workspace := filepath.Join(root, reviewCandidate.Identifier)
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	pr := pullRequestSummary{
		Number:            188,
		URL:               "https://github.com/weskor/pi-symphony/pull/188",
		BaseRefName:       "develop",
		HeadRefName:       expectedWorkspaceBranch(reviewCandidate.Identifier),
		Author:            prAuthor{Login: githubAppPRAuthorLogin},
		ReviewDecision:    "COMMENTED",
		Mergeable:         "MERGEABLE",
		MergeStateStatus:  "CLEAN",
		StatusCheckRollup: []statusCheck{{Typename: "CheckRun", Status: "COMPLETED", Conclusion: "SUCCESS"}},
	}
	upsertReviewNotReadyAttempt(t, store, reviewCandidate, workspace, pr.URL)
	original := openPRsByIssueForSelection
	openPRsByIssueForSelection = func(runnerConfig) (map[string]*pullRequestSummary, error) {
		return map[string]*pullRequestSummary{reviewCandidate.Identifier: &pr}, nil
	}
	t.Cleanup(func() { openPRsByIssueForSelection = original })
	client := linearClientWithCandidates(t, []issue{reviewCandidate})
	config := testRunnerConfig(root)
	config.BaseBranch = "develop"
	config.ReviewCommand = "true"
	if _, err := readRunProgress(root, reviewCandidate.Identifier); err == nil {
		t.Fatal("readRunProgress() succeeded before scheduling; test should not create progress")
	}

	didWork, err := scheduleReviewReadyWorkerTasks(context.Background(), client, config, store, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !didWork {
		t.Fatal("didWork=false; want review resume task enqueued")
	}
	tasks, err := store.WorkerTasks(context.Background(), reviewWorkerRole)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].TaskKey != reviewReadyWorkerTaskKey(reviewCandidate.Identifier, 1) || tasks[0].Status != "queued" {
		t.Fatalf("review tasks = %+v; want queued review resume task", tasks)
	}
	if hasRunLock(workspace) {
		t.Fatal("scheduler should not acquire review run lock")
	}
}

func TestClaimNextQueuedReviewReadyAttemptClaimsQueuedTaskWithoutCandidateDiscovery(t *testing.T) {
	root := t.TempDir()
	store := openCandidateTestStateStore(t)
	reviewCandidate := testIssue("CAG-189", "In Progress")
	workspace := filepath.Join(root, reviewCandidate.Identifier)
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	pr := pullRequestSummary{
		Number:            189,
		URL:               "https://github.com/weskor/pi-symphony/pull/189",
		BaseRefName:       "develop",
		HeadRefName:       expectedWorkspaceBranch(reviewCandidate.Identifier),
		Author:            prAuthor{Login: githubAppPRAuthorLogin},
		ReviewDecision:    "COMMENTED",
		Mergeable:         "MERGEABLE",
		MergeStateStatus:  "CLEAN",
		StatusCheckRollup: []statusCheck{{Typename: "CheckRun", Status: "COMPLETED", Conclusion: "SUCCESS"}},
	}
	upsertReviewNotReadyAttempt(t, store, reviewCandidate, workspace, pr.URL)
	taskKey := reviewReadyWorkerTaskKey(reviewCandidate.Identifier, 1)
	if _, enqueued, err := enqueueReviewReadyWorkerTask(context.Background(), store, reviewCandidate, pr, workspace, time.Now().UTC()); err != nil || !enqueued {
		t.Fatalf("enqueueReviewReadyWorkerTask() enqueued=%v err=%v", enqueued, err)
	}
	original := openPRsByIssueForSelection
	openPRsByIssueForSelection = func(runnerConfig) (map[string]*pullRequestSummary, error) {
		return map[string]*pullRequestSummary{reviewCandidate.Identifier: &pr}, nil
	}
	t.Cleanup(func() { openPRsByIssueForSelection = original })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if _, ok := request.Variables["projectSlug"]; ok {
			t.Fatal("queued review task should not rediscover candidates")
		}
		if request.Variables["id"] != reviewCandidate.Identifier {
			t.Fatalf("issue lookup id = %#v, want %s", request.Variables["id"], reviewCandidate.Identifier)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"issue": reviewCandidate}})
	}))
	t.Cleanup(server.Close)
	config := testRunnerConfig(root)
	config.BaseBranch = "develop"
	config.PiCommand = "true"
	config.ReviewCommand = "true"

	claim, didWork, err := claimNextQueuedReviewReadyAttempt(linearClient{apiKey: "test-key", endpoint: server.URL}, project{}, config, store)
	if err != nil {
		t.Fatal(err)
	}
	if !didWork || claim == nil || claim.Candidate.Identifier != reviewCandidate.Identifier {
		t.Fatalf("claim = %#v didWork=%t; want queued review task claim", claim, didWork)
	}
	defer claim.ReleaseLock()
	if claim.ReviewWorkerTaskKey != taskKey {
		t.Fatalf("review task key = %q, want %q", claim.ReviewWorkerTaskKey, taskKey)
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

func upsertReviewNotReadyAttempt(t *testing.T, store *state.Store, candidate issue, workspace, prURL string) {
	t.Helper()
	if err := store.UpsertAttemptResult(context.Background(), state.AttemptResult{
		IssueKey:      candidate.Identifier,
		IssueID:       candidate.ID,
		Attempt:       1,
		WorkspacePath: workspace,
		BranchName:    expectedWorkspaceBranch(candidate.Identifier),
		BaseBranch:    "develop",
		Status:        runAttemptStatusReviewNotReady,
		Repository:    "weskor/pi-symphony",
		PRURL:         prURL,
		UpdatedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatalf("UpsertAttemptResult(review_not_ready) error = %v", err)
	}
}
