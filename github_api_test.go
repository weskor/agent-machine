package main

import (
	"context"
	"testing"
	"time"
)

type fakeGitHubAPI struct {
	prs             []pullRequestSummary
	comments        map[string][]githubIssueComment
	feedback        prFeedback
	state           string
	merged          bool
	details         prHandoffDetails
	updatedComments map[int64]string
	createdComments map[int]string
	mergedPRs       map[int]bool
	deletedBranches map[string]bool
	mergeErr        error
	deleteErr       error
}

func (f fakeGitHubAPI) OpenPullRequests(context.Context) ([]pullRequestSummary, error) {
	return f.prs, nil
}

func (f fakeGitHubAPI) PullRequestState(context.Context, string) (string, bool, error) {
	return f.state, f.merged, nil
}

func (f fakeGitHubAPI) PullRequestFeedback(context.Context, int) (prFeedback, error) {
	return f.feedback, nil
}

func (f fakeGitHubAPI) IssueComments(_ context.Context, prNumber string) ([]githubIssueComment, error) {
	return f.comments[prNumber], nil
}

func (f fakeGitHubAPI) UpdateIssueComment(_ context.Context, commentID int64, body string) error {
	if f.updatedComments != nil {
		f.updatedComments[commentID] = body
	}
	return nil
}

func (f fakeGitHubAPI) CreateIssueComment(_ context.Context, prNumber int, body string) error {
	if f.createdComments != nil {
		f.createdComments[prNumber] = body
	}
	return nil
}

func (f fakeGitHubAPI) SquashMergePullRequest(_ context.Context, prNumber int) error {
	if f.mergedPRs != nil {
		f.mergedPRs[prNumber] = true
	}
	return f.mergeErr
}

func (f fakeGitHubAPI) DeleteBranch(_ context.Context, branch string) error {
	if f.deletedBranches != nil {
		f.deletedBranches[branch] = true
	}
	return f.deleteErr
}

func withFakeGitHubAPI(t interface{ Cleanup(func()) }, api githubAPI) {
	previous := newGitHubAPI
	newGitHubAPI = func() (githubAPI, error) { return api, nil }
	t.Cleanup(func() { newGitHubAPI = previous })
}

func withFakeGitHubAppEnv(t interface{ Cleanup(func()) }, fn func() (map[string]string, string, error)) {
	previous := githubAppEnvFromEnvironmentForAPI
	githubAppEnvFromEnvironmentForAPI = fn
	t.Cleanup(func() { githubAppEnvFromEnvironmentForAPI = previous })
}

func (f fakeGitHubAPI) PullRequestHandoffDetails(context.Context, string) (prHandoffDetails, error) {
	return f.details, nil
}

func TestGitHubClientWithTimeoutDefaultsNonPositiveTimeout(t *testing.T) {
	previousTimeout := defaultGitHubCommandTimeout
	defaultGitHubCommandTimeout = time.Minute
	t.Cleanup(func() { defaultGitHubCommandTimeout = previousTimeout })

	withFakeGitHubAPI(t, fakeGitHubAPI{})

	_, ctx, cancel, err := githubClientWithTimeout(0)
	if err != nil {
		t.Fatalf("githubClientWithTimeout returned error: %v", err)
	}
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("context has no deadline")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 || remaining > time.Minute {
		t.Fatalf("deadline remaining = %v, want within default timeout", remaining)
	}
}
