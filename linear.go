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

func (c linearClient) firstCandidate(projectSlug string, states []string) (*issue, error) {
	return c.firstCandidateContext(context.Background(), projectSlug, states)
}

func (c linearClient) firstCandidateContext(ctx context.Context, projectSlug string, states []string) (*issue, error) {
	candidates, err := c.candidatesContext(ctx, projectSlug, states)
	if err != nil || len(candidates) == 0 {
		return nil, err
	}
	ordered := orderCandidates(candidates, "Ready for Agent")
	return &ordered[0], nil
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

func (c linearClient) workflowStates(teamID string) ([]workflowState, error) {
	return c.workflowStatesContext(context.Background(), teamID)
}

func (c linearClient) workflowStatesContext(ctx context.Context, teamID string) ([]workflowState, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.adapter().WorkflowStatesContext(ctx, teamID)
}

func (c linearClient) updateIssueState(issueID, stateID string) error {
	return c.updateIssueStateContext(context.Background(), issueID, stateID)
}

func (c linearClient) updateIssueStateContext(ctx context.Context, issueID, stateID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.adapter().UpdateIssueStateContext(ctx, issueID, stateID)
}

func (c linearClient) createComment(issueID, body string) error {
	return c.createCommentContext(context.Background(), issueID, body)
}

func (c linearClient) createCommentContext(ctx context.Context, issueID, body string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.adapter().CreateCommentContext(ctx, issueID, body)
}

func (c linearClient) issueByIdentifier(identifier string) (*issue, error) {
	return c.issueByIdentifierContext(context.Background(), identifier)
}

func (c linearClient) issueByIdentifierContext(ctx context.Context, identifier string) (*issue, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.adapter().IssueByIdentifierContext(ctx, identifier)
}
