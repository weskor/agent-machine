package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/weskor/agent-machine/internal/state"
)

func TestClaimNextImplementationAttemptSkipsReviewReadyResume(t *testing.T) {
	root := t.TempDir()
	store := openCandidateTestStateStore(t)
	reviewCandidate := testIssue("CAG-166", "Ready for Agent")
	freshCandidate := testIssue("CAG-167", "Ready for Agent")
	pr := pullRequestSummary{
		Number:            166,
		URL:               "https://github.com/weskor/agent-machine/pull/166",
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

	client := linearClientWithCandidates(t, []issue{reviewCandidate, freshCandidate})
	config := testRunnerConfig(root)
	config.BaseBranch = "develop"
	config.PiCommand = "true"
	config.ReviewCommand = "true"
	proj := project{}

	claim, didWork, err := claimNextImplementationAttempt(client, proj, config, store)
	if err != nil {
		t.Fatal(err)
	}
	if !didWork || claim == nil {
		t.Fatalf("claim = %#v didWork=%t; want fresh implementation claim", claim, didWork)
	}
	defer claim.ReleaseLock()
	if claim.Candidate.Identifier != freshCandidate.Identifier {
		t.Fatalf("claimed %s; want fresh %s", claim.Candidate.Identifier, freshCandidate.Identifier)
	}
	if claim.SelectedPR != nil {
		t.Fatalf("selected PR = %#v; want no PR for fresh implementation", claim.SelectedPR)
	}
	if !hasRunLock(filepath.Join(root, freshCandidate.Identifier)) {
		t.Fatalf("expected implementation claim to hold a run lock")
	}
	if hasRunLock(filepath.Join(root, reviewCandidate.Identifier)) {
		t.Fatalf("review-ready candidate should remain unclaimed by implementation worker")
	}
}

func TestClaimNextImplementationAttemptClaimsIssueWorkerTaskBeforeRunLock(t *testing.T) {
	root := t.TempDir()
	store := openCandidateTestStateStore(t)
	candidate := testIssue("CAG-180", "Ready for Agent")
	client := linearClientWithCandidates(t, []issue{candidate})
	config := testRunnerConfig(root)
	config.PiCommand = "true"

	claim, didWork, err := claimNextImplementationAttempt(client, project{}, config, store)
	if err != nil {
		t.Fatal(err)
	}
	if !didWork || claim == nil {
		t.Fatalf("claim = %#v didWork=%t; want implementation claim", claim, didWork)
	}
	defer claim.ReleaseLock()
	wantTaskKey := implementationWorkerTaskKey(candidate.Identifier, 1)
	if claim.ImplementationWorkerTaskKey != wantTaskKey {
		t.Fatalf("implementation task key = %q, want %q", claim.ImplementationWorkerTaskKey, wantTaskKey)
	}
	tasks, err := store.WorkerTasks(context.Background(), implementationWorkerRole)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].TaskKey != wantTaskKey || tasks[0].Status != "claimed" || tasks[0].IssueKey != candidate.Identifier || tasks[0].Attempt != 1 {
		t.Fatalf("implementation tasks = %+v; want claimed issue-specific task", tasks)
	}
	if !hasRunLock(filepath.Join(root, candidate.Identifier)) {
		t.Fatal("expected run lock after implementation task claim")
	}
}

func TestClaimNextImplementationAttemptSkipsAlreadyClaimedIssueWorkerTask(t *testing.T) {
	root := t.TempDir()
	store := openCandidateTestStateStore(t)
	candidate := testIssue("CAG-181", "Ready for Agent")
	freshCandidate := testIssue("CAG-183", "Ready for Agent")
	taskKey := implementationWorkerTaskKey(candidate.Identifier, 1)
	if err := store.UpsertWorkerTask(context.Background(), state.WorkerTask{
		TaskKey:  taskKey,
		Role:     implementationWorkerRole,
		IssueKey: candidate.Identifier,
		IssueID:  candidate.ID,
		Attempt:  1,
		Status:   "claimed",
	}); err != nil {
		t.Fatal(err)
	}
	client := linearClientWithCandidates(t, []issue{candidate, freshCandidate})
	config := testRunnerConfig(root)
	config.PiCommand = "true"

	claim, didWork, err := claimNextImplementationAttempt(client, project{}, config, store)
	if err != nil {
		t.Fatal(err)
	}
	if !didWork || claim == nil || claim.Candidate.Identifier != freshCandidate.Identifier {
		t.Fatalf("claim = %#v didWork=%t; want fresh candidate while first task is claimed", claim, didWork)
	}
	defer claim.ReleaseLock()
	if hasRunLock(filepath.Join(root, candidate.Identifier)) {
		t.Fatal("run lock should not be acquired when implementation task is already claimed")
	}
	tasks, err := store.WorkerTasks(context.Background(), implementationWorkerRole)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatalf("implementation tasks = %+v; want claimed original and fresh task", tasks)
	}
	statusByKey := map[string]string{}
	for _, task := range tasks {
		statusByKey[task.TaskKey] = task.Status
	}
	if statusByKey[taskKey] != "claimed" || statusByKey[implementationWorkerTaskKey(freshCandidate.Identifier, 1)] != "claimed" {
		t.Fatalf("implementation task statuses = %+v; want both claimed", statusByKey)
	}
}

func TestEnqueueImplementationWorkerTaskDoesNotOverwriteReconciliationNeeded(t *testing.T) {
	ctx := context.Background()
	store := openCandidateTestStateStore(t)
	candidate := testIssue("CAG-241", "Ready for Agent")
	taskKey := implementationWorkerTaskKey(candidate.Identifier, 1)
	if err := store.UpsertWorkerTask(ctx, state.WorkerTask{TaskKey: taskKey, Role: implementationWorkerRole, IssueKey: candidate.Identifier, Attempt: 1, Status: state.WorkerTaskStatusReconciliationNeeded}); err != nil {
		t.Fatal(err)
	}

	task, enqueued, err := enqueueImplementationWorkerTask(ctx, store, &candidate, filepath.Join(t.TempDir(), candidate.Identifier), expectedWorkspaceBranch(candidate.Identifier), nowFixture())
	if err != nil {
		t.Fatal(err)
	}
	if enqueued {
		t.Fatal("enqueued = true; want reconciliation_needed task to block dispatch")
	}
	if task.Status != state.WorkerTaskStatusReconciliationNeeded {
		t.Fatalf("returned task status = %q, want reconciliation_needed", task.Status)
	}
	persisted, ok, err := store.WorkerTask(ctx, taskKey)
	if err != nil || !ok {
		t.Fatalf("WorkerTask() ok=%v err=%v", ok, err)
	}
	if persisted.Status != state.WorkerTaskStatusReconciliationNeeded {
		t.Fatalf("persisted task status = %q, want reconciliation_needed", persisted.Status)
	}
}

func TestEnqueueImplementationWorkerTaskBacksOffAfterFailure(t *testing.T) {
	ctx := context.Background()
	store := openCandidateTestStateStore(t)
	candidate := testIssue("CAG-242", "Ready for Agent")
	taskKey := implementationWorkerTaskKey(candidate.Identifier, 1)
	finishedAt := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)
	if err := store.UpsertWorkerTask(ctx, state.WorkerTask{TaskKey: taskKey, Role: implementationWorkerRole, IssueKey: candidate.Identifier, Attempt: 1, Status: state.WorkerTaskStatusFailed, AvailableAt: finishedAt, UpdatedAt: finishedAt}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertWorkerResult(ctx, state.WorkerResult{TaskKey: taskKey, Role: implementationWorkerRole, IssueKey: candidate.Identifier, Attempt: 1, Status: state.WorkerTaskStatusFailed, Reason: "worker_error", StartedAt: finishedAt.Add(-time.Minute), FinishedAt: finishedAt, UpdatedAt: finishedAt}); err != nil {
		t.Fatal(err)
	}

	task, enqueued, err := enqueueImplementationWorkerTask(ctx, store, &candidate, filepath.Join(t.TempDir(), candidate.Identifier), expectedWorkspaceBranch(candidate.Identifier), finishedAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !enqueued {
		t.Fatal("enqueued = false; want failed task requeued with backoff")
	}
	wantAvailableAt := finishedAt.Add(workerTaskRetryBackoff(implementationWorkerRole))
	if task.Status != state.WorkerTaskStatusQueued || !task.AvailableAt.Equal(wantAvailableAt) {
		t.Fatalf("task = %+v; want queued at %s", task, wantAvailableAt)
	}
}

func TestClaimNextImplementationAttemptClaimsQueuedTaskWithoutCandidateDiscovery(t *testing.T) {
	root := t.TempDir()
	store := openCandidateTestStateStore(t)
	candidate := testIssue("CAG-182", "Ready for Agent")
	taskKey := implementationWorkerTaskKey(candidate.Identifier, 1)
	if err := store.UpsertWorkerTask(context.Background(), state.WorkerTask{
		TaskKey:  taskKey,
		Role:     implementationWorkerRole,
		IssueKey: candidate.Identifier,
		IssueID:  candidate.ID,
		Attempt:  1,
		Status:   "queued",
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if _, ok := request.Variables["projectSlug"]; ok {
			t.Fatal("queued implementation task should not rediscover candidates")
		}
		if request.Variables["id"] != candidate.Identifier {
			t.Fatalf("issue lookup id = %#v, want %s", request.Variables["id"], candidate.Identifier)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"issue": candidate}})
	}))
	t.Cleanup(server.Close)
	config := testRunnerConfig(root)
	config.PiCommand = "true"

	claim, didWork, err := claimNextImplementationAttempt(linearClient{apiKey: "test-key", endpoint: server.URL}, project{}, config, store)
	if err != nil {
		t.Fatal(err)
	}
	if !didWork || claim == nil || claim.Candidate.Identifier != candidate.Identifier {
		t.Fatalf("claim = %#v didWork=%t; want queued task claim", claim, didWork)
	}
	defer claim.ReleaseLock()
	if claim.ImplementationWorkerTaskKey != taskKey {
		t.Fatalf("implementation task key = %q, want %q", claim.ImplementationWorkerTaskKey, taskKey)
	}
	tasks, err := store.WorkerTasks(context.Background(), implementationWorkerRole)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Status != "claimed" {
		t.Fatalf("tasks = %+v; want queued task claimed", tasks)
	}
}

func TestClaimNextImplementationAttemptClaimsQueuedRepairableReviewFailedTask(t *testing.T) {
	root := t.TempDir()
	store := openCandidateTestStateStore(t)
	candidate := testIssue("CAG-194", "Ready for Agent")
	prURL := "https://github.com/weskor/agent-machine/pull/194"
	pr := pullRequestSummary{Number: 194, URL: prURL, BaseRefName: "develop", HeadRefName: expectedWorkspaceBranch(candidate.Identifier), Author: prAuthor{Login: githubAppPRAuthorLogin}, ReviewDecision: "COMMENTED"}
	workspace := filepath.Join(root, candidate.Identifier)
	upsertRepairableReviewFailedAttempt(t, store, candidate, workspace, prURL)
	taskKey := implementationWorkerTaskKey(candidate.Identifier, 1)
	if err := store.UpsertWorkerTask(context.Background(), state.WorkerTask{
		TaskKey:     taskKey,
		Role:        implementationWorkerRole,
		IssueKey:    candidate.Identifier,
		IssueID:     candidate.ID,
		Attempt:     1,
		Status:      "queued",
		AvailableAt: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	original := openPRsByIssueForSelection
	openPRsByIssueForSelection = func(runnerConfig) (map[string]*pullRequestSummary, error) {
		return map[string]*pullRequestSummary{candidate.Identifier: &pr}, nil
	}
	t.Cleanup(func() { openPRsByIssueForSelection = original })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"issue": candidate}})
	}))
	t.Cleanup(server.Close)
	config := testRunnerConfig(root)
	config.BaseBranch = "develop"
	config.PiCommand = "true"

	claim, didWork, err := claimNextImplementationAttempt(linearClient{apiKey: "test-key", endpoint: server.URL}, project{}, config, store)
	if err != nil {
		t.Fatal(err)
	}
	if !didWork || claim == nil || claim.Candidate.Identifier != candidate.Identifier {
		t.Fatalf("claim = %#v didWork=%t; want repairable review-failed claim", claim, didWork)
	}
	defer claim.ReleaseLock()
	results, err := store.WorkerResults(context.Background(), implementationWorkerRole)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("worker results = %+v; queued repair should not complete as terminal_or_no_retry", results)
	}
}

func TestScheduleImplementationWorkerTasksEnqueuesDistinctCandidatesWithoutClaiming(t *testing.T) {
	root := t.TempDir()
	store := openCandidateTestStateStore(t)
	first := testIssue("CAG-184", "Ready for Agent")
	first.Priority = 10
	second := testIssue("CAG-185", "Ready for Agent")
	second.Priority = 9
	client := linearClientWithCandidates(t, []issue{first, second})
	config := testRunnerConfig(root)

	didWork, err := scheduleImplementationWorkerTasks(context.Background(), client, config, store, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !didWork {
		t.Fatal("didWork = false; want scheduler to enqueue implementation tasks")
	}
	tasks, err := store.WorkerTasks(context.Background(), implementationWorkerRole)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatalf("tasks = %+v; want two implementation tasks", tasks)
	}
	statusByIssue := map[string]string{}
	for _, task := range tasks {
		statusByIssue[task.IssueKey] = task.Status
	}
	if statusByIssue[first.Identifier] != "queued" || statusByIssue[second.Identifier] != "queued" {
		t.Fatalf("task statuses = %+v; want both queued", statusByIssue)
	}
	if hasRunLock(filepath.Join(root, first.Identifier)) || hasRunLock(filepath.Join(root, second.Identifier)) {
		t.Fatal("scheduler should not acquire run locks")
	}
}
