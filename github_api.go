package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-github/v69/github"
)

type githubAPI interface {
	OpenPullRequests(ctx context.Context) ([]pullRequestSummary, error)
	PullRequestState(ctx context.Context, prURL string) (string, bool, error)
	PullRequestFeedback(ctx context.Context, prNumber int) (prFeedback, error)
	IssueComments(ctx context.Context, prNumber string) ([]githubIssueComment, error)
	UpdateIssueComment(ctx context.Context, commentID int64, body string) error
	CreateIssueComment(ctx context.Context, prNumber int, body string) error
	SquashMergePullRequest(ctx context.Context, prNumber int) error
	DeleteBranch(ctx context.Context, branch string) error
	PullRequestHandoffDetails(ctx context.Context, prURL string) (prHandoffDetails, error)
}

var newGitHubAPI = newGoGitHubClient

var githubAppEnvFromEnvironmentForAPI = githubAppEnvFromEnvironment

type goGitHubClient struct {
	owner  string
	repo   string
	client *github.Client
}

func newGoGitHubClient() (githubAPI, error) {
	owner, repo, err := currentGitHubRepo()
	if err != nil {
		return nil, err
	}
	token, err := githubAPITokenFromEnvironment()
	if err != nil {
		return nil, err
	}
	return &goGitHubClient{owner: owner, repo: repo, client: github.NewClient(nil).WithAuthToken(token)}, nil
}

func githubAPITokenFromEnvironment() (string, error) {
	token := strings.TrimSpace(os.Getenv("GH_TOKEN"))
	if token == "" {
		token = strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
	}
	if token != "" {
		return token, nil
	}
	githubAppEnv, _, err := githubAppEnvFromEnvironmentForAPI()
	if err != nil {
		return "", err
	}
	if githubAppEnv != nil {
		if token := strings.TrimSpace(githubAppEnv["GH_TOKEN"]); token != "" {
			return token, nil
		}
		if token := strings.TrimSpace(githubAppEnv["GITHUB_TOKEN"]); token != "" {
			return token, nil
		}
	}
	if token == "" {
		return "", fmt.Errorf("GitHub API token missing: set GH_TOKEN or GITHUB_TOKEN, or configure GITHUB_APP_ID, GITHUB_APP_INSTALLATION_ID, and GITHUB_APP_PRIVATE_KEY_PATH")
	}
	return token, nil
}

func currentGitHubRepo() (string, string, error) {
	if value := strings.TrimSpace(os.Getenv("GITHUB_REPOSITORY")); value != "" {
		parts := strings.Split(value, "/")
		if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
			return parts[0], parts[1], nil
		}
	}
	out, err := exec.Command("git", "config", "--get", "remote.origin.url").Output()
	if err != nil {
		return "", "", fmt.Errorf("GitHub repository could not be determined from GITHUB_REPOSITORY or git remote origin: %w", err)
	}
	remote := strings.TrimSpace(string(out))
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`github\.com[:/]([^/]+)/([^/.]+)(?:\.git)?$`),
		regexp.MustCompile(`github\.com/([^/]+)/([^/.]+)(?:\.git)?$`),
	}
	for _, pattern := range patterns {
		match := pattern.FindStringSubmatch(remote)
		if len(match) == 3 {
			return match[1], match[2], nil
		}
	}
	return "", "", fmt.Errorf("GitHub repository could not be parsed from remote origin %q", remote)
}

func githubClientWithTimeout(timeout time.Duration) (githubAPI, context.Context, context.CancelFunc, error) {
	client, err := newGitHubAPI()
	if err != nil {
		return nil, nil, nil, err
	}
	if timeout <= 0 {
		timeout = defaultGitHubCommandTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	return client, ctx, cancel, nil
}

func (c *goGitHubClient) OpenPullRequests(ctx context.Context) ([]pullRequestSummary, error) {
	options := &github.PullRequestListOptions{State: "open", Sort: "updated", Direction: "desc", ListOptions: github.ListOptions{PerPage: 100}}
	prs := []pullRequestSummary{}
	for {
		openPRs, response, err := c.client.PullRequests.List(ctx, c.owner, c.repo, options)
		if err != nil {
			return nil, fmt.Errorf("GitHub API open PR list failed: %w", err)
		}
		for _, openPR := range openPRs {
			details := openPR
			if openPR.GetNumber() != 0 {
				fetched, _, err := c.client.PullRequests.Get(ctx, c.owner, c.repo, openPR.GetNumber())
				if err != nil {
					return nil, fmt.Errorf("GitHub API open PR #%d details lookup failed: %w", openPR.GetNumber(), err)
				}
				details = fetched
			}
			summary := c.openPullRequestSummary(ctx, details)
			if details.GetNumber() != 0 {
				commits, err := c.pullRequestCommits(ctx, details.GetNumber())
				if err != nil {
					return nil, err
				}
				summary.Commits = commits
			}
			prs = append(prs, summary)
		}
		if response == nil || response.NextPage == 0 {
			break
		}
		options.Page = response.NextPage
	}
	return prs, nil
}

func (c *goGitHubClient) pullRequestCommits(ctx context.Context, number int) ([]prCommit, error) {
	options := &github.ListOptions{PerPage: 100}
	commits := []prCommit{}
	for {
		githubCommits, response, err := c.client.PullRequests.ListCommits(ctx, c.owner, c.repo, number, options)
		if err != nil {
			return nil, fmt.Errorf("GitHub API PR #%d commits lookup failed: %w", number, err)
		}
		for _, githubCommit := range githubCommits {
			if githubCommit == nil {
				continue
			}
			author := githubCommit.GetCommit().GetAuthor()
			commits = append(commits, prCommit{
				OID: githubCommit.GetSHA(),
				Author: prCommitAuthor{
					Name:  author.GetName(),
					Email: author.GetEmail(),
					Login: githubCommit.GetAuthor().GetLogin(),
				},
			})
		}
		if response == nil || response.NextPage == 0 {
			break
		}
		options.Page = response.NextPage
	}
	return commits, nil
}

func (c *goGitHubClient) openPullRequestSummary(ctx context.Context, pr *github.PullRequest) pullRequestSummary {
	return pullRequestSummary{
		Number:            pr.GetNumber(),
		URL:               pr.GetHTMLURL(),
		BaseRefName:       pr.GetBase().GetRef(),
		HeadRefName:       pr.GetHead().GetRef(),
		Author:            prAuthor{Login: pr.GetUser().GetLogin()},
		Mergeable:         mergeableFromPullRequest(pr),
		MergeStateStatus:  mergeStateStatusFromPullRequest(pr),
		ReviewDecision:    c.pullRequestReviewDecision(ctx, pr.GetNumber()),
		StatusCheckRollup: c.statusChecksForRef(ctx, pr.GetHead().GetSHA()),
	}
}

func mergeableFromPullRequest(pr *github.PullRequest) string {
	if pr.Mergeable == nil {
		return "UNKNOWN"
	}
	if pr.GetMergeable() {
		return "MERGEABLE"
	}
	return "CONFLICTING"
}

func mergeStateStatusFromPullRequest(pr *github.PullRequest) string {
	state := strings.ToUpper(strings.TrimSpace(pr.GetMergeableState()))
	if state == "" {
		return "UNKNOWN"
	}
	return state
}

func (c *goGitHubClient) pullRequestReviewDecision(ctx context.Context, prNumber int) string {
	reviews := []*github.PullRequestReview{}
	options := &github.ListOptions{PerPage: 100}
	for {
		page, response, err := c.client.PullRequests.ListReviews(ctx, c.owner, c.repo, prNumber, options)
		if err != nil {
			return ""
		}
		reviews = append(reviews, page...)
		if response == nil || response.NextPage == 0 {
			break
		}
		options.Page = response.NextPage
	}
	return reviewDecisionFromReviews(reviews)
}

func reviewDecisionFromReviews(reviews []*github.PullRequestReview) string {
	latestByReviewer := map[string]string{}
	for _, review := range reviews {
		login := strings.TrimSpace(review.GetUser().GetLogin())
		if login == "" {
			continue
		}
		latestByReviewer[login] = strings.ToUpper(strings.TrimSpace(review.GetState()))
	}
	hasApproval := false
	for _, state := range latestByReviewer {
		if state == "CHANGES_REQUESTED" {
			return "CHANGES_REQUESTED"
		}
		if state == "APPROVED" {
			hasApproval = true
		}
	}
	if hasApproval {
		return "APPROVED"
	}
	return ""
}

func (c *goGitHubClient) statusChecksForRef(ctx context.Context, ref string) []statusCheck {
	checks := []statusCheck{}
	combinedStatus, _, err := c.client.Repositories.GetCombinedStatus(ctx, c.owner, c.repo, ref, &github.ListOptions{PerPage: 100})
	if err != nil {
		checks = append(checks, statusCheck{Typename: "StatusContext", Context: "GitHub commit statuses", State: "UNKNOWN"})
	} else if combinedStatus != nil {
		for _, status := range combinedStatus.Statuses {
			checks = append(checks, statusCheck{Typename: "StatusContext", Context: status.GetContext(), State: strings.ToUpper(status.GetState())})
		}
	}
	checkRuns, _, err := c.client.Checks.ListCheckRunsForRef(ctx, c.owner, c.repo, ref, &github.ListCheckRunsOptions{ListOptions: github.ListOptions{PerPage: 100}})
	if err != nil {
		checks = append(checks, statusCheck{Typename: "CheckRun", Name: "GitHub check runs", Status: "UNKNOWN", Conclusion: "UNKNOWN"})
		return checks
	}
	if checkRuns != nil {
		for _, checkRun := range checkRuns.CheckRuns {
			checks = append(checks, statusCheck{Typename: "CheckRun", Name: checkRun.GetName(), Status: strings.ToUpper(checkRun.GetStatus()), Conclusion: strings.ToUpper(checkRun.GetConclusion())})
		}
	}
	return checks
}

func (c *goGitHubClient) PullRequestState(ctx context.Context, prURL string) (string, bool, error) {
	number, err := strconv.Atoi(prNumberFromURL(prURL))
	if err != nil {
		return "", false, fmt.Errorf("invalid GitHub PR URL %q", prURL)
	}
	pr, _, err := c.client.PullRequests.Get(ctx, c.owner, c.repo, number)
	if err != nil {
		return "", false, err
	}
	state := strings.ToUpper(pr.GetState())
	if pr.GetMergedAt().Time != (time.Time{}) {
		state = "MERGED"
	}
	return state, state == "MERGED", nil
}

func (c *goGitHubClient) PullRequestFeedback(ctx context.Context, prNumber int) (prFeedback, error) {
	reviews, _, err := c.client.PullRequests.ListReviews(ctx, c.owner, c.repo, prNumber, nil)
	if err != nil {
		return prFeedback{}, err
	}
	issueComments, _, err := c.client.Issues.ListComments(ctx, c.owner, c.repo, prNumber, nil)
	if err != nil {
		return prFeedback{}, err
	}
	reviewComments, _, err := c.client.PullRequests.ListComments(ctx, c.owner, c.repo, prNumber, nil)
	if err != nil {
		return prFeedback{}, err
	}
	return mapPullRequestFeedback(reviews, issueComments, reviewComments), nil
}

func mapPullRequestFeedback(reviews []*github.PullRequestReview, issueComments []*github.IssueComment, reviewComments []*github.PullRequestComment) prFeedback {
	var feedback prFeedback
	for _, review := range reviews {
		if review == nil {
			continue
		}
		feedback.Reviews = append(feedback.Reviews, struct {
			Author struct {
				Login string `json:"login"`
			} `json:"author"`
			State       string `json:"state"`
			Body        string `json:"body"`
			SubmittedAt string `json:"submittedAt"`
		}{Author: struct {
			Login string `json:"login"`
		}{Login: review.GetUser().GetLogin()}, State: review.GetState(), Body: review.GetBody(), SubmittedAt: review.GetSubmittedAt().Format(time.RFC3339)})
	}
	for _, comment := range issueComments {
		if comment == nil {
			continue
		}
		feedback.Comments = append(feedback.Comments, struct {
			Author struct {
				Login string `json:"login"`
			} `json:"author"`
			Body      string `json:"body"`
			CreatedAt string `json:"createdAt"`
		}{Author: struct {
			Login string `json:"login"`
		}{Login: comment.GetUser().GetLogin()}, Body: comment.GetBody(), CreatedAt: comment.GetCreatedAt().Format(time.RFC3339)})
	}
	for _, comment := range reviewComments {
		if comment == nil {
			continue
		}
		feedback.ReviewComments = append(feedback.ReviewComments, struct {
			User struct {
				Login string `json:"login"`
			} `json:"user"`
			Path      string `json:"path"`
			Line      int    `json:"line"`
			Body      string `json:"body"`
			CreatedAt string `json:"created_at"`
		}{User: struct {
			Login string `json:"login"`
		}{Login: comment.GetUser().GetLogin()}, Path: comment.GetPath(), Line: comment.GetLine(), Body: comment.GetBody(), CreatedAt: comment.GetCreatedAt().Format(time.RFC3339)})
	}
	return feedback
}

func (c *goGitHubClient) IssueComments(ctx context.Context, prNumber string) ([]githubIssueComment, error) {
	number, err := strconv.Atoi(prNumber)
	if err != nil {
		return nil, fmt.Errorf("invalid GitHub PR number %q", prNumber)
	}
	comments, _, err := c.client.Issues.ListComments(ctx, c.owner, c.repo, number, nil)
	if err != nil {
		return nil, err
	}
	result := make([]githubIssueComment, 0, len(comments))
	for _, comment := range comments {
		result = append(result, githubIssueComment{ID: comment.GetID(), Body: comment.GetBody()})
	}
	return result, nil
}

func (c *goGitHubClient) UpdateIssueComment(ctx context.Context, commentID int64, body string) error {
	_, _, err := c.client.Issues.EditComment(ctx, c.owner, c.repo, commentID, &github.IssueComment{Body: github.Ptr(body)})
	return err
}

func (c *goGitHubClient) CreateIssueComment(ctx context.Context, prNumber int, body string) error {
	_, _, err := c.client.Issues.CreateComment(ctx, c.owner, c.repo, prNumber, &github.IssueComment{Body: github.Ptr(body)})
	return err
}

func (c *goGitHubClient) SquashMergePullRequest(ctx context.Context, prNumber int) error {
	result, _, err := c.client.PullRequests.Merge(ctx, c.owner, c.repo, prNumber, "", &github.PullRequestOptions{MergeMethod: "squash"})
	if err != nil {
		return err
	}
	if result == nil || !result.GetMerged() {
		return fmt.Errorf("GitHub API squash merge for PR #%d did not confirm merged=true", prNumber)
	}
	return nil
}

func (c *goGitHubClient) DeleteBranch(ctx context.Context, branch string) error {
	if !strings.HasPrefix(branch, "symphony/") || issueIdentifierFromBranch(branch) == "" || strings.Contains(branch, "..") || strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") {
		return fmt.Errorf("refusing to delete non-Symphony branch %q", branch)
	}
	_, err := c.client.Git.DeleteRef(ctx, c.owner, c.repo, "heads/"+branch)
	return err
}

func (c *goGitHubClient) PullRequestHandoffDetails(ctx context.Context, prURL string) (prHandoffDetails, error) {
	number, err := strconv.Atoi(prNumberFromURL(prURL))
	if err != nil {
		return prHandoffDetails{}, fmt.Errorf("invalid GitHub PR URL %q", prURL)
	}
	pr, _, err := c.client.PullRequests.Get(ctx, c.owner, c.repo, number)
	if err != nil {
		return prHandoffDetails{}, err
	}
	return prHandoffDetails{Number: pr.GetNumber(), URL: pr.GetHTMLURL(), BaseRefName: pr.GetBase().GetRef(), HeadRefName: pr.GetHead().GetRef(), ChangedFiles: pr.GetChangedFiles(), Additions: pr.GetAdditions(), Deletions: pr.GetDeletions()}, nil
}
