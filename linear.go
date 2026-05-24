package main

import (
	"context"

	linearapi "github.com/weskor/pi-symphony/internal/linear"
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
