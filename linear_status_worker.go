package main

import "strings"

type linearStatusWorker struct {
	client    linearClient
	candidate *issue
	states    []workflowState
}

func (w linearStatusWorker) MoveTo(stateName string) (bool, error) {
	stateName = strings.TrimSpace(stateName)
	if w.candidate == nil || stateName == "" {
		return false, nil
	}
	id := stateID(w.states, stateName)
	if id == "" {
		return false, nil
	}
	if err := updateIssueStateForLinearStatusWorker(w.client, w.candidate.ID, id); err != nil {
		return false, err
	}
	w.candidate.State.Name = stateName
	log("moved %s to %s", w.candidate.Identifier, stateName)
	return true, nil
}

func (w linearStatusWorker) Comment(body string) error {
	if w.candidate == nil || strings.TrimSpace(body) == "" {
		return nil
	}
	return createCommentForLinearStatusWorker(w.client, w.candidate.ID, body)
}

var updateIssueStateForLinearStatusWorker = func(client linearClient, issueID, stateID string) error {
	return client.updateIssueState(issueID, stateID)
}

var createCommentForLinearStatusWorker = func(client linearClient, issueID, body string) error {
	return client.createComment(issueID, body)
}

func resetLinearStatusWorkerHooks() {
	updateIssueStateForLinearStatusWorker = func(client linearClient, issueID, stateID string) error {
		return client.updateIssueState(issueID, stateID)
	}
	createCommentForLinearStatusWorker = func(client linearClient, issueID, body string) error {
		return client.createComment(issueID, body)
	}
}
