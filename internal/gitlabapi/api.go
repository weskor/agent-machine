package gitlabapi

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	gitlab "gitlab.com/gitlab-org/api/client-go"

	"github.com/weskor/agent-machine/internal/codehost"
)

type Client struct {
	project string
	webBase string
	client  *gitlab.Client
}

func NewClient(config codehost.Config) (codehost.Client, error) {
	project := strings.TrimSpace(config.Project)
	if project == "" {
		project = strings.TrimSpace(os.Getenv("GITLAB_PROJECT"))
	}
	if project == "" {
		return nil, fmt.Errorf("GitLab project missing: set gitlab.project, GITLAB_PROJECT, or use a GitLab repository.remote")
	}
	token := strings.TrimSpace(os.Getenv("GITLAB_TOKEN"))
	if token == "" {
		token = strings.TrimSpace(os.Getenv("GL_TOKEN"))
	}
	if token == "" {
		return nil, fmt.Errorf("GitLab API token missing: set GITLAB_TOKEN or GL_TOKEN")
	}
	endpoint := strings.TrimRight(strings.TrimSpace(firstNonEmpty(config.Endpoint, os.Getenv("GITLAB_ENDPOINT"), "https://gitlab.com")), "/")
	options := []gitlab.ClientOptionFunc{}
	if endpoint != "" {
		options = append(options, gitlab.WithBaseURL(endpoint))
	}
	client, err := gitlab.NewClient(token, options...)
	if err != nil {
		return nil, err
	}
	return &Client{project: project, webBase: gitLabWebBase(endpoint), client: client}, nil
}

func (c *Client) OpenPullRequests(ctx context.Context) ([]codehost.PullRequestSummary, error) {
	options := &gitlab.ListProjectMergeRequestsOptions{
		State:                  gitlab.Ptr("opened"),
		OrderBy:                gitlab.Ptr("updated_at"),
		Sort:                   gitlab.Ptr("desc"),
		WithMergeStatusRecheck: gitlab.Ptr(true),
		ListOptions:            gitlab.ListOptions{PerPage: 100},
	}
	var out []codehost.PullRequestSummary
	for {
		mrs, response, err := c.client.MergeRequests.ListProjectMergeRequests(c.project, options, gitlab.WithContext(ctx))
		if err != nil {
			return nil, fmt.Errorf("GitLab API open MR list failed: %w", err)
		}
		for _, mr := range mrs {
			details, err := c.mergeRequestSummary(ctx, mr.IID)
			if err != nil {
				return nil, err
			}
			out = append(out, details)
		}
		if response == nil || response.NextPage == 0 {
			break
		}
		options.Page = response.NextPage
	}
	return out, nil
}

func (c *Client) PullRequestState(ctx context.Context, prURL string) (string, bool, error) {
	number, err := mrNumberFromURL(prURL)
	if err != nil {
		return "", false, err
	}
	mr, _, err := c.client.MergeRequests.GetMergeRequest(c.project, number, &gitlab.GetMergeRequestsOptions{}, gitlab.WithContext(ctx))
	if err != nil {
		return "", false, err
	}
	state := strings.ToUpper(strings.TrimSpace(mr.State))
	if mr.MergedAt != nil || state == "MERGED" {
		state = "MERGED"
	}
	return state, state == "MERGED", nil
}

func (c *Client) PullRequestFeedback(ctx context.Context, prNumber int) (codehost.PRFeedback, error) {
	notes, err := c.mergeRequestNotes(ctx, prNumber)
	if err != nil {
		return codehost.PRFeedback{}, err
	}
	var feedback codehost.PRFeedback
	for _, note := range notes {
		if note == nil || note.System || strings.TrimSpace(note.Body) == "" {
			continue
		}
		if note.Position != nil {
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
			}{Login: note.Author.Username}, Path: firstNonEmpty(note.Position.NewPath, note.Position.OldPath), Line: firstNonZero(note.Position.NewLine, note.Position.OldLine), Body: note.Body, CreatedAt: formatTime(note.CreatedAt)})
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
		}{Login: note.Author.Username}, Body: note.Body, CreatedAt: formatTime(note.CreatedAt)})
	}
	if approvals, _, err := c.client.MergeRequestApprovals.GetConfiguration(c.project, prNumber, gitlab.WithContext(ctx)); err == nil {
		for _, approvedBy := range approvals.ApprovedBy {
			if approvedBy == nil || approvedBy.User == nil {
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
			}{Login: approvedBy.User.Username}, State: "APPROVED", Body: "", SubmittedAt: ""})
		}
	}
	return feedback, nil
}

func (c *Client) SquashMergePullRequest(ctx context.Context, prNumber int) error {
	squash := true
	removeBranch := true
	mr, _, err := c.client.MergeRequests.AcceptMergeRequest(c.project, prNumber, &gitlab.AcceptMergeRequestOptions{Squash: &squash, ShouldRemoveSourceBranch: &removeBranch}, gitlab.WithContext(ctx))
	if err != nil {
		return err
	}
	if mr == nil || (mr.MergedAt == nil && !strings.EqualFold(mr.State, "merged")) {
		return fmt.Errorf("GitLab API squash merge for MR !%d did not confirm merged state", prNumber)
	}
	return nil
}

func (c *Client) DeleteBranch(ctx context.Context, branch string) error {
	if !strings.HasPrefix(branch, "am/") || codehostIssueIdentifierFromBranch(branch) == "" || strings.Contains(branch, "..") || strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") {
		return fmt.Errorf("refusing to delete non-Agent Machine branch %q", branch)
	}
	_, err := c.client.Branches.DeleteBranch(c.project, branch, gitlab.WithContext(ctx))
	return err
}

func (c *Client) PullRequestHandoffDetails(ctx context.Context, prURL string) (codehost.PRHandoffDetails, error) {
	number, err := mrNumberFromURL(prURL)
	if err != nil {
		return codehost.PRHandoffDetails{}, err
	}
	return c.mergeRequestHandoffDetails(ctx, number)
}

func (c *Client) CreatePullRequest(ctx context.Context, title, body, head, base string) (codehost.PRHandoffDetails, error) {
	mr, _, err := c.client.MergeRequests.CreateMergeRequest(c.project, &gitlab.CreateMergeRequestOptions{
		Title:              gitlab.Ptr(title),
		Description:        gitlab.Ptr(body),
		SourceBranch:       gitlab.Ptr(head),
		TargetBranch:       gitlab.Ptr(base),
		RemoveSourceBranch: gitlab.Ptr(false),
		Squash:             gitlab.Ptr(true),
	}, gitlab.WithContext(ctx))
	if err != nil {
		return codehost.PRHandoffDetails{}, fmt.Errorf("GitLab API MR create failed: %w", err)
	}
	return c.mergeRequestHandoffDetails(ctx, mr.IID)
}

func (c *Client) UpdatePullRequest(ctx context.Context, number int, title, body, base string) (codehost.PRHandoffDetails, error) {
	mr, _, err := c.client.MergeRequests.UpdateMergeRequest(c.project, number, &gitlab.UpdateMergeRequestOptions{
		Title:        gitlab.Ptr(title),
		Description:  gitlab.Ptr(body),
		TargetBranch: gitlab.Ptr(base),
		Squash:       gitlab.Ptr(true),
	}, gitlab.WithContext(ctx))
	if err != nil {
		return codehost.PRHandoffDetails{}, fmt.Errorf("GitLab API MR update failed for !%d: %w", number, err)
	}
	return c.mergeRequestHandoffDetails(ctx, mr.IID)
}

func (c *Client) mergeRequestSummary(ctx context.Context, iid int) (codehost.PullRequestSummary, error) {
	details, err := c.mergeRequestHandoffDetails(ctx, iid)
	if err != nil {
		return codehost.PullRequestSummary{}, err
	}
	mergeable, mergeState := mergeability(details)
	return codehost.PullRequestSummary{
		Number:            details.Number,
		URL:               details.URL,
		BaseRefName:       details.BaseRefName,
		HeadRefName:       details.HeadRefName,
		Author:            details.Author,
		Commits:           details.Commits,
		Mergeable:         mergeable,
		MergeStateStatus:  mergeState,
		ReviewDecision:    c.reviewDecision(ctx, iid),
		StatusCheckRollup: details.StatusCheckRollup,
	}, nil
}

func (c *Client) mergeRequestHandoffDetails(ctx context.Context, iid int) (codehost.PRHandoffDetails, error) {
	mr, _, err := c.client.MergeRequests.GetMergeRequest(c.project, iid, &gitlab.GetMergeRequestsOptions{}, gitlab.WithContext(ctx))
	if err != nil {
		return codehost.PRHandoffDetails{}, err
	}
	diffs, _, _ := c.client.MergeRequests.ListMergeRequestDiffs(c.project, iid, &gitlab.ListMergeRequestDiffsOptions{ListOptions: gitlab.ListOptions{PerPage: 100}}, gitlab.WithContext(ctx))
	commits, err := c.mergeRequestCommits(ctx, iid)
	if err != nil {
		return codehost.PRHandoffDetails{}, err
	}
	headSHA := mr.SHA
	if mr.DiffRefs.HeadSha != "" {
		headSHA = mr.DiffRefs.HeadSha
	}
	return codehost.PRHandoffDetails{
		Number:            int(mr.IID),
		URL:               firstNonEmpty(mr.WebURL, c.mrWebURL(mr.IID)),
		BaseRefName:       mr.TargetBranch,
		HeadRefName:       mr.SourceBranch,
		HeadSHA:           headSHA,
		Author:            codehost.PRAuthor{Login: userLogin(mr.Author)},
		Commits:           commits,
		ChangedFiles:      len(diffs),
		Additions:         diffAdditions(diffs),
		Deletions:         diffDeletions(diffs),
		StatusCheckRollup: c.statusChecks(ctx, mr),
	}, nil
}

func (c *Client) mergeRequestCommits(ctx context.Context, iid int) ([]codehost.PRCommit, error) {
	commits, _, err := c.client.MergeRequests.GetMergeRequestCommits(c.project, iid, &gitlab.GetMergeRequestCommitsOptions{}, gitlab.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("GitLab API MR !%d commits lookup failed: %w", iid, err)
	}
	out := make([]codehost.PRCommit, 0, len(commits))
	for _, commit := range commits {
		if commit == nil {
			continue
		}
		out = append(out, codehost.PRCommit{OID: commit.ID, Author: codehost.PRCommitAuthor{Name: commit.AuthorName, Email: commit.AuthorEmail}})
	}
	return out, nil
}

func (c *Client) mergeRequestNotes(ctx context.Context, iid int) ([]*gitlab.Note, error) {
	options := &gitlab.ListMergeRequestNotesOptions{ListOptions: gitlab.ListOptions{PerPage: 100}}
	var out []*gitlab.Note
	for {
		notes, response, err := c.client.Notes.ListMergeRequestNotes(c.project, iid, options, gitlab.WithContext(ctx))
		if err != nil {
			return nil, err
		}
		out = append(out, notes...)
		if response == nil || response.NextPage == 0 {
			break
		}
		options.Page = response.NextPage
	}
	return out, nil
}

func (c *Client) reviewDecision(ctx context.Context, iid int) string {
	approvals, _, err := c.client.MergeRequestApprovals.GetConfiguration(c.project, iid, gitlab.WithContext(ctx))
	if err != nil || approvals == nil {
		return ""
	}
	if approvals.ApprovalsRequired == 0 && len(approvals.ApprovedBy) == 0 {
		return ""
	}
	if approvals.ApprovalsLeft == 0 || approvals.Approved {
		return "APPROVED"
	}
	return ""
}

func (c *Client) statusChecks(ctx context.Context, mr *gitlab.MergeRequest) []codehost.StatusCheck {
	var checks []codehost.StatusCheck
	if strings.EqualFold(mr.DetailedMergeStatus, "mergeable") || strings.EqualFold(mr.DetailedMergeStatus, "can_be_merged") {
		checks = append(checks, codehost.StatusCheck{Typename: "StatusContext", Context: "GitLab merge state", State: "SUCCESS"})
	} else if mr.HasConflicts || strings.Contains(strings.ToLower(mr.DetailedMergeStatus), "conflict") {
		checks = append(checks, codehost.StatusCheck{Typename: "StatusContext", Context: "GitLab merge state", State: "FAILED"})
	}
	if mr.HeadPipeline != nil {
		checks = append(checks, statusCheckFromGitLabPipeline("GitLab head pipeline", mr.HeadPipeline.Status))
	}
	if mr.Pipeline != nil {
		checks = append(checks, statusCheckFromGitLabPipeline("GitLab pipeline", mr.Pipeline.Status))
	}
	if len(checks) == 0 && mr.SHA != "" {
		pipeline, _, err := c.client.Pipelines.GetLatestPipeline(c.project, &gitlab.GetLatestPipelineOptions{Ref: gitlab.Ptr(mr.SourceBranch)}, gitlab.WithContext(ctx))
		if err == nil && pipeline != nil {
			checks = append(checks, statusCheckFromGitLabPipeline("GitLab latest pipeline", pipeline.Status))
		}
	}
	if len(checks) == 0 {
		checks = append(checks, codehost.StatusCheck{Typename: "StatusContext", Context: "GitLab pipelines", State: "UNKNOWN"})
	}
	return checks
}

func statusCheckFromGitLabPipeline(name, status string) codehost.StatusCheck {
	status = strings.ToUpper(strings.TrimSpace(status))
	if status == "SUCCESS" {
		return codehost.StatusCheck{Typename: "StatusContext", Context: name, State: "SUCCESS"}
	}
	if status == "" {
		status = "UNKNOWN"
	}
	return codehost.StatusCheck{Typename: "StatusContext", Context: name, State: status}
}

func mergeability(details codehost.PRHandoffDetails) (string, string) {
	for _, check := range details.StatusCheckRollup {
		if strings.EqualFold(check.Context, "GitLab merge state") {
			if strings.EqualFold(check.State, "SUCCESS") {
				return "MERGEABLE", "CLEAN"
			}
			return "CONFLICTING", "DIRTY"
		}
	}
	return "MERGEABLE", "CLEAN"
}

func mrNumberFromURL(prURL string) (int, error) {
	parsed, ok := codehost.ParsePullRequestURL(prURL)
	if !ok || parsed.Provider != codehost.ProviderGitLab {
		return 0, fmt.Errorf("invalid GitLab MR URL %q", prURL)
	}
	return parsed.Number, nil
}

func (c *Client) mrWebURL(iid int) string {
	return strings.TrimRight(c.webBase, "/") + "/" + strings.Trim(c.project, "/") + "/-/merge_requests/" + strconv.Itoa(iid)
}

func userLogin(user *gitlab.BasicUser) string {
	if user == nil {
		return ""
	}
	return user.Username
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func gitLabWebBase(endpoint string) string {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	return strings.TrimSuffix(endpoint, "/api/v4")
}

func firstNonZero(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func formatTime(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.Format(time.RFC3339)
}

func codehostIssueIdentifierFromBranch(branch string) string {
	branch = strings.TrimSpace(branch)
	branch = strings.TrimPrefix(branch, "am/")
	branch = strings.TrimSuffix(branch, "-workspace")
	return strings.TrimSpace(branch)
}

func diffAdditions(diffs []*gitlab.MergeRequestDiff) int {
	additions := 0
	for _, diff := range diffs {
		for _, line := range strings.Split(diff.Diff, "\n") {
			if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
				additions++
			}
		}
	}
	return additions
}

func diffDeletions(diffs []*gitlab.MergeRequestDiff) int {
	deletions := 0
	for _, diff := range diffs {
		for _, line := range strings.Split(diff.Diff, "\n") {
			if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
				deletions++
			}
		}
	}
	return deletions
}
