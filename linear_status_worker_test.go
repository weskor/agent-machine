package main

import (
	"context"
	"errors"
	"testing"
)

func TestLinearStatusWorkerMovesIssueToNamedState(t *testing.T) {
	t.Cleanup(resetLinearStatusWorkerHooks)
	candidate := &issue{ID: "issue-163", Identifier: "CAG-163"}
	var updatedIssueID, updatedStateID string
	updateIssueStateForLinearStatusWorker = func(ctx context.Context, client linearClient, issueID, stateID string) error {
		updatedIssueID = issueID
		updatedStateID = stateID
		return nil
	}

	moved, err := (linearStatusWorker{
		client:    linearClient{},
		candidate: candidate,
		states:    []workflowState{{ID: "running-id", Name: "In Progress"}},
	}).MoveTo("In Progress")
	if err != nil {
		t.Fatalf("MoveTo() error = %v", err)
	}
	if !moved || updatedIssueID != candidate.ID || updatedStateID != "running-id" || candidate.State.Name != "In Progress" {
		t.Fatalf("moved=%t issue=%q state=%q candidate=%+v; want moved running state", moved, updatedIssueID, updatedStateID, candidate)
	}
}

func TestLinearStatusWorkerMissingStateIsNoop(t *testing.T) {
	t.Cleanup(resetLinearStatusWorkerHooks)
	called := false
	updateIssueStateForLinearStatusWorker = func(context.Context, linearClient, string, string) error {
		called = true
		return nil
	}

	moved, err := (linearStatusWorker{
		client:    linearClient{},
		candidate: &issue{ID: "issue-163", Identifier: "CAG-163"},
		states:    []workflowState{{ID: "ready-id", Name: "Ready for Agent"}},
	}).MoveTo("Needs Info")
	if err != nil {
		t.Fatalf("MoveTo() error = %v", err)
	}
	if moved || called {
		t.Fatalf("moved=%t called=%t; want missing-state noop", moved, called)
	}
}

func TestLinearStatusWorkerCreatesComment(t *testing.T) {
	t.Cleanup(resetLinearStatusWorkerHooks)
	candidate := &issue{ID: "issue-163", Identifier: "CAG-163"}
	var commentIssueID, commentBody string
	createCommentForLinearStatusWorker = func(ctx context.Context, client linearClient, issueID, body string) error {
		commentIssueID = issueID
		commentBody = body
		return nil
	}

	if err := (linearStatusWorker{client: linearClient{}, candidate: candidate}).Comment("runner comment"); err != nil {
		t.Fatalf("Comment() error = %v", err)
	}
	if commentIssueID != candidate.ID || commentBody != "runner comment" {
		t.Fatalf("comment issue=%q body=%q; want issue/body", commentIssueID, commentBody)
	}
}

func TestLinearStatusWorkerHonorsCanceledContextBeforeMutation(t *testing.T) {
	t.Cleanup(resetLinearStatusWorkerHooks)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	updateIssueStateForLinearStatusWorker = func(context.Context, linearClient, string, string) error {
		called = true
		return nil
	}

	moved, err := (linearStatusWorker{
		client:    linearClient{},
		candidate: &issue{ID: "issue-164", Identifier: "CAG-164"},
		states:    []workflowState{{ID: "running-id", Name: "In Progress"}},
	}).MoveToContext(ctx, "In Progress")

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("MoveToContext() error = %v; want context.Canceled", err)
	}
	if moved || called {
		t.Fatalf("moved=%t called=%t; want canceled mutation skipped", moved, called)
	}
}
