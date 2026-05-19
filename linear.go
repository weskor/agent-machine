package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

type linearClient struct {
	apiKey   string
	endpoint string
}

func (c linearClient) query(query string, variables map[string]any, out any) error {
	body, _ := json.Marshal(map[string]any{"query": query, "variables": variables})
	req, err := http.NewRequest(http.MethodPost, c.endpoint, bytes.NewReader(body))
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
		return fmt.Errorf("Linear API error: %s", string(data))
	}
	return json.Unmarshal(envelope.Data, out)
}

func (c linearClient) firstCandidate(projectSlug string, states []string) (*issue, error) {
	candidates, err := c.candidates(projectSlug, states)
	if err != nil || len(candidates) == 0 {
		return nil, err
	}
	ordered := orderCandidates(candidates, "Ready for Agent")
	return &ordered[0], nil
}

func (c linearClient) candidates(projectSlug string, states []string) ([]issue, error) {
	var out struct {
		Issues struct {
			Nodes []issue `json:"nodes"`
		} `json:"issues"`
	}
	err := c.query(`query($projectSlug: String!, $states: [String!]) {
  issues(first: 10, filter: { project: { slugId: { eq: $projectSlug } }, state: { name: { in: $states } } }) {
    nodes { id identifier title url description priority createdAt team { id key name } state { name } labels { nodes { name } } }
  }
}`, map[string]any{"projectSlug": projectSlug, "states": states}, &out)
	return out.Issues.Nodes, err
}

func (c linearClient) issueIdentifiersByState(projectSlug, state string) (map[string]bool, error) {
	var out struct {
		Issues struct {
			Nodes []issue `json:"nodes"`
		} `json:"issues"`
	}
	err := c.query(`query($projectSlug: String!, $state: String!) {
  issues(first: 100, filter: { project: { slugId: { eq: $projectSlug } }, state: { name: { eq: $state } } }) {
    nodes { identifier }
  }
}`, map[string]any{"projectSlug": projectSlug, "state": state}, &out)
	if err != nil {
		return nil, err
	}
	identifiers := map[string]bool{}
	for _, issue := range out.Issues.Nodes {
		identifiers[issue.Identifier] = true
	}
	return identifiers, nil
}

func (c linearClient) workflowStates(teamID string) ([]workflowState, error) {
	var out struct {
		WorkflowStates struct {
			Nodes []workflowState `json:"nodes"`
		} `json:"workflowStates"`
	}
	err := c.query(`query($teamId: ID!) { workflowStates(first: 50, filter: { team: { id: { eq: $teamId } } }) { nodes { id name } } }`, map[string]any{"teamId": teamID}, &out)
	return out.WorkflowStates.Nodes, err
}

func (c linearClient) updateIssueState(issueID, stateID string) error {
	var out struct {
		IssueUpdate struct {
			Success bool `json:"success"`
		} `json:"issueUpdate"`
	}
	err := c.query(`mutation($id: String!, $stateId: String!) { issueUpdate(id: $id, input: { stateId: $stateId }) { success } }`, map[string]any{"id": issueID, "stateId": stateID}, &out)
	if err != nil {
		return err
	}
	if !out.IssueUpdate.Success {
		return errors.New("Linear issueUpdate returned success=false")
	}
	return nil
}

func (c linearClient) createComment(issueID, body string) error {
	var out struct {
		CommentCreate struct {
			Success bool `json:"success"`
		} `json:"commentCreate"`
	}
	err := c.query(`mutation($issueId: String!, $body: String!) { commentCreate(input: { issueId: $issueId, body: $body }) { success } }`, map[string]any{"issueId": issueID, "body": body}, &out)
	if err != nil {
		return err
	}
	if !out.CommentCreate.Success {
		return errors.New("Linear commentCreate returned success=false")
	}
	return nil
}

func (c linearClient) issueByIdentifier(identifier string) (*issue, error) {
	var out struct {
		Issue issue `json:"issue"`
	}
	err := c.query(`query($id: String!) { issue(id: $id) { id identifier title url description priority createdAt team { id key name } state { name } labels { nodes { name } } } }`, map[string]any{"id": identifier}, &out)
	if err != nil {
		return nil, err
	}
	if out.Issue.ID == "" {
		return nil, nil
	}
	return &out.Issue, nil
}
