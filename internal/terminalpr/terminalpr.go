package terminalpr

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

const ReasonAlreadyMerged = "pull_request_already_merged"

var ErrTerminalPullRequest = errors.New("terminal pull request")

type Facts struct {
	PRURL  string
	State  string
	Reason string
}

type StateClient interface {
	PullRequestState(ctx context.Context, prURL string) (state string, merged bool, err error)
}

type terminalError struct {
	Facts Facts
}

func (e terminalError) Error() string {
	reason := strings.TrimSpace(e.Facts.Reason)
	if reason == "" {
		reason = ReasonAlreadyMerged
	}
	if strings.TrimSpace(e.Facts.PRURL) == "" {
		return reason
	}
	return fmt.Sprintf("%s (state=%s): %s", reason, emptyAsUnknown(e.Facts.State), e.Facts.PRURL)
}

func (e terminalError) Is(target error) bool {
	return target == ErrTerminalPullRequest
}

func MergedFacts(ctx context.Context, client StateClient, prURL string) (Facts, bool, error) {
	prURL = strings.TrimSpace(prURL)
	if prURL == "" || client == nil {
		return Facts{}, false, nil
	}
	state, merged, err := client.PullRequestState(ctx, prURL)
	if err != nil {
		return Facts{}, false, err
	}
	if !merged {
		return Facts{}, false, nil
	}
	return Facts{PRURL: prURL, State: state, Reason: ReasonAlreadyMerged}, true, nil
}

func TerminalError(facts Facts) error {
	return terminalError{Facts: facts}
}

func FactsFromError(err error, fallbackPRURL string) (Facts, bool) {
	var terminalErr terminalError
	if !errors.As(err, &terminalErr) {
		return Facts{}, false
	}
	facts := terminalErr.Facts
	if strings.TrimSpace(facts.PRURL) == "" {
		facts.PRURL = strings.TrimSpace(fallbackPRURL)
	}
	if strings.TrimSpace(facts.Reason) == "" {
		facts.Reason = ReasonAlreadyMerged
	}
	return facts, true
}

func emptyAsUnknown(value string) string {
	if strings.TrimSpace(value) == "" {
		return "UNKNOWN"
	}
	return value
}
