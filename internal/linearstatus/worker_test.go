package linearstatus

import (
	"context"
	"errors"
	"testing"

	"github.com/weskor/agent-machine/internal/domain"
)

type fakeClient struct {
	updatedIssueID string
	updatedStateID string
	commentIssueID string
	commentBody    string
	updateCalled   bool
}

func (f *fakeClient) UpdateIssueStateContext(_ context.Context, issueID, stateID string) error {
	f.updateCalled = true
	f.updatedIssueID = issueID
	f.updatedStateID = stateID
	return nil
}

func (f *fakeClient) CreateCommentContext(_ context.Context, issueID, body string) error {
	f.commentIssueID = issueID
	f.commentBody = body
	return nil
}

func TestWorkerMovesIssueToNamedState(t *testing.T) {
	candidate := &domain.Issue{ID: "issue-163", Identifier: "CAG-163"}
	client := &fakeClient{}

	moved, err := (Worker{
		Client:    client,
		Candidate: candidate,
		States:    []domain.WorkflowState{{ID: "running-id", Name: "In Progress"}},
	}).MoveTo("In Progress")
	if err != nil {
		t.Fatalf("MoveTo() error = %v", err)
	}
	if !moved || client.updatedIssueID != candidate.ID || client.updatedStateID != "running-id" || candidate.State.Name != "In Progress" {
		t.Fatalf("moved=%t issue=%q state=%q candidate=%+v; want moved running state", moved, client.updatedIssueID, client.updatedStateID, candidate)
	}
}

func TestWorkerMissingStateIsNoop(t *testing.T) {
	client := &fakeClient{}

	moved, err := (Worker{
		Client:    client,
		Candidate: &domain.Issue{ID: "issue-163", Identifier: "CAG-163"},
		States:    []domain.WorkflowState{{ID: "ready-id", Name: "Ready for Agent"}},
	}).MoveTo("Needs Info")
	if err != nil {
		t.Fatalf("MoveTo() error = %v", err)
	}
	if moved || client.updateCalled {
		t.Fatalf("moved=%t called=%t; want missing-state noop", moved, client.updateCalled)
	}
}

func TestWorkerCreatesComment(t *testing.T) {
	candidate := &domain.Issue{ID: "issue-163", Identifier: "CAG-163"}
	client := &fakeClient{}

	if err := (Worker{Client: client, Candidate: candidate}).Comment("runner comment"); err != nil {
		t.Fatalf("Comment() error = %v", err)
	}
	if client.commentIssueID != candidate.ID || client.commentBody != "runner comment" {
		t.Fatalf("comment issue=%q body=%q; want issue/body", client.commentIssueID, client.commentBody)
	}
}

func TestWorkerHonorsCanceledContextBeforeMutation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &fakeClient{}

	moved, err := (Worker{
		Client:    client,
		Candidate: &domain.Issue{ID: "issue-164", Identifier: "CAG-164"},
		States:    []domain.WorkflowState{{ID: "running-id", Name: "In Progress"}},
	}).MoveToContext(ctx, "In Progress")

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("MoveToContext() error = %v; want context.Canceled", err)
	}
	if moved || client.updateCalled {
		t.Fatalf("moved=%t called=%t; want canceled mutation skipped", moved, client.updateCalled)
	}
}
