package main

import (
	"context"
	"time"

	linearapi "github.com/weskor/agent-machine/internal/linear"
	"github.com/weskor/agent-machine/internal/linearstatus"
	"github.com/weskor/agent-machine/internal/state"
)

type linearClient struct {
	apiKey   string
	endpoint string
}

func (c linearClient) adapter() linearapi.Client {
	return linearapi.NewClient(c.apiKey, c.endpoint)
}

func (c linearClient) candidates(projectSlug string, states []string) ([]issue, error) {
	return c.candidatesContext(context.Background(), projectSlug, states)
}

func (c linearClient) candidatesContext(ctx context.Context, projectSlug string, states []string) ([]issue, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.adapter().CandidatesContext(ctx, projectSlug, states)
}

func (c linearClient) issueIdentifiersByState(projectSlug, state string) (map[string]bool, error) {
	return c.issueIdentifiersByStateContext(context.Background(), projectSlug, state)
}

func (c linearClient) issueIdentifiersByStateContext(ctx context.Context, projectSlug, state string) (map[string]bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.adapter().IssueIdentifiersByStateContext(ctx, projectSlug, state)
}

func (c linearClient) workflowStatesContext(ctx context.Context, teamID string) ([]workflowState, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.adapter().WorkflowStatesContext(ctx, teamID)
}

func (c linearClient) updateIssueStateContext(ctx context.Context, issueID, stateID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.adapter().UpdateIssueStateContext(ctx, issueID, stateID)
}

func (c linearClient) createCommentContext(ctx context.Context, issueID, body string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.adapter().CreateCommentContext(ctx, issueID, body)
}

func (c linearClient) issueByIdentifierContext(ctx context.Context, identifier string) (*issue, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.adapter().IssueByIdentifierContext(ctx, identifier)
}

type linearStatusClient struct {
	client linearClient
}

func (c linearStatusClient) UpdateIssueStateContext(ctx context.Context, issueID, stateID string) error {
	return updateIssueStateForLinearStatusWorker(ctx, c.client, issueID, stateID)
}

func (c linearStatusClient) CreateCommentContext(ctx context.Context, issueID, body string) error {
	return createCommentForLinearStatusWorker(ctx, c.client, issueID, body)
}

func (c linearStatusClient) WorkflowStatesContext(ctx context.Context, teamID string) ([]workflowState, error) {
	return workflowStatesForLinearStatusWorker(ctx, c.client, teamID)
}

func newLinearStatusWorker(client linearClient, candidate *issue, states []workflowState) linearstatus.Worker {
	return linearstatus.Worker{Client: linearStatusClient{client: client}, Candidate: candidate, States: states, Logf: log}
}

var updateIssueStateForLinearStatusWorker = func(ctx context.Context, client linearClient, issueID, stateID string) error {
	return client.updateIssueStateContext(ctx, issueID, stateID)
}

var createCommentForLinearStatusWorker = func(ctx context.Context, client linearClient, issueID, body string) error {
	return client.createCommentContext(ctx, issueID, body)
}

var workflowStatesForLinearStatusWorker = func(ctx context.Context, client linearClient, teamID string) ([]workflowState, error) {
	return client.workflowStatesContext(ctx, teamID)
}

const linearStatusTaskKindTransition = linearstatus.TaskKindTransition

type linearStatusTransitionPayload = linearstatus.TransitionPayload

func queueLinearStatusTransitionTask(ctx context.Context, store *state.Store, payload linearStatusTransitionPayload, priority int, availableAt time.Time) error {
	return linearstatus.QueueTransitionTask(ctx, store, payload, priority, availableAt)
}

func runLinearStatusTransitionTask(client linearClient, config runnerConfig, store *state.Store) (bool, error) {
	return runLinearStatusTransitionTaskContext(context.Background(), client, config, store)
}

func runLinearStatusTransitionTaskContext(ctx context.Context, client linearClient, config runnerConfig, store *state.Store) (bool, error) {
	return linearstatus.RunTransitionTaskContext(ctx, linearStatusClient{client: client}, config.WorkspaceRoot, store, log)
}

func claimNextLinearStatusTransitionTask(ctx context.Context, store *state.Store) (state.WorkerTask, bool, error) {
	return linearstatus.ClaimNextTransitionTask(ctx, store)
}

func executeLinearStatusTransitionTask(ctx context.Context, client linearClient, store *state.Store, task state.WorkerTask) (bool, error) {
	return linearstatus.ExecuteTransitionTask(ctx, linearStatusClient{client: client}, store, task, log)
}

func completeLinearStatusTask(ctx context.Context, store *state.Store, taskKey, status string, primaryErr error) error {
	return linearstatus.CompleteTransitionTask(ctx, store, taskKey, status, primaryErr)
}

func resetLinearStatusWorkerHooks() {
	updateIssueStateForLinearStatusWorker = func(ctx context.Context, client linearClient, issueID, stateID string) error {
		return client.updateIssueStateContext(ctx, issueID, stateID)
	}
	createCommentForLinearStatusWorker = func(ctx context.Context, client linearClient, issueID, body string) error {
		return client.createCommentContext(ctx, issueID, body)
	}
	workflowStatesForLinearStatusWorker = func(ctx context.Context, client linearClient, teamID string) ([]workflowState, error) {
		return client.workflowStatesContext(ctx, teamID)
	}
}
