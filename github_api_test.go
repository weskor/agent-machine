package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v69/github"
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

func TestConfigureGitHubRepositoryFromWorkflowUsesWorkflowRepoRemote(t *testing.T) {
	repo := t.TempDir()
	workflowDir := filepath.Join(repo, ".symphony", "workspaces", "CAG-1")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "init", "-q", repo).Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	if err := exec.Command("git", "-C", repo, "remote", "add", "origin", "git@github.com:pennywise-investments/compound-web.git").Run(); err != nil {
		t.Fatalf("git remote add: %v", err)
	}

	t.Setenv("GITHUB_REPOSITORY", "")
	configureGitHubRepositoryFromWorkflow(filepath.Join(workflowDir, "WORKFLOW.md"))

	if got := os.Getenv("GITHUB_REPOSITORY"); got != "pennywise-investments/compound-web" {
		t.Fatalf("GITHUB_REPOSITORY = %q, want pennywise-investments/compound-web", got)
	}
}

func TestGitHubAPITokenFromEnvironmentPrefersExplicitGHToken(t *testing.T) {
	t.Setenv("GH_TOKEN", "explicit-gh-token")
	t.Setenv("GITHUB_TOKEN", "fallback-token")
	called := false
	withFakeGitHubAppEnv(t, func() (map[string]string, string, error) {
		called = true
		return map[string]string{"GH_TOKEN": "app-token"}, "github_app_installation", nil
	})

	token, err := githubAPITokenFromEnvironment()
	if err != nil {
		t.Fatalf("expected explicit token, got error: %v", err)
	}
	if token != "explicit-gh-token" {
		t.Fatalf("expected GH_TOKEN to win, got %q", token)
	}
	if called {
		t.Fatal("did not expect GitHub App token minting when GH_TOKEN is set")
	}
}

func TestGitHubAPITokenFromEnvironmentFallsBackToGitHubToken(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "explicit-github-token")
	withFakeGitHubAppEnv(t, func() (map[string]string, string, error) {
		t.Fatal("did not expect GitHub App token minting when GITHUB_TOKEN is set")
		return nil, "", nil
	})

	token, err := githubAPITokenFromEnvironment()
	if err != nil {
		t.Fatalf("expected GITHUB_TOKEN, got error: %v", err)
	}
	if token != "explicit-github-token" {
		t.Fatalf("expected GITHUB_TOKEN fallback, got %q", token)
	}
}

func TestGitHubAPITokenFromEnvironmentUsesGitHubAppToken(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	withFakeGitHubAppEnv(t, func() (map[string]string, string, error) {
		return map[string]string{"GH_TOKEN": "minted-app-token", "GITHUB_TOKEN": "minted-app-token"}, "github_app_installation", nil
	})

	token, err := githubAPITokenFromEnvironment()
	if err != nil {
		t.Fatalf("expected GitHub App token, got error: %v", err)
	}
	if token != "minted-app-token" {
		t.Fatalf("expected minted GitHub App token, got %q", token)
	}
}

func TestGitHubAPITokenFromEnvironmentErrorsWithoutAuth(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	withFakeGitHubAppEnv(t, func() (map[string]string, string, error) {
		return nil, "default_gh_auth", nil
	})

	_, err := githubAPITokenFromEnvironment()
	if err == nil {
		t.Fatal("expected missing auth error")
	}
	if !strings.Contains(err.Error(), "GITHUB_APP_ID") {
		t.Fatalf("expected error to mention GitHub App fallback env, got %q", err.Error())
	}
}

func TestOpenPullRequestsUsesGitHubAppCompatibleRESTMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/acme/rocket/pulls":
			if r.URL.Query().Get("state") != "open" || r.URL.Query().Get("sort") != "updated" {
				t.Fatalf("unexpected open PR query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`[{"number":7}]`))
		case "/repos/acme/rocket/pulls/7":
			_, _ = w.Write([]byte(`{"number":7,"html_url":"https://github.com/acme/rocket/pull/7","user":{"login":"compound-symphony-bot[bot]"},"base":{"ref":"develop"},"head":{"ref":"symphony/CAG-43-workspace","sha":"abc123"},"mergeable":true,"mergeable_state":"clean"}`))
		case "/repos/acme/rocket/pulls/7/commits":
			_, _ = w.Write([]byte(`[{"sha":"c0ffee","commit":{"author":{"name":"compound-symphony-bot[bot]","email":"285402021+compound-symphony-bot[bot]@users.noreply.github.com","date":"2026-05-19T06:40:15Z"}},"author":{"login":"compound-symphony-bot[bot]"}}]`))
		case "/repos/acme/rocket/pulls/7/reviews":
			_, _ = w.Write([]byte(`[{"state":"APPROVED","user":{"login":"human-reviewer"}}]`))
		case "/repos/acme/rocket/commits/abc123/status":
			_, _ = w.Write([]byte(`{"statuses":[{"context":"Vercel – customer","state":"success"}]}`))
		case "/repos/acme/rocket/commits/abc123/check-runs":
			_, _ = w.Write([]byte(`{"total_count":1,"check_runs":[{"name":"lint","status":"completed","conclusion":"success"}]}`))
		default:
			t.Fatalf("unexpected GitHub API path: %s", r.URL.String())
		}
	}))
	defer server.Close()

	client := github.NewClient(server.Client())
	client.BaseURL = mustParseURL(t, server.URL+"/")
	api := goGitHubClient{owner: "acme", repo: "rocket", client: client}

	prs, err := api.OpenPullRequests(context.Background())
	if err != nil {
		t.Fatalf("expected open PR lookup to succeed, got error: %v", err)
	}
	if len(prs) != 1 {
		t.Fatalf("expected one PR, got %d", len(prs))
	}
	pr := prs[0]
	if pr.Number != 7 || pr.URL != "https://github.com/acme/rocket/pull/7" || pr.BaseRefName != "develop" || pr.HeadRefName != "symphony/CAG-43-workspace" {
		t.Fatalf("unexpected PR metadata: %+v", pr)
	}
	if pr.AuthorLogin() != "compound-symphony-bot[bot]" || pr.Mergeable != "MERGEABLE" || pr.MergeStateStatus != "CLEAN" || pr.ReviewDecision != "APPROVED" {
		t.Fatalf("unexpected PR gates: %+v", pr)
	}
	if len(pr.Commits) != 1 || pr.Commits[0].Author.Name != githubAppBotName || pr.Commits[0].Author.Email != githubAppBotEmail {
		t.Fatalf("expected bot commit identity evidence, got %+v", pr.Commits)
	}
	if reason := checksBlockReason(pr.StatusCheckRollup); reason != "" {
		t.Fatalf("expected green status rollup, got blocker: %s", reason)
	}
}

func TestOpenPullRequestsMarksInaccessibleCheckRunsUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/acme/rocket/pulls":
			_, _ = w.Write([]byte(`[{"number":8}]`))
		case "/repos/acme/rocket/pulls/8":
			_, _ = w.Write([]byte(`{"number":8,"html_url":"https://github.com/acme/rocket/pull/8","user":{"login":"app/compound-symphony-bot"},"base":{"ref":"develop"},"head":{"ref":"symphony/CAG-43-workspace","sha":"def456"},"mergeable":true,"mergeable_state":"clean"}`))
		case "/repos/acme/rocket/pulls/8/commits":
			_, _ = w.Write([]byte(`[{"sha":"def456","commit":{"author":{"name":"compound-symphony-bot[bot]","email":"285402021+compound-symphony-bot[bot]@users.noreply.github.com","date":"2026-05-19T06:40:15Z"}},"author":{"login":"compound-symphony-bot[bot]"}}]`))
		case "/repos/acme/rocket/pulls/8/reviews":
			_, _ = w.Write([]byte(`[{"state":"APPROVED","user":{"login":"human-reviewer"}}]`))
		case "/repos/acme/rocket/commits/def456/status":
			_, _ = w.Write([]byte(`{"statuses":[{"context":"Vercel – customer","state":"success"}]}`))
		case "/repos/acme/rocket/commits/def456/check-runs":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"Resource not accessible by integration"}`))
		default:
			t.Fatalf("unexpected GitHub API path: %s", r.URL.String())
		}
	}))
	defer server.Close()

	client := github.NewClient(server.Client())
	client.BaseURL = mustParseURL(t, server.URL+"/")
	api := goGitHubClient{owner: "acme", repo: "rocket", client: client}

	prs, err := api.OpenPullRequests(context.Background())
	if err != nil {
		t.Fatalf("expected inaccessible check runs to block merge without failing status, got error: %v", err)
	}
	if len(prs) != 1 {
		t.Fatalf("expected one PR, got %d", len(prs))
	}
	if reason := checksBlockReason(prs[0].StatusCheckRollup); !strings.Contains(reason, "GitHub check runs") {
		t.Fatalf("expected check-run access blocker, got %q", reason)
	}
}

func TestReviewDecisionFromReviewsUsesLatestReviewerState(t *testing.T) {
	reviews := []*github.PullRequestReview{
		{User: &github.User{Login: github.Ptr("reviewer")}, State: github.Ptr("APPROVED")},
		{User: &github.User{Login: github.Ptr("reviewer")}, State: github.Ptr("COMMENTED")},
		{User: &github.User{Login: github.Ptr("second")}, State: github.Ptr("APPROVED")},
	}
	if got := reviewDecisionFromReviews(reviews); got != "APPROVED" {
		t.Fatalf("expected approval from latest reviewer states, got %q", got)
	}
	reviews = append(reviews, &github.PullRequestReview{User: &github.User{Login: github.Ptr("second")}, State: github.Ptr("CHANGES_REQUESTED")})
	if got := reviewDecisionFromReviews(reviews); got != "CHANGES_REQUESTED" {
		t.Fatalf("expected changes requested to win, got %q", got)
	}
}

func mustParseURL(t *testing.T, value string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatalf("failed to parse test URL: %v", err)
	}
	return parsed
}

func (f fakeGitHubAPI) PullRequestHandoffDetails(context.Context, string) (prHandoffDetails, error) {
	return f.details, nil
}

func TestMapPullRequestFeedbackPreservesGhShapedReviewAndCommentFields(t *testing.T) {
	submittedAt := github.Timestamp{Time: time.Date(2026, 5, 18, 21, 0, 0, 0, time.UTC)}
	createdAt := github.Timestamp{Time: time.Date(2026, 5, 18, 21, 1, 0, 0, time.UTC)}
	reviewCreatedAt := github.Timestamp{Time: time.Date(2026, 5, 18, 21, 2, 0, 0, time.UTC)}

	feedback := mapPullRequestFeedback(
		[]*github.PullRequestReview{{User: &github.User{Login: github.Ptr("weskor")}, State: github.Ptr("CHANGES_REQUESTED"), Body: github.Ptr("fix typed client parity"), SubmittedAt: &submittedAt}},
		[]*github.IssueComment{{User: &github.User{Login: github.Ptr("vercel")}, Body: github.Ptr("preview ready"), CreatedAt: &createdAt}},
		[]*github.PullRequestComment{{User: &github.User{Login: github.Ptr("reviewer")}, Path: github.Ptr("tools/pi-symphony/github_api.go"), Line: github.Ptr(42), Body: github.Ptr("inline note"), CreatedAt: &reviewCreatedAt}},
	)
	rendered := renderPRFeedback(448, feedback)

	for _, want := range []string{
		"## Review: CHANGES_REQUESTED by weskor",
		"fix typed client parity",
		"## Comment by vercel",
		"preview ready",
		"tools/pi-symphony/github_api.go:42",
		"inline note",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("mapped feedback missing %q:\n%s", want, rendered)
		}
	}
	if feedback.Reviews[0].SubmittedAt == "" || feedback.Comments[0].CreatedAt == "" || feedback.ReviewComments[0].CreatedAt == "" {
		t.Fatalf("expected mapped timestamps to preserve gh-shaped feedback fields: %+v", feedback)
	}
}
