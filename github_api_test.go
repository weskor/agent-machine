package main

import (
	"context"
	"fmt"
	"testing"
	"time"
)

type fakeGitHubAPI struct {
	prs                 []pullRequestSummary
	feedback            prFeedback
	state               string
	merged              bool
	handoffDetailsByURL map[string]prHandoffDetails
	handoffErrorsByURL  map[string]error
	handoffErr          error
	details             prHandoffDetails
	mergedPRs           map[int]bool
	deletedBranches     map[string]bool
	updatedPRs          map[int]bool
	updatedPRBodies     map[int]string
	mergeErr            error
	deleteErr           error
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

func TestCodeHostProviderNormalizesConfiguredProvider(t *testing.T) {
	if got := codeHostProvider(runnerConfig{RepositoryProvider: " GitLab "}); got != "gitlab" {
		t.Fatalf("provider = %q, want gitlab", got)
	}
}

func TestNewCodeHostAPIRejectsUnsupportedProvider(t *testing.T) {
	if _, err := newCodeHostAPI(runnerConfig{RepositoryProvider: "bitbucket"}); err == nil {
		t.Fatal("expected unsupported provider error")
	}
}

func (f fakeGitHubAPI) PullRequestHandoffDetails(_ context.Context, prURL string) (prHandoffDetails, error) {
	if f.handoffErrorsByURL != nil {
		if detailsErr, ok := f.handoffErrorsByURL[prURL]; ok {
			return prHandoffDetails{}, detailsErr
		}
	}
	if f.handoffDetailsByURL != nil {
		if details, ok := f.handoffDetailsByURL[prURL]; ok {
			return details, nil
		}
	}
	if f.handoffErr != nil {
		return prHandoffDetails{}, f.handoffErr
	}
	var number int
	if _, err := fmt.Sscanf(prURL, "https://github.com/weskor/agent-machine/pull/%d", &number); err == nil && number != 0 {
		return prHandoffDetails{Number: number, URL: prURL, BaseRefName: "main", HeadRefName: expectedWorkspaceBranch("CAG-119"), Author: prAuthor{Login: "app/agent-machine-bot"}}, nil
	}
	return f.details, nil
}

func (f fakeGitHubAPI) CreatePullRequest(_ context.Context, title, body, head, base string) (prHandoffDetails, error) {
	if f.updatedPRBodies != nil {
		f.updatedPRBodies[900] = body
	}
	return prHandoffDetails{Number: 900, URL: "https://github.com/weskor/agent-machine/pull/900", BaseRefName: base, HeadRefName: head, Author: prAuthor{Login: "app/agent-machine-bot"}}, nil
}

func (f fakeGitHubAPI) UpdatePullRequest(_ context.Context, number int, title, body, base string) (prHandoffDetails, error) {
	if f.updatedPRs != nil {
		f.updatedPRs[number] = true
	}
	if f.updatedPRBodies != nil {
		f.updatedPRBodies[number] = body
	}
	url := fmt.Sprintf("https://github.com/weskor/agent-machine/pull/%d", number)
	if f.handoffDetailsByURL != nil {
		if details, ok := f.handoffDetailsByURL[url]; ok {
			details.BaseRefName = base
			return details, nil
		}
	}
	return prHandoffDetails{Number: number, URL: url, BaseRefName: base, Author: prAuthor{Login: "app/agent-machine-bot"}}, nil
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
