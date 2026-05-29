package linearstatus

import (
	"context"
	"strings"

	"github.com/weskor/agent-machine/internal/domain"
)

type Client interface {
	UpdateIssueStateContext(ctx context.Context, issueID, stateID string) error
	CreateCommentContext(ctx context.Context, issueID, body string) error
}

type Worker struct {
	Client    Client
	Candidate *domain.Issue
	States    []domain.WorkflowState
	Logf      func(format string, args ...any)
}

func (w Worker) MoveTo(stateName string) (bool, error) {
	return w.MoveToContext(context.Background(), stateName)
}

func (w Worker) MoveToContext(ctx context.Context, stateName string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	stateName = strings.TrimSpace(stateName)
	if w.Client == nil || w.Candidate == nil || stateName == "" {
		return false, nil
	}
	id := StateID(w.States, stateName)
	if id == "" {
		return false, nil
	}
	if err := w.Client.UpdateIssueStateContext(ctx, w.Candidate.ID, id); err != nil {
		return false, err
	}
	w.Candidate.State.Name = stateName
	if w.Logf != nil {
		w.Logf("moved %s to %s", w.Candidate.Identifier, stateName)
	}
	return true, nil
}

func (w Worker) Comment(body string) error {
	return w.CommentContext(context.Background(), body)
}

func (w Worker) CommentContext(ctx context.Context, body string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if w.Client == nil || w.Candidate == nil || strings.TrimSpace(body) == "" {
		return nil
	}
	return w.Client.CreateCommentContext(ctx, w.Candidate.ID, body)
}

func StateID(states []domain.WorkflowState, name string) string {
	for _, state := range states {
		if state.Name == name {
			return state.ID
		}
	}
	return ""
}
