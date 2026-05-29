package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const terminalPRReasonAlreadyMerged = "pull_request_already_merged"

var errTerminalPullRequest = errors.New("terminal pull request")

type terminalPullRequestFacts struct {
	PRURL  string
	State  string
	Reason string
}

type terminalPullRequestError struct {
	Facts terminalPullRequestFacts
}

func (e terminalPullRequestError) Error() string {
	reason := strings.TrimSpace(e.Facts.Reason)
	if reason == "" {
		reason = terminalPRReasonAlreadyMerged
	}
	if strings.TrimSpace(e.Facts.PRURL) == "" {
		return reason
	}
	return fmt.Sprintf("%s (state=%s): %s", reason, emptyAsUnknown(e.Facts.State), e.Facts.PRURL)
}

func (e terminalPullRequestError) Is(target error) bool {
	return target == errTerminalPullRequest
}

func mergedPullRequestFacts(ctx context.Context, github githubAPI, prURL string) (terminalPullRequestFacts, bool, error) {
	prURL = strings.TrimSpace(prURL)
	if prURL == "" || github == nil {
		return terminalPullRequestFacts{}, false, nil
	}
	state, merged, err := github.PullRequestState(ctx, prURL)
	if err != nil {
		return terminalPullRequestFacts{}, false, err
	}
	if !merged {
		return terminalPullRequestFacts{}, false, nil
	}
	return terminalPullRequestFacts{PRURL: prURL, State: state, Reason: terminalPRReasonAlreadyMerged}, true, nil
}

func mergedPullRequestTerminalError(facts terminalPullRequestFacts) error {
	return terminalPullRequestError{Facts: facts}
}

func terminalPullRequestFactsFromError(err error, fallbackPRURL string) (terminalPullRequestFacts, bool) {
	var terminalErr terminalPullRequestError
	if !errors.As(err, &terminalErr) {
		return terminalPullRequestFacts{}, false
	}
	facts := terminalErr.Facts
	if strings.TrimSpace(facts.PRURL) == "" {
		facts.PRURL = strings.TrimSpace(fallbackPRURL)
	}
	if strings.TrimSpace(facts.Reason) == "" {
		facts.Reason = terminalPRReasonAlreadyMerged
	}
	return facts, true
}

func writeTerminalPullRequestProgress(config runnerConfig, candidate *issue, workspace, branch string, progressStarted time.Time, facts terminalPullRequestFacts) {
	snapshot := runProgressForIssue(candidate, workspace, "completed", progressStarted)
	snapshot.Status = runAttemptStatusSuccess
	snapshot.Outcome = facts.Reason
	snapshot.Branch = branch
	snapshot.PRURL = facts.PRURL
	snapshot.NextAction = "cleanup_workspace"
	writeRunProgress(config.WorkspaceRoot, snapshot)
}

func completeTerminalPullRequestHandoffProgress(config runnerConfig, candidate *issue, workspace, branch string, progressStarted time.Time, fallbackPRURL string, err error) bool {
	facts, ok := terminalPullRequestFactsFromError(err, fallbackPRURL)
	if !ok {
		return false
	}
	writeTerminalPullRequestProgress(config, candidate, workspace, branch, progressStarted, facts)
	identifier := ""
	if candidate != nil {
		identifier = candidate.Identifier
	}
	log("%s PR handoff stopped because PR is already merged: %s", emptyAsUnknown(identifier), facts.PRURL)
	return true
}
