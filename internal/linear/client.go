package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/weskor/agent-machine/internal/domain"
)

type Client struct {
	apiKey   string
	endpoint string
}

func NewClient(apiKey, endpoint string) Client {
	return Client{apiKey: apiKey, endpoint: endpoint}
}

func (c Client) queryContext(ctx context.Context, query string, variables map[string]any, out any) error {
	body, _ := json.Marshal(map[string]any{"query": query, "variables": variables})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", c.apiKey)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	var envelope struct {
		Data   json.RawMessage   `json:"data"`
		Errors []json.RawMessage `json:"errors"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return err
	}
	if res.StatusCode >= 300 || len(envelope.Errors) > 0 {
		return fmt.Errorf("linear API error: %s", string(data))
	}
	return json.Unmarshal(envelope.Data, out)
}

func (c Client) Candidates(projectSlug string, states []string) ([]domain.Issue, error) {
	return c.CandidatesContext(context.Background(), projectSlug, states)
}

func (c Client) CandidatesContext(ctx context.Context, projectSlug string, states []string) ([]domain.Issue, error) {
	var out struct {
		Issues struct {
			Nodes []domain.Issue `json:"nodes"`
		} `json:"issues"`
	}
	err := c.queryContext(ctx, `query($projectSlug: String!, $states: [String!]) {
  issues(first: 10, filter: { project: { slugId: { eq: $projectSlug } }, state: { name: { in: $states } } }) {
    nodes { id identifier title url description priority createdAt team { id key name } state { name } labels { nodes { name } } }
  }
}`, map[string]any{"projectSlug": projectSlug, "states": states}, &out)
	return out.Issues.Nodes, err
}

func (c Client) IssueIdentifiersByState(projectSlug, state string) (map[string]bool, error) {
	return c.IssueIdentifiersByStateContext(context.Background(), projectSlug, state)
}

func (c Client) IssueIdentifiersByStateContext(ctx context.Context, projectSlug, state string) (map[string]bool, error) {
	identifiers := map[string]bool{}
	var after any
	for {
		var out struct {
			Issues struct {
				Nodes    []domain.Issue `json:"nodes"`
				PageInfo struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
			} `json:"issues"`
		}
		err := c.queryContext(ctx, `query($projectSlug: String!, $state: String!, $after: String) {
  issues(first: 100, after: $after, filter: { project: { slugId: { eq: $projectSlug } }, state: { name: { eq: $state } } }) {
    nodes { identifier }
    pageInfo { hasNextPage endCursor }
  }
}`, map[string]any{"projectSlug": projectSlug, "state": state, "after": after}, &out)
		if err != nil {
			return nil, err
		}
		for _, issue := range out.Issues.Nodes {
			identifiers[issue.Identifier] = true
		}
		if !out.Issues.PageInfo.HasNextPage {
			break
		}
		after = out.Issues.PageInfo.EndCursor
	}
	return identifiers, nil
}

func (c Client) WorkflowStates(teamID string) ([]domain.WorkflowState, error) {
	return c.WorkflowStatesContext(context.Background(), teamID)
}

func (c Client) WorkflowStatesContext(ctx context.Context, teamID string) ([]domain.WorkflowState, error) {
	var out struct {
		WorkflowStates struct {
			Nodes []domain.WorkflowState `json:"nodes"`
		} `json:"workflowStates"`
	}
	err := c.queryContext(ctx, `query($teamId: ID!) { workflowStates(first: 50, filter: { team: { id: { eq: $teamId } } }) { nodes { id name } } }`, map[string]any{"teamId": teamID}, &out)
	return out.WorkflowStates.Nodes, err
}

func (c Client) UpdateIssueState(issueID, stateID string) error {
	return c.UpdateIssueStateContext(context.Background(), issueID, stateID)
}

func (c Client) UpdateIssueStateContext(ctx context.Context, issueID, stateID string) error {
	var out struct {
		IssueUpdate struct {
			Success bool `json:"success"`
		} `json:"issueUpdate"`
	}
	err := c.queryContext(ctx, `mutation($id: String!, $stateId: String!) { issueUpdate(id: $id, input: { stateId: $stateId }) { success } }`, map[string]any{"id": issueID, "stateId": stateID}, &out)
	if err != nil {
		return err
	}
	if !out.IssueUpdate.Success {
		return errors.New("linear issueUpdate returned success=false")
	}
	return nil
}

func (c Client) CreateComment(issueID, body string) error {
	return c.CreateCommentContext(context.Background(), issueID, body)
}

func (c Client) CreateCommentContext(ctx context.Context, issueID, body string) error {
	var out struct {
		CommentCreate struct {
			Success bool `json:"success"`
		} `json:"commentCreate"`
	}
	err := c.queryContext(ctx, `mutation($issueId: String!, $body: String!) { commentCreate(input: { issueId: $issueId, body: $body }) { success } }`, map[string]any{"issueId": issueID, "body": body}, &out)
	if err != nil {
		return err
	}
	if !out.CommentCreate.Success {
		return errors.New("linear commentCreate returned success=false")
	}
	return nil
}

func (c Client) IssueByIdentifier(identifier string) (*domain.Issue, error) {
	return c.IssueByIdentifierContext(context.Background(), identifier)
}

func (c Client) IssueByIdentifierContext(ctx context.Context, identifier string) (*domain.Issue, error) {
	var out struct {
		Issue domain.Issue `json:"issue"`
	}
	err := c.queryContext(ctx, `query($id: String!) { issue(id: $id) { id identifier title url description priority createdAt team { id key name } state { name } labels { nodes { name } } } }`, map[string]any{"id": identifier}, &out)
	if err != nil {
		return nil, err
	}
	if out.Issue.ID == "" {
		return nil, nil
	}
	return &out.Issue, nil
}
