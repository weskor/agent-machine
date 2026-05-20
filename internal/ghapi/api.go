package ghapi

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-github/v69/github"
)

type Client interface {
	OpenPullRequests(ctx context.Context) ([]PullRequestSummary, error)
	PullRequestState(ctx context.Context, prURL string) (string, bool, error)
	PullRequestFeedback(ctx context.Context, prNumber int) (PRFeedback, error)
	IssueComments(ctx context.Context, prNumber string) ([]IssueComment, error)
	UpdateIssueComment(ctx context.Context, commentID int64, body string) error
	CreateIssueComment(ctx context.Context, prNumber int, body string) error
	SquashMergePullRequest(ctx context.Context, prNumber int) error
	DeleteBranch(ctx context.Context, branch string) error
	PullRequestHandoffDetails(ctx context.Context, prURL string) (PRHandoffDetails, error)
	CreatePullRequest(ctx context.Context, title, body, head, base string) (PRHandoffDetails, error)
	UpdatePullRequest(ctx context.Context, number int, title, body, base string) (PRHandoffDetails, error)
}

var AppEnvFromEnvironmentForAPI = AppEnvFromEnvironment

type goClient struct {
	owner  string
	repo   string
	client *github.Client
}

func NewClient() (Client, error) {
	owner, repo, err := CurrentRepository()
	if err != nil {
		return nil, err
	}
	token, err := APITokenFromEnvironment()
	if err != nil {
		return nil, err
	}
	return &goClient{owner: owner, repo: repo, client: github.NewClient(nil).WithAuthToken(token)}, nil
}

func APITokenFromEnvironment() (string, error) {
	token := strings.TrimSpace(os.Getenv("GH_TOKEN"))
	if token == "" {
		token = strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
	}
	if token != "" {
		return token, nil
	}
	githubAppEnv, _, err := AppEnvFromEnvironmentForAPI()
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

func CurrentRepository() (string, string, error) {
	if value := strings.TrimSpace(os.Getenv("GITHUB_REPOSITORY")); value != "" {
		if owner, repo, ok := ParseRepository(value); ok {
			return owner, repo, nil
		}
	}
	owner, repo, err := gitHubRepoFromDir(".")
	if err != nil {
		return "", "", fmt.Errorf("GitHub repository could not be determined from GITHUB_REPOSITORY or git remote origin: %w", err)
	}
	return owner, repo, nil
}

func ConfigureRepositoryFromWorkflow(workflowPath string) {
	owner, repo, err := gitHubRepoFromDir(filepath.Dir(workflowPath))
	if err != nil {
		return
	}
	_ = os.Setenv("GITHUB_REPOSITORY", owner+"/"+repo)
}

func gitHubRepoFromDir(dir string) (string, string, error) {
	out, err := exec.Command("git", "-C", dir, "config", "--get", "remote.origin.url").Output()
	if err != nil {
		return "", "", err
	}
	remote := strings.TrimSpace(string(out))
	if owner, repo, ok := ParseRepository(remote); ok {
		return owner, repo, nil
	}
	return "", "", fmt.Errorf("GitHub repository could not be parsed from remote origin %q", remote)
}

func ParseRepository(value string) (string, string, bool) {
	if parts := strings.Split(value, "/"); len(parts) == 2 && parts[0] != "" && parts[1] != "" && !strings.Contains(parts[0], ":") {
		return parts[0], strings.TrimSuffix(parts[1], ".git"), true
	}
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`github\.com[:/]([^/]+)/([^/.]+)(?:\.git)?$`),
		regexp.MustCompile(`github\.com/([^/]+)/([^/.]+)(?:\.git)?$`),
	}
	for _, pattern := range patterns {
		match := pattern.FindStringSubmatch(value)
		if len(match) == 3 {
			return match[1], match[2], true
		}
	}
	return "", "", false
}

func ClientWithTimeout(timeout time.Duration) (Client, context.Context, context.CancelFunc, error) {
	client, err := NewClient()
	if err != nil {
		return nil, nil, nil, err
	}
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	return client, ctx, cancel, nil
}

func PRNumberFromURL(prURL string) string {
	prURL = strings.TrimSpace(prURL)
	if prURL == "" {
		return ""
	}
	parts := strings.Split(strings.TrimRight(prURL, "/"), "/")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

func (c *goClient) OpenPullRequests(ctx context.Context) ([]PullRequestSummary, error) {
	options := &github.PullRequestListOptions{State: "open", Sort: "updated", Direction: "desc", ListOptions: github.ListOptions{PerPage: 100}}
	prs := []PullRequestSummary{}
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

func (c *goClient) pullRequestCommits(ctx context.Context, number int) ([]PRCommit, error) {
	options := &github.ListOptions{PerPage: 100}
	commits := []PRCommit{}
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
			commits = append(commits, PRCommit{
				OID: githubCommit.GetSHA(),
				Author: PRCommitAuthor{
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

func (c *goClient) openPullRequestSummary(ctx context.Context, pr *github.PullRequest) PullRequestSummary {
	mergeState := mergeStateStatusFromPullRequest(pr)
	return PullRequestSummary{
		Number:            pr.GetNumber(),
		URL:               pr.GetHTMLURL(),
		BaseRefName:       pr.GetBase().GetRef(),
		HeadRefName:       pr.GetHead().GetRef(),
		Author:            PRAuthor{Login: pr.GetUser().GetLogin()},
		Mergeable:         mergeableFromPullRequest(pr),
		MergeStateStatus:  mergeState,
		ReviewDecision:    c.pullRequestReviewDecision(ctx, pr.GetNumber()),
		StatusCheckRollup: checksWithMergeStateFallback(c.statusChecksForRef(ctx, pr.GetHead().GetSHA()), mergeState),
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

func (c *goClient) pullRequestReviewDecision(ctx context.Context, prNumber int) string {
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

func (c *goClient) statusChecksForRef(ctx context.Context, ref string) []StatusCheck {
	checks := []StatusCheck{}
	combinedStatus, _, err := c.client.Repositories.GetCombinedStatus(ctx, c.owner, c.repo, ref, &github.ListOptions{PerPage: 100})
	if err != nil {
		checks = append(checks, StatusCheck{Typename: "StatusContext", Context: "GitHub commit statuses", State: "UNKNOWN"})
	} else if combinedStatus != nil {
		for _, status := range combinedStatus.Statuses {
			checks = append(checks, StatusCheck{Typename: "StatusContext", Context: status.GetContext(), State: strings.ToUpper(status.GetState())})
		}
	}
	checkRuns, _, err := c.client.Checks.ListCheckRunsForRef(ctx, c.owner, c.repo, ref, &github.ListCheckRunsOptions{ListOptions: github.ListOptions{PerPage: 100}})
	if err != nil {
		checks = append(checks, StatusCheck{Typename: "CheckRun", Name: "GitHub check runs", Status: "UNKNOWN", Conclusion: "UNKNOWN"})
		return checks
	}
	if checkRuns != nil {
		for _, checkRun := range checkRuns.CheckRuns {
			checks = append(checks, StatusCheck{Typename: "CheckRun", Name: checkRun.GetName(), Status: strings.ToUpper(checkRun.GetStatus()), Conclusion: strings.ToUpper(checkRun.GetConclusion())})
		}
	}
	return checks
}

func (c *goClient) PullRequestState(ctx context.Context, prURL string) (string, bool, error) {
	number, err := strconv.Atoi(PRNumberFromURL(prURL))
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

func (c *goClient) PullRequestFeedback(ctx context.Context, prNumber int) (PRFeedback, error) {
	reviews, _, err := c.client.PullRequests.ListReviews(ctx, c.owner, c.repo, prNumber, nil)
	if err != nil {
		return PRFeedback{}, err
	}
	issueComments, _, err := c.client.Issues.ListComments(ctx, c.owner, c.repo, prNumber, nil)
	if err != nil {
		return PRFeedback{}, err
	}
	reviewComments, _, err := c.client.PullRequests.ListComments(ctx, c.owner, c.repo, prNumber, nil)
	if err != nil {
		return PRFeedback{}, err
	}
	return MapPullRequestFeedback(reviews, issueComments, reviewComments), nil
}

func MapPullRequestFeedback(reviews []*github.PullRequestReview, issueComments []*github.IssueComment, reviewComments []*github.PullRequestComment) PRFeedback {
	var feedback PRFeedback
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

func (c *goClient) IssueComments(ctx context.Context, prNumber string) ([]IssueComment, error) {
	number, err := strconv.Atoi(prNumber)
	if err != nil {
		return nil, fmt.Errorf("invalid GitHub PR number %q", prNumber)
	}
	comments, _, err := c.client.Issues.ListComments(ctx, c.owner, c.repo, number, nil)
	if err != nil {
		return nil, err
	}
	result := make([]IssueComment, 0, len(comments))
	for _, comment := range comments {
		result = append(result, IssueComment{ID: comment.GetID(), Body: comment.GetBody()})
	}
	return result, nil
}

func (c *goClient) UpdateIssueComment(ctx context.Context, commentID int64, body string) error {
	_, _, err := c.client.Issues.EditComment(ctx, c.owner, c.repo, commentID, &github.IssueComment{Body: github.Ptr(body)})
	return err
}

func (c *goClient) CreateIssueComment(ctx context.Context, prNumber int, body string) error {
	_, _, err := c.client.Issues.CreateComment(ctx, c.owner, c.repo, prNumber, &github.IssueComment{Body: github.Ptr(body)})
	return err
}

func (c *goClient) SquashMergePullRequest(ctx context.Context, prNumber int) error {
	result, _, err := c.client.PullRequests.Merge(ctx, c.owner, c.repo, prNumber, "", &github.PullRequestOptions{MergeMethod: "squash"})
	if err != nil {
		return err
	}
	if result == nil || !result.GetMerged() {
		return fmt.Errorf("GitHub API squash merge for PR #%d did not confirm merged=true", prNumber)
	}
	return nil
}

func (c *goClient) DeleteBranch(ctx context.Context, branch string) error {
	if !strings.HasPrefix(branch, "symphony/") || IssueIdentifierFromBranch(branch) == "" || strings.Contains(branch, "..") || strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") {
		return fmt.Errorf("refusing to delete non-Symphony branch %q", branch)
	}
	_, err := c.client.Git.DeleteRef(ctx, c.owner, c.repo, "heads/"+branch)
	return err
}

func (c *goClient) PullRequestHandoffDetails(ctx context.Context, prURL string) (PRHandoffDetails, error) {
	number, err := strconv.Atoi(PRNumberFromURL(prURL))
	if err != nil {
		return PRHandoffDetails{}, fmt.Errorf("invalid GitHub PR URL %q", prURL)
	}
	pr, _, err := c.client.PullRequests.Get(ctx, c.owner, c.repo, number)
	if err != nil {
		return PRHandoffDetails{}, err
	}
	return c.prHandoffDetails(ctx, pr), nil
}

func (c *goClient) CreatePullRequest(ctx context.Context, title, body, head, base string) (PRHandoffDetails, error) {
	pr, _, err := c.client.PullRequests.Create(ctx, c.owner, c.repo, &github.NewPullRequest{Title: github.String(title), Body: github.String(body), Head: github.String(head), Base: github.String(base)})
	if err != nil {
		return PRHandoffDetails{}, fmt.Errorf("GitHub API PR create failed: %w", err)
	}
	return c.prHandoffDetails(ctx, pr), nil
}

func (c *goClient) UpdatePullRequest(ctx context.Context, number int, title, body, base string) (PRHandoffDetails, error) {
	pr, _, err := c.client.PullRequests.Edit(ctx, c.owner, c.repo, number, &github.PullRequest{Title: github.String(title), Body: github.String(body), Base: &github.PullRequestBranch{Ref: github.String(base)}})
	if err != nil {
		return PRHandoffDetails{}, fmt.Errorf("GitHub API PR update failed for #%d: %w", number, err)
	}
	return c.prHandoffDetails(ctx, pr), nil
}

func (c *goClient) prHandoffDetails(ctx context.Context, pr *github.PullRequest) PRHandoffDetails {
	headSHA := pr.GetHead().GetSHA()
	return PRHandoffDetails{Number: pr.GetNumber(), URL: pr.GetHTMLURL(), BaseRefName: pr.GetBase().GetRef(), HeadRefName: pr.GetHead().GetRef(), HeadSHA: headSHA, ChangedFiles: pr.GetChangedFiles(), Additions: pr.GetAdditions(), Deletions: pr.GetDeletions(), StatusCheckRollup: checksWithMergeStateFallback(c.statusChecksForRef(ctx, headSHA), mergeStateStatusFromPullRequest(pr))}
}

func checksWithMergeStateFallback(checks []StatusCheck, mergeState string) []StatusCheck {
	if !strings.EqualFold(strings.TrimSpace(mergeState), "CLEAN") || !onlyCheckRunAccessUnavailable(checks) {
		return checks
	}
	return []StatusCheck{{Typename: "StatusContext", Context: "GitHub merge state", State: "SUCCESS"}}
}

func onlyCheckRunAccessUnavailable(checks []StatusCheck) bool {
	if len(checks) == 0 {
		return false
	}
	foundUnavailableCheckRun := false
	for _, check := range checks {
		if check.Typename == "CheckRun" && check.Name == "GitHub check runs" && strings.EqualFold(check.Status, "UNKNOWN") && strings.EqualFold(check.Conclusion, "UNKNOWN") {
			foundUnavailableCheckRun = true
			continue
		}
		if check.Typename == "StatusContext" && strings.EqualFold(check.State, "SUCCESS") {
			continue
		}
		return false
	}
	return foundUnavailableCheckRun
}
