package linearstatus

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/weskor/agent-machine/internal/domain"
	"github.com/weskor/agent-machine/internal/state"
	"github.com/weskor/agent-machine/internal/workertask"
)

func TestRunLinearStatusTransitionTaskConsumesQueuedTransition(t *testing.T) {
	root := t.TempDir()
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	payload := TransitionPayload{
		IssueID:         "issue-173",
		IssueIdentifier: "CAG-173",
		IssueTitle:      "Linear transition",
		IssueURL:        "https://linear.app/acme/issue/CAG-173",
		TeamID:          "team-173",
		TargetState:     "In Progress",
	}
	if err := QueueTransitionTask(context.Background(), store, payload, 5, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}

	var updatedIssueID, updatedStateID string
	client := transitionTaskTestClient{
		workflowStates: func(ctx context.Context, teamID string) ([]domain.WorkflowState, error) {
			if teamID != "team-173" {
				t.Fatalf("workflow states team=%q; want team-173", teamID)
			}
			return []domain.WorkflowState{{ID: "running-id", Name: "In Progress"}}, nil
		},
		updateIssueState: func(ctx context.Context, issueID, stateID string) error {
			updatedIssueID = issueID
			updatedStateID = stateID
			return nil
		},
	}

	didWork, err := RunTransitionTask(client, root, store, nil)
	if err != nil {
		t.Fatalf("runLinearStatusTransitionTask() error = %v", err)
	}
	if !didWork {
		t.Fatal("runLinearStatusTransitionTask() didWork=false; want queued transition consumed")
	}
	if updatedIssueID != "issue-173" || updatedStateID != "running-id" {
		t.Fatalf("transition issue=%q state=%q; want issue-173/running-id", updatedIssueID, updatedStateID)
	}
	tasks, err := store.WorkerTasks(context.Background(), workertask.RoleLinearStatus)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Status != "completed" || tasks[0].TaskKey != "linear-status:CAG-173:transition:in-progress" {
		t.Fatalf("tasks = %+v; want completed transition task", tasks)
	}
}

func TestRunLinearStatusTransitionTaskFailsMalformedTransitionClosed(t *testing.T) {
	root := t.TempDir()
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	task := state.WorkerTask{
		TaskKey:     "linear-status:CAG-174:transition:missing-team",
		Role:        workertask.RoleLinearStatus,
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
	client := transitionTaskTestClient{
		workflowStates: func(context.Context, string) ([]domain.WorkflowState, error) {
			t.Fatal("malformed transition should fail before Linear workflow-state lookup")
			return nil, nil
		},
	}

	didWork, err := RunTransitionTask(client, root, store, nil)
	if err == nil || !strings.Contains(err.Error(), "team_id is required") {
		t.Fatalf("runLinearStatusTransitionTask() error = %v; want team_id validation failure", err)
	}
	if !didWork {
		t.Fatal("runLinearStatusTransitionTask() didWork=false; want malformed task claimed and failed")
	}
	tasks, err := store.WorkerTasks(context.Background(), workertask.RoleLinearStatus)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Status != "failed" {
		t.Fatalf("tasks = %+v; want failed malformed transition task", tasks)
	}
}

func TestRunLinearStatusTransitionTaskIgnoresUnavailableTasks(t *testing.T) {
	root := t.TempDir()
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := QueueTransitionTask(context.Background(), store, TransitionPayload{IssueID: "issue-175", IssueIdentifier: "CAG-175", TeamID: "team-175", TargetState: "In Progress"}, 5, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	client := transitionTaskTestClient{
		updateIssueState: func(context.Context, string, string) error {
			t.Fatal("future Linear status task should not be claimed")
			return nil
		},
	}

	didWork, err := RunTransitionTask(client, root, store, nil)
	if err != nil {
		t.Fatalf("runLinearStatusTransitionTask() error = %v", err)
	}
	if didWork {
		t.Fatal("runLinearStatusTransitionTask() didWork=true; want idle for future task")
	}
	tasks, err := store.WorkerTasks(context.Background(), workertask.RoleLinearStatus)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Status != "queued" {
		t.Fatalf("tasks = %+v; want future task still queued", tasks)
	}
}

func TestRunLinearStatusTransitionTaskHonorsCanceledContextBeforeClaim(t *testing.T) {
	root := t.TempDir()
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := QueueTransitionTask(context.Background(), store, TransitionPayload{IssueID: "issue-176", IssueIdentifier: "CAG-176", TeamID: "team-176", TargetState: "In Progress"}, 5, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := transitionTaskTestClient{
		updateIssueState: func(context.Context, string, string) error {
			t.Fatal("canceled Linear status task should not execute transition")
			return nil
		},
	}

	didWork, err := RunTransitionTaskContext(ctx, client, root, store, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runLinearStatusTransitionTaskContext() error = %v; want context.Canceled", err)
	}
	if didWork {
		t.Fatal("runLinearStatusTransitionTaskContext() didWork=true; want no claim after cancellation")
	}
	tasks, err := store.WorkerTasks(context.Background(), workertask.RoleLinearStatus)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Status != "queued" {
		t.Fatalf("tasks = %+v; want canceled context to leave task queued", tasks)
	}
}

func TestExecuteLinearStatusTransitionTaskHonorsCanceledContextBeforeLookup(t *testing.T) {
	root := t.TempDir()
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	task := state.WorkerTask{
		TaskKey:     "linear-status:CAG-177:transition:in-progress",
		Role:        workertask.RoleLinearStatus,
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
	client := transitionTaskTestClient{
		workflowStates: func(context.Context, string) ([]domain.WorkflowState, error) {
			t.Fatal("canceled Linear status task should not lookup workflow states")
			return nil, nil
		},
	}

	didWork, err := ExecuteTransitionTask(ctx, client, store, task, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("executeLinearStatusTransitionTask() error = %v; want context.Canceled", err)
	}
	if !didWork {
		t.Fatal("executeLinearStatusTransitionTask() didWork=false; want claimed task completed as failed")
	}
	tasks, err := store.WorkerTasks(context.Background(), workertask.RoleLinearStatus)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Status != "failed" {
		t.Fatalf("tasks = %+v; want canceled claimed task failed closed", tasks)
	}
}

type transitionTaskTestClient struct {
	workflowStates   func(context.Context, string) ([]domain.WorkflowState, error)
	updateIssueState func(context.Context, string, string) error
}

func (c transitionTaskTestClient) WorkflowStatesContext(ctx context.Context, teamID string) ([]domain.WorkflowState, error) {
	if c.workflowStates != nil {
		return c.workflowStates(ctx, teamID)
	}
	return nil, nil
}

func (c transitionTaskTestClient) UpdateIssueStateContext(ctx context.Context, issueID, stateID string) error {
	if c.updateIssueState != nil {
		return c.updateIssueState(ctx, issueID, stateID)
	}
	return nil
}

func (c transitionTaskTestClient) CreateCommentContext(context.Context, string, string) error {
	return nil
}
