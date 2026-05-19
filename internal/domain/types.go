package domain

import (
	"time"

	"github.com/weskor/pi-symphony/internal/config"
)

type Budget = config.Budget

type Workflow = config.Workflow

type RunRecord struct {
	IssueIdentifier          string    `json:"issue_identifier"`
	IssueID                  string    `json:"issue_id"`
	IssueTitle               string    `json:"issue_title"`
	IssueURL                 string    `json:"issue_url"`
	Workspace                string    `json:"workspace"`
	WorkspaceRoot            string    `json:"workspace_root,omitempty"`
	Branch                   string    `json:"branch,omitempty"`
	ExpectedBranch           string    `json:"expected_branch,omitempty"`
	PiCommand                string    `json:"pi_command"`
	GitHubAuth               string    `json:"github_auth,omitempty"`
	StartedAt                time.Time `json:"started_at"`
	EndedAt                  time.Time `json:"ended_at"`
	DurationMS               int64     `json:"duration_ms"`
	PiUsage                  *Usage    `json:"pi_usage,omitempty"`
	ReviewStatus             string    `json:"review_status,omitempty"`
	ReviewClassification     string    `json:"review_classification,omitempty"`
	ReviewFindings           string    `json:"review_findings,omitempty"`
	ReviewUsage              *Usage    `json:"review_usage,omitempty"`
	PRURL                    string    `json:"pr_url,omitempty"`
	FeedbackHash             string    `json:"feedback_hash,omitempty"`
	Status                   string    `json:"status"`
	OriginalStatus           string    `json:"original_status,omitempty"`
	ManualRepair             string    `json:"manual_repair,omitempty"`
	Error                    string    `json:"error,omitempty"`
	Budget                   *Budget   `json:"budget,omitempty"`
	BudgetExceeded           string    `json:"budget_exceeded,omitempty"`
	BehaviorContractEvidence []string  `json:"behavior_contract_evidence,omitempty"`
}

type RunLock struct {
	Owner           string    `json:"owner"`
	PID             int       `json:"pid"`
	Host            string    `json:"host"`
	IssueIdentifier string    `json:"issue_identifier"`
	IssueID         string    `json:"issue_id"`
	Branch          string    `json:"branch,omitempty"`
	Workspace       string    `json:"workspace"`
	StartedAt       time.Time `json:"started_at"`
	HeartbeatAt     time.Time `json:"heartbeat_at"`
}

type ReviewResult struct {
	Status         string `json:"status"`
	Classification string `json:"classification,omitempty"`
	Findings       string `json:"findings,omitempty"`
	Usage          *Usage `json:"usage,omitempty"`
}

type Usage struct {
	Input       float64    `json:"input"`
	Output      float64    `json:"output"`
	CacheRead   float64    `json:"cacheRead"`
	CacheWrite  float64    `json:"cacheWrite"`
	TotalTokens float64    `json:"totalTokens"`
	Cost        *UsageCost `json:"cost,omitempty"`
}

func (u Usage) TotalCost() float64 {
	if u.Cost == nil {
		return 0
	}
	return u.Cost.Total
}

type UsageCost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
	Total      float64 `json:"total"`
}

type Issue struct {
	ID          string `json:"id"`
	Identifier  string `json:"identifier"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
	Priority    int    `json:"priority"`
	CreatedAt   string `json:"createdAt"`
	Team        struct {
		ID   string `json:"id"`
		Key  string `json:"key"`
		Name string `json:"name"`
	} `json:"team"`
	State struct {
		Name string `json:"name"`
	} `json:"state"`
	Labels struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
}

type WorkflowState struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type RunnerConfig struct {
	WorkflowPath   string
	ProjectSlug    string
	WorkspaceRoot  string
	RunningState   string
	HandoffState   string
	DoneState      string
	NeedsInfoState string
	ReadyState     string
	BaseBranch     string
	ActiveStates   []string
	PiCommand      string
	ReviewCommand  string
	AfterCreate    string
	BeforeRun      string
	AfterRun       string
	Budget         Budget
}
