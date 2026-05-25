package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sh "github.com/weskor/agent-machine/internal/shell"
	"github.com/weskor/agent-machine/internal/state"
)

func TestEnsureRunnerPRHandoffCreatesPRWhenAgentEmitsNoURL(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "weskor/agent-machine")
	workspace := runnerHandoffGitWorkspace(t, "main")
	candidate := testIssue("CAG-118", "In Progress")
	if err := sh.Run("git switch -q -C "+sh.Quote(expectedWorkspaceBranch(candidate.Identifier))+" && echo change > handoff.go", workspace); err != nil {
		t.Fatal(err)
	}
	config := testRunnerConfig(filepath.Dir(workspace))
	config.BaseBranch = "main"
	withFakeGitHubAPI(t, fakeGitHubAPI{})

	prURL, err := ensureRunnerPRHandoff(config, &candidate, workspace, "", nil)
	if err != nil {
		t.Fatalf("ensureRunnerPRHandoff returned error: %v", err)
	}
	if prURL != "https://github.com/weskor/agent-machine/pull/900" {
		t.Fatalf("unexpected PR URL %q", prURL)
	}
}

func TestEnsureRunnerPRHandoffExecutesPersistedPayloadBoundary(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "weskor/agent-machine")
	workspace := runnerHandoffGitWorkspace(t, "main")
	candidate := testIssue("CAG-179", "In Progress")
	branch := expectedWorkspaceBranch(candidate.Identifier)
	if err := sh.Run("git switch -q -C "+sh.Quote(branch)+" && echo change > handoff.go", workspace); err != nil {
		t.Fatal(err)
	}
	config := testRunnerConfig(filepath.Dir(workspace))
	config.BaseBranch = "main"
	rewrittenURL := "https://github.com/weskor/agent-machine/pull/179"
	withFakeGitHubAPI(t, fakeGitHubAPI{
		handoffDetailsByURL: map[string]prHandoffDetails{
			rewrittenURL: {Number: 179, URL: rewrittenURL, BaseRefName: "main", HeadRefName: branch},
		},
	})
	previous := readPRHandoffPendingPayloadForExecution
	readPRHandoffPendingPayloadForExecution = func(workspaceRoot, issueIdentifier string) (prHandoffPendingPayload, error) {
		payload, err := readPRHandoffPendingPayload(workspaceRoot, issueIdentifier)
		if err != nil {
			return prHandoffPendingPayload{}, err
		}
		snapshot, err := readRunProgress(workspaceRoot, issueIdentifier)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Phase != runProgressPhasePRHandoffPending || snapshot.Status != runProgressPhasePRHandoffPending || snapshot.NextAction != "complete_runner_pr_handoff" || snapshot.PRHandoffPayloadPath == "" {
			t.Fatalf("progress before PR handoff side effects = %+v; want pr_handoff_pending", snapshot)
		}
		payload.AgentPRURL = rewrittenURL
		return payload, nil
	}
	t.Cleanup(func() { readPRHandoffPendingPayloadForExecution = previous })

	prURL, err := ensureRunnerPRHandoff(config, &candidate, workspace, "", nil)
	if err != nil {
		t.Fatalf("ensureRunnerPRHandoff returned error: %v", err)
	}
	if prURL != rewrittenURL {
		t.Fatalf("PR URL = %q; want side effects from persisted payload read %q", prURL, rewrittenURL)
	}
}

func TestPRHandoffPendingPayloadRoundTripsInput(t *testing.T) {
	root := t.TempDir()
	candidate := testIssue("CAG-180", "In Progress")
	candidate.URL = "https://linear.app/acme/issue/CAG-180"
	candidate.Description = "Implement handoff payload."
	candidate.Team.ID = "team-180"
	workspace := filepath.Join(root, candidate.Identifier)
	input := prHandoffPendingPayloadFromInput(prHandoffInput{Candidate: &candidate, Workspace: workspace, AgentPRURL: "https://github.com/weskor/agent-machine/pull/180"})
	if err := writePRHandoffPendingPayload(root, input); err != nil {
		t.Fatal(err)
	}
	payload, err := readPRHandoffPendingPayload(root, candidate.Identifier)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip := payload.Issue()
	if roundTrip.Identifier != candidate.Identifier || roundTrip.ID != candidate.ID || roundTrip.URL != candidate.URL || roundTrip.Description != candidate.Description || roundTrip.Team.ID != "team-180" || payload.Workspace != workspace || payload.AgentPRURL != input.AgentPRURL || payload.Branch != expectedWorkspaceBranch(candidate.Identifier) {
		t.Fatalf("payload = %+v issue=%+v; want PR handoff input round trip", payload, roundTrip)
	}
}

func TestEnsureRunnerPRHandoffCreatesPRWhenAgentEmitsStaleMissingURL(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "weskor/agent-machine")
	workspace := runnerHandoffGitWorkspace(t, "main")
	candidate := testIssue("CAG-91", "In Progress")
	if err := sh.Run("git switch -q -C "+sh.Quote(expectedWorkspaceBranch(candidate.Identifier))+" && echo change > handoff.go", workspace); err != nil {
		t.Fatal(err)
	}
	config := testRunnerConfig(filepath.Dir(workspace))
	config.BaseBranch = "main"
	withFakeGitHubAPI(t, fakeGitHubAPI{
		handoffErrorsByURL: map[string]error{"https://github.com/weskor/agent-machine/pull/73": errors.New("GitHub API PR handoff lookup failed: 404 Not Found")},
		prs: []pullRequestSummary{
			{Number: 73, URL: "https://github.com/weskor/agent-machine/pull/73", BaseRefName: "main", HeadRefName: "symphony/other-workspace"},
		},
	})

	prURL, err := ensureRunnerPRHandoff(config, &candidate, workspace, "https://github.com/weskor/agent-machine/pull/73", nil)
	if err != nil {
		t.Fatalf("ensureRunnerPRHandoff returned error: %v", err)
	}
	if prURL != "https://github.com/weskor/agent-machine/pull/900" {
		t.Fatalf("expected runner-created PR URL, got %q", prURL)
	}
}

func TestEnsureRunnerPRHandoffRecordsDurableIntentResult(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "weskor/agent-machine")
	workspace := runnerHandoffGitWorkspace(t, "main")
	candidate := testIssue("CAG-185", "In Progress")
	branch := expectedWorkspaceBranch(candidate.Identifier)
	if err := sh.Run("git switch -q -C "+sh.Quote(branch)+" && echo change > handoff.go", workspace); err != nil {
		t.Fatal(err)
	}
	config := testRunnerConfig(filepath.Dir(workspace))
	config.BaseBranch = "main"
	store, err := state.Open(context.Background(), state.DefaultDBPath(config.WorkspaceRoot))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	withFakeGitHubAPI(t, fakeGitHubAPI{})

	prURL, err := ensureRunnerPRHandoffFromInput(config, prHandoffInput{Candidate: &candidate, Workspace: workspace, StateStore: store}, nil)
	if err != nil {
		t.Fatalf("ensureRunnerPRHandoffFromInput() error = %v", err)
	}
	intent, ok, err := store.PRHandoffIntent(context.Background(), candidate.Identifier, 1)
	if err != nil || !ok {
		t.Fatalf("PRHandoffIntent() ok=%t err=%v", ok, err)
	}
	if intent.Status != state.PRHandoffIntentStatusCompleted || intent.PRURL != prURL || intent.Result != "success" {
		t.Fatalf("intent = %+v; want completed PR handoff result for %s", intent, prURL)
	}
}

func TestEnsureRunnerPRHandoffIgnoresWrongBranchAdvisoryPR(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "weskor/agent-machine")
	workspace := runnerHandoffGitWorkspace(t, "main")
	candidate := testIssue("CAG-129", "In Progress")
	if err := sh.Run("git switch -q -C "+sh.Quote(expectedWorkspaceBranch(candidate.Identifier))+" && echo change > handoff.go", workspace); err != nil {
		t.Fatal(err)
	}
	config := testRunnerConfig(filepath.Dir(workspace))
	config.BaseBranch = "main"
	staleURL := "https://github.com/weskor/agent-machine/pull/25"
	withFakeGitHubAPI(t, fakeGitHubAPI{
		handoffDetailsByURL: map[string]prHandoffDetails{staleURL: {Number: 25, URL: staleURL, BaseRefName: "main", HeadRefName: "symphony/CAG-86-workspace"}},
	})

	prURL, err := ensureRunnerPRHandoff(config, &candidate, workspace, staleURL, nil)
	if err != nil {
		t.Fatalf("ensureRunnerPRHandoff returned error: %v", err)
	}
	if prURL != "https://github.com/weskor/agent-machine/pull/900" {
		t.Fatalf("expected runner-created PR URL, got %q", prURL)
	}
}

func TestEnsureRunnerPRHandoffUpdatesExistingRetryBranch(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "weskor/agent-machine")
	workspace := runnerHandoffGitWorkspace(t, "main")
	candidate := testIssue("CAG-130", "In Progress")
	branch := expectedWorkspaceBranch(candidate.Identifier)
	if err := sh.Run("git switch -q -C "+sh.Quote(branch)+" && echo first > handoff.go && git add handoff.go && git commit -qm first && git push origin HEAD:refs/heads/"+sh.Quote(branch), workspace); err != nil {
		t.Fatal(err)
	}
	if err := sh.Run("echo second > handoff.go", workspace); err != nil {
		t.Fatal(err)
	}
	config := testRunnerConfig(filepath.Dir(workspace))
	config.BaseBranch = "main"
	withFakeGitHubAPI(t, fakeGitHubAPI{})

	prURL, err := ensureRunnerPRHandoff(config, &candidate, workspace, "", nil)
	if err != nil {
		t.Fatalf("ensureRunnerPRHandoff returned error: %v", err)
	}
	if prURL != "https://github.com/weskor/agent-machine/pull/900" {
		t.Fatalf("expected runner-created PR URL, got %q", prURL)
	}
	remoteHead, err := sh.CaptureQuiet("git rev-parse origin/"+sh.Quote(branch), workspace)
	if err != nil {
		t.Fatal(err)
	}
	localHead, err := sh.CaptureQuiet("git rev-parse HEAD", workspace)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(remoteHead) != strings.TrimSpace(localHead) {
		t.Fatalf("remote retry branch was not updated: remote=%s local=%s", remoteHead, localHead)
	}
}

func TestEnsureRunnerPRHandoffFailsOnNoBranchChanges(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "weskor/agent-machine")
	workspace := runnerHandoffGitWorkspace(t, "main")
	candidate := testIssue("CAG-118", "In Progress")
	if err := sh.Run("git switch -q -C "+sh.Quote(expectedWorkspaceBranch(candidate.Identifier)), workspace); err != nil {
		t.Fatal(err)
	}
	config := testRunnerConfig(filepath.Dir(workspace))
	config.BaseBranch = "main"
	withFakeGitHubAPI(t, fakeGitHubAPI{})

	_, err := ensureRunnerPRHandoff(config, &candidate, workspace, "", nil)
	if err == nil || !strings.Contains(err.Error(), "no branch changes") {
		t.Fatalf("expected no branch changes error, got %v", err)
	}
}

func runnerHandoffGitWorkspace(t *testing.T, base string) string {
	t.Helper()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	workspace := filepath.Join(root, "CAG-118")
	if err := sh.Run("git init -q --bare "+sh.Quote(remote), ""); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := "git init -q && git config user.email test@example.com && git config user.name Test && git checkout -q -b " + sh.Quote(base) + " && echo base > README.md && git add README.md && git commit -qm base && git remote add origin " + sh.Quote(remote) + " && git push -q -u origin " + sh.Quote(base)
	if err := sh.Run(cmd, workspace); err != nil {
		t.Fatal(err)
	}
	return workspace
}

func TestValidatePRForHandoffFallsBackToOpenPRByExpectedBranch(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "weskor/agent-machine")

	config := testRunnerConfig(t.TempDir())
	config.BaseBranch = "develop"
	candidate := testIssue("CAG-95", "In Progress")

	branch := expectedWorkspaceBranch(candidate.Identifier)
	openPR := pullRequestSummary{
		Number:      73,
		URL:         "https://github.com/weskor/agent-machine/pull/73",
		BaseRefName: "develop",
		HeadRefName: branch,
	}

	withFakeGitHubAPI(t, fakeGitHubAPI{
		handoffErrorsByURL:  map[string]error{"https://github.com/weskor/agent-machine/pull/999": errors.New("GitHub API PR handoff lookup failed: 404 Not Found")},
		handoffDetailsByURL: map[string]prHandoffDetails{"https://github.com/weskor/agent-machine/pull/73": {Number: 73, URL: "https://github.com/weskor/agent-machine/pull/73", BaseRefName: "develop", HeadRefName: branch}},
		prs:                 []pullRequestSummary{openPR},
	})

	resolved, reason, err := validatePRForHandoff(config, &candidate, "https://github.com/weskor/agent-machine/pull/999")
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
	t.Setenv("GITHUB_REPOSITORY", "weskor/agent-machine")

	config := testRunnerConfig(t.TempDir())
	config.BaseBranch = "develop"
	candidate := testIssue("CAG-95", "In Progress")

	withFakeGitHubAPI(t, fakeGitHubAPI{
		handoffErr: errors.New("GitHub API PR handoff lookup failed: 404 Not Found"),
		prs: []pullRequestSummary{
			{Number: 73, URL: "https://github.com/weskor/agent-machine/pull/73", BaseRefName: "develop", HeadRefName: "symphony/CAG-96-workspace"},
		},
	})

	_, _, err := validatePRForHandoff(config, &candidate, "https://github.com/weskor/agent-machine/pull/999")
	if err == nil {
		t.Fatal("expected error when no matching open PR exists")
	}
	if !strings.Contains(err.Error(), "no open PR found with head branch") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidatePRForHandoffFallbackFailsWhenBranchIsAmbiguous(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "weskor/agent-machine")

	config := testRunnerConfig(t.TempDir())
	config.BaseBranch = "develop"
	candidate := testIssue("CAG-95", "In Progress")
	branch := expectedWorkspaceBranch(candidate.Identifier)

	withFakeGitHubAPI(t, fakeGitHubAPI{
		handoffErrorsByURL: map[string]error{"https://github.com/weskor/agent-machine/pull/999": errors.New("GitHub API PR handoff lookup failed: 404 Not Found")},
		handoffDetailsByURL: map[string]prHandoffDetails{
			"https://github.com/weskor/agent-machine/pull/73": {Number: 73, URL: "https://github.com/weskor/agent-machine/pull/73", BaseRefName: "develop", HeadRefName: branch},
			"https://github.com/weskor/agent-machine/pull/84": {Number: 84, URL: "https://github.com/weskor/agent-machine/pull/84", BaseRefName: "develop", HeadRefName: branch},
		},
		prs: []pullRequestSummary{
			{Number: 73, URL: "https://github.com/weskor/agent-machine/pull/73", BaseRefName: "develop", HeadRefName: branch},
			{Number: 84, URL: "https://github.com/weskor/agent-machine/pull/84", BaseRefName: "develop", HeadRefName: branch},
		},
	})

	_, _, err := validatePRForHandoff(config, &candidate, "https://github.com/weskor/agent-machine/pull/999")
	if err == nil {
		t.Fatal("expected ambiguity error")
	}
	if !strings.Contains(err.Error(), "found 2 open PRs for head branch") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidatePRForHandoffDoesNotFallbackOnNon404LookupError(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "weskor/agent-machine")

	config := testRunnerConfig(t.TempDir())
	candidate := testIssue("CAG-95", "In Progress")

	withFakeGitHubAPI(t, fakeGitHubAPI{handoffErr: errors.New("transient API failure")})

	_, _, err := validatePRForHandoff(config, &candidate, "https://github.com/weskor/agent-machine/pull/73")
	if err == nil {
		t.Fatal("expected error for non-recoverable lookup failure")
	}
	if !strings.Contains(err.Error(), "GitHub API PR handoff lookup failed for") {
		t.Fatalf("unexpected error: %v", err)
	}
}
