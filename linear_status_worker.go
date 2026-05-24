package main

import (
	"context"
	"strings"
)

type linearStatusWorker struct {
	client    linearClient
	candidate *issue
	states    []workflowState
}

func (w linearStatusWorker) MoveTo(stateName string) (bool, error) {
	return w.MoveToContext(context.Background(), stateName)
}

func (w linearStatusWorker) MoveToContext(ctx context.Context, stateName string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	stateName = strings.TrimSpace(stateName)
	if w.candidate == nil || stateName == "" {
		return false, nil
	}
	id := stateID(w.states, stateName)
	if id == "" {
		return false, nil
	}
	if err := updateIssueStateForLinearStatusWorker(ctx, w.client, w.candidate.ID, id); err != nil {
		return false, err
	}
	w.candidate.State.Name = stateName
	log("moved %s to %s", w.candidate.Identifier, stateName)
	return true, nil
}

func (w linearStatusWorker) Comment(body string) error {
	return w.CommentContext(context.Background(), body)
}

func (w linearStatusWorker) CommentContext(ctx context.Context, body string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if w.candidate == nil || strings.TrimSpace(body) == "" {
		return nil
	}
	return createCommentForLinearStatusWorker(ctx, w.client, w.candidate.ID, body)
}

var updateIssueStateForLinearStatusWorker = func(ctx context.Context, client linearClient, issueID, stateID string) error {
	return client.updateIssueStateContext(ctx, issueID, stateID)
}

var createCommentForLinearStatusWorker = func(ctx context.Context, client linearClient, issueID, body string) error {
	return client.createCommentContext(ctx, issueID, body)
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
