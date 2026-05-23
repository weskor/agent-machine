package main

import linearapi "github.com/weskor/pi-symphony/internal/linear"

type linearClient struct {
	apiKey   string
	endpoint string
}

func (c linearClient) adapter() linearapi.Client {
	return linearapi.NewClient(c.apiKey, c.endpoint)
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
	return c.adapter().Candidates(projectSlug, states)
}

func (c linearClient) issueIdentifiersByState(projectSlug, state string) (map[string]bool, error) {
	return c.adapter().IssueIdentifiersByState(projectSlug, state)
}

func (c linearClient) workflowStates(teamID string) ([]workflowState, error) {
	return c.adapter().WorkflowStates(teamID)
}

func (c linearClient) updateIssueState(issueID, stateID string) error {
	return c.adapter().UpdateIssueState(issueID, stateID)
}

func (c linearClient) createComment(issueID, body string) error {
	return c.adapter().CreateComment(issueID, body)
}

func (c linearClient) issueByIdentifier(identifier string) (*issue, error) {
	return c.adapter().IssueByIdentifier(identifier)
}
