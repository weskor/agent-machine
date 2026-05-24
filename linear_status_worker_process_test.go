package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/weskor/pi-symphony/internal/state"
)

func TestRunLinearStatusTransitionTaskConsumesQueuedTransition(t *testing.T) {
	t.Cleanup(resetLinearStatusWorkerHooks)
	root := t.TempDir()
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	payload := linearStatusTransitionPayload{
		IssueID:         "issue-173",
		IssueIdentifier: "CAG-173",
		IssueTitle:      "Linear transition",
		IssueURL:        "https://linear.app/acme/issue/CAG-173",
		TeamID:          "team-173",
		TargetState:     "In Progress",
	}
	if err := queueLinearStatusTransitionTask(context.Background(), store, payload, 5, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}

	workflowStatesForLinearStatusWorker = func(ctx context.Context, client linearClient, teamID string) ([]workflowState, error) {
		if teamID != "team-173" {
			t.Fatalf("workflow states team=%q; want team-173", teamID)
		}
		return []workflowState{{ID: "running-id", Name: "In Progress"}}, nil
	}
	var updatedIssueID, updatedStateID string
	updateIssueStateForLinearStatusWorker = func(ctx context.Context, client linearClient, issueID, stateID string) error {
		updatedIssueID = issueID
		updatedStateID = stateID
		return nil
	}

	didWork, err := runLinearStatusTransitionTask(linearClient{}, runnerConfig{WorkspaceRoot: root}, store)
	if err != nil {
		t.Fatalf("runLinearStatusTransitionTask() error = %v", err)
	}
	if !didWork {
		t.Fatal("runLinearStatusTransitionTask() didWork=false; want queued transition consumed")
	}
	if updatedIssueID != "issue-173" || updatedStateID != "running-id" {
		t.Fatalf("transition issue=%q state=%q; want issue-173/running-id", updatedIssueID, updatedStateID)
	}
	tasks, err := store.WorkerTasks(context.Background(), linearStatusWorkerRole)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Status != "completed" || tasks[0].TaskKey != "linear-status:CAG-173:transition:in-progress" {
		t.Fatalf("tasks = %+v; want completed transition task", tasks)
	}
}

func TestRunLinearStatusTransitionTaskFailsMalformedTransitionClosed(t *testing.T) {
	t.Cleanup(resetLinearStatusWorkerHooks)
	root := t.TempDir()
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	task := state.WorkerTask{
		TaskKey:     "linear-status:CAG-174:transition:missing-team",
		Role:        linearStatusWorkerRole,
		IssueKey:    "CAG-174",
		IssueID:     "issue-174",
		Status:      "queued",
		AvailableAt: time.Now().Add(-time.Minute),
		LeaseName:   "linear-status:CAG-174",
		Payload:     json.RawMessage(`{"kind":"transition","issue_id":"issue-174","issue_identifier":"CAG-174","target_state":"In Progress"}`),
	}
	if err := store.UpsertWorkerTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	workflowStatesForLinearStatusWorker = func(context.Context, linearClient, string) ([]workflowState, error) {
		t.Fatal("malformed transition should fail before Linear workflow-state lookup")
		return nil, nil
	}

	didWork, err := runLinearStatusTransitionTask(linearClient{}, runnerConfig{WorkspaceRoot: root}, store)
	if err == nil || !strings.Contains(err.Error(), "team_id is required") {
		t.Fatalf("runLinearStatusTransitionTask() error = %v; want team_id validation failure", err)
	}
	if !didWork {
		t.Fatal("runLinearStatusTransitionTask() didWork=false; want malformed task claimed and failed")
	}
	tasks, err := store.WorkerTasks(context.Background(), linearStatusWorkerRole)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Status != "failed" {
		t.Fatalf("tasks = %+v; want failed malformed transition task", tasks)
	}
}

func TestRunLinearStatusTransitionTaskIgnoresUnavailableTasks(t *testing.T) {
	t.Cleanup(resetLinearStatusWorkerHooks)
	root := t.TempDir()
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := queueLinearStatusTransitionTask(context.Background(), store, linearStatusTransitionPayload{IssueID: "issue-175", IssueIdentifier: "CAG-175", TeamID: "team-175", TargetState: "In Progress"}, 5, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	updateIssueStateForLinearStatusWorker = func(context.Context, linearClient, string, string) error {
		t.Fatal("future Linear status task should not be claimed")
		return nil
	}

	didWork, err := runLinearStatusTransitionTask(linearClient{}, runnerConfig{WorkspaceRoot: root}, store)
	if err != nil {
		t.Fatalf("runLinearStatusTransitionTask() error = %v", err)
	}
	if didWork {
		t.Fatal("runLinearStatusTransitionTask() didWork=true; want idle for future task")
	}
	tasks, err := store.WorkerTasks(context.Background(), linearStatusWorkerRole)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Status != "queued" {
		t.Fatalf("tasks = %+v; want future task still queued", tasks)
	}
}

func TestRunLinearStatusTransitionTaskHonorsCanceledContextBeforeClaim(t *testing.T) {
	t.Cleanup(resetLinearStatusWorkerHooks)
	root := t.TempDir()
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := queueLinearStatusTransitionTask(context.Background(), store, linearStatusTransitionPayload{IssueID: "issue-176", IssueIdentifier: "CAG-176", TeamID: "team-176", TargetState: "In Progress"}, 5, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	updateIssueStateForLinearStatusWorker = func(context.Context, linearClient, string, string) error {
		t.Fatal("canceled Linear status task should not execute transition")
		return nil
	}

	didWork, err := runLinearStatusTransitionTaskContext(ctx, linearClient{}, runnerConfig{WorkspaceRoot: root}, store)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runLinearStatusTransitionTaskContext() error = %v; want context.Canceled", err)
	}
	if didWork {
		t.Fatal("runLinearStatusTransitionTaskContext() didWork=true; want no claim after cancellation")
	}
	tasks, err := store.WorkerTasks(context.Background(), linearStatusWorkerRole)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Status != "queued" {
		t.Fatalf("tasks = %+v; want canceled context to leave task queued", tasks)
	}
}

func TestExecuteLinearStatusTransitionTaskHonorsCanceledContextBeforeLookup(t *testing.T) {
	t.Cleanup(resetLinearStatusWorkerHooks)
	root := t.TempDir()
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	task := state.WorkerTask{
		TaskKey:     "linear-status:CAG-177:transition:in-progress",
		Role:        linearStatusWorkerRole,
		IssueKey:    "CAG-177",
		IssueID:     "issue-177",
		Status:      "claimed",
		AvailableAt: time.Now().Add(-time.Minute),
		LeaseName:   "linear-status:CAG-177",
		Payload:     json.RawMessage(`{"kind":"transition","issue_id":"issue-177","issue_identifier":"CAG-177","team_id":"team-177","target_state":"In Progress"}`),
	}
	if err := store.UpsertWorkerTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	workflowStatesForLinearStatusWorker = func(context.Context, linearClient, string) ([]workflowState, error) {
		t.Fatal("canceled Linear status task should not lookup workflow states")
		return nil, nil
	}

	didWork, err := executeLinearStatusTransitionTask(ctx, linearClient{}, store, task)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("executeLinearStatusTransitionTask() error = %v; want context.Canceled", err)
	}
	if !didWork {
		t.Fatal("executeLinearStatusTransitionTask() didWork=false; want claimed task completed as failed")
	}
	tasks, err := store.WorkerTasks(context.Background(), linearStatusWorkerRole)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Status != "failed" {
		t.Fatalf("tasks = %+v; want canceled claimed task failed closed", tasks)
	}
}
