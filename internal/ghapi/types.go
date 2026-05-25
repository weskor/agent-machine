package ghapi

import "strings"

type PullRequestSummary struct {
	Number            int           `json:"number"`
	URL               string        `json:"url"`
	BaseRefName       string        `json:"baseRefName"`
	HeadRefName       string        `json:"headRefName"`
	Author            PRAuthor      `json:"author"`
	Commits           []PRCommit    `json:"commits,omitempty"`
	Mergeable         string        `json:"mergeable"`
	MergeStateStatus  string        `json:"mergeStateStatus"`
	ReviewDecision    string        `json:"reviewDecision"`
	StatusCheckRollup []StatusCheck `json:"statusCheckRollup"`
}

type PRAuthor struct {
	Login string `json:"login"`
}

type PRCommit struct {
	OID    string         `json:"oid,omitempty"`
	Author PRCommitAuthor `json:"author"`
}

type PRCommitAuthor struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	Login string `json:"login,omitempty"`
}

func (pr PullRequestSummary) AuthorLogin() string {
	return strings.TrimSpace(pr.Author.Login)
}

type StatusCheck struct {
	Typename   string `json:"__typename"`
	Conclusion string `json:"conclusion"`
	Status     string `json:"status"`
	State      string `json:"state"`
	Name       string `json:"name"`
	Context    string `json:"context"`
}

type PRFeedback struct {
	Reviews []struct {
		Author struct {
			Login string `json:"login"`
		} `json:"author"`
		State       string `json:"state"`
		Body        string `json:"body"`
		SubmittedAt string `json:"submittedAt"`
	} `json:"reviews"`
	Comments []struct {
		Author struct {
			Login string `json:"login"`
		} `json:"author"`
		Body      string `json:"body"`
		CreatedAt string `json:"createdAt"`
	} `json:"comments"`
	ReviewComments []struct {
		User struct {
			Login string `json:"login"`
		} `json:"user"`
		Path      string `json:"path"`
		Line      int    `json:"line"`
		Body      string `json:"body"`
		CreatedAt string `json:"created_at"`
	} `json:"review_comments"`
}

type IssueComment struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
}

type PRHandoffDetails struct {
	Number            int           `json:"number"`
	URL               string        `json:"url"`
	BaseRefName       string        `json:"baseRefName"`
	HeadRefName       string        `json:"headRefName"`
	HeadSHA           string        `json:"headSha,omitempty"`
	Author            PRAuthor      `json:"author"`
	Commits           []PRCommit    `json:"commits,omitempty"`
	ChangedFiles      int           `json:"changedFiles"`
	Additions         int           `json:"additions"`
	Deletions         int           `json:"deletions"`
	StatusCheckRollup []StatusCheck `json:"statusCheckRollup,omitempty"`
}

func (pr PRHandoffDetails) AuthorLogin() string {
	return strings.TrimSpace(pr.Author.Login)
}
