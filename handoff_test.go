package main

import (
	"errors"
	"strings"
	"testing"
)

func TestValidatePRForHandoffFallsBackToOpenPRByExpectedBranch(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "weskor/pi-symphony")

	config := testRunnerConfig(t.TempDir())
	config.BaseBranch = "develop"
	candidate := testIssue("CAG-95", "In Progress")

	branch := expectedWorkspaceBranch(candidate.Identifier)
	openPR := pullRequestSummary{
		Number:      73,
		URL:         "https://github.com/weskor/pi-symphony/pull/73",
		BaseRefName: "develop",
		HeadRefName: branch,
	}

	withFakeGitHubAPI(t, fakeGitHubAPI{
		handoffErrorsByURL:  map[string]error{"https://github.com/weskor/pi-symphony/pull/999": errors.New("GitHub API PR handoff lookup failed: 404 Not Found")},
		handoffDetailsByURL: map[string]prHandoffDetails{"https://github.com/weskor/pi-symphony/pull/73": {Number: 73, URL: "https://github.com/weskor/pi-symphony/pull/73", BaseRefName: "develop", HeadRefName: branch}},
		prs:                 []pullRequestSummary{openPR},
	})

	resolved, reason, err := validatePRForHandoff(config, &candidate, "https://github.com/weskor/pi-symphony/pull/999")
	if err != nil {
		t.Fatalf("expected fallback resolution, got %v", err)
	}
	if resolved != openPR.URL {
		t.Fatalf("expected resolved PR URL %q, got %q", openPR.URL, resolved)
	}
	if reason != "" {
		t.Fatalf("expected no handoff block reason, got %q", reason)
	}
}

func TestValidatePRForHandoffFallbackFailsWhenNoBranchMatch(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "weskor/pi-symphony")

	config := testRunnerConfig(t.TempDir())
	config.BaseBranch = "develop"
	candidate := testIssue("CAG-95", "In Progress")

	withFakeGitHubAPI(t, fakeGitHubAPI{
		handoffErr: errors.New("GitHub API PR handoff lookup failed: 404 Not Found"),
		prs: []pullRequestSummary{
			{Number: 73, URL: "https://github.com/weskor/pi-symphony/pull/73", BaseRefName: "develop", HeadRefName: "symphony/CAG-96-workspace"},
		},
	})

	_, _, err := validatePRForHandoff(config, &candidate, "https://github.com/weskor/pi-symphony/pull/999")
	if err == nil {
		t.Fatal("expected error when no matching open PR exists")
	}
	if !strings.Contains(err.Error(), "no open PR found with head branch") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidatePRForHandoffFallbackFailsWhenBranchIsAmbiguous(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "weskor/pi-symphony")

	config := testRunnerConfig(t.TempDir())
	config.BaseBranch = "develop"
	candidate := testIssue("CAG-95", "In Progress")
	branch := expectedWorkspaceBranch(candidate.Identifier)

	withFakeGitHubAPI(t, fakeGitHubAPI{
		handoffErrorsByURL: map[string]error{"https://github.com/weskor/pi-symphony/pull/999": errors.New("GitHub API PR handoff lookup failed: 404 Not Found")},
		handoffDetailsByURL: map[string]prHandoffDetails{
			"https://github.com/weskor/pi-symphony/pull/73": {Number: 73, URL: "https://github.com/weskor/pi-symphony/pull/73", BaseRefName: "develop", HeadRefName: branch},
			"https://github.com/weskor/pi-symphony/pull/84": {Number: 84, URL: "https://github.com/weskor/pi-symphony/pull/84", BaseRefName: "develop", HeadRefName: branch},
		},
		prs: []pullRequestSummary{
			{Number: 73, URL: "https://github.com/weskor/pi-symphony/pull/73", BaseRefName: "develop", HeadRefName: branch},
			{Number: 84, URL: "https://github.com/weskor/pi-symphony/pull/84", BaseRefName: "develop", HeadRefName: branch},
		},
	})

	_, _, err := validatePRForHandoff(config, &candidate, "https://github.com/weskor/pi-symphony/pull/999")
	if err == nil {
		t.Fatal("expected ambiguity error")
	}
	if !strings.Contains(err.Error(), "found 2 open PRs for head branch") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidatePRForHandoffDoesNotFallbackOnNon404LookupError(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "weskor/pi-symphony")

	config := testRunnerConfig(t.TempDir())
	candidate := testIssue("CAG-95", "In Progress")

	withFakeGitHubAPI(t, fakeGitHubAPI{handoffErr: errors.New("transient API failure")})

	_, _, err := validatePRForHandoff(config, &candidate, "https://github.com/weskor/pi-symphony/pull/73")
	if err == nil {
		t.Fatal("expected error for non-recoverable lookup failure")
	}
	if !strings.Contains(err.Error(), "GitHub API PR handoff lookup failed for") {
		t.Fatalf("unexpected error: %v", err)
	}
}
