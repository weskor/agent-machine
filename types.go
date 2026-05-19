package main

import "time"

type runRecord struct {
	IssueIdentifier          string     `json:"issue_identifier"`
	IssueID                  string     `json:"issue_id"`
	IssueTitle               string     `json:"issue_title"`
	IssueURL                 string     `json:"issue_url"`
	Workspace                string     `json:"workspace"`
	WorkspaceRoot            string     `json:"workspace_root,omitempty"`
	Branch                   string     `json:"branch,omitempty"`
	ExpectedBranch           string     `json:"expected_branch,omitempty"`
	PiCommand                string     `json:"pi_command"`
	GitHubAuth               string     `json:"github_auth,omitempty"`
	StartedAt                time.Time  `json:"started_at"`
	EndedAt                  time.Time  `json:"ended_at"`
	DurationMS               int64      `json:"duration_ms"`
	PiUsage                  *usage     `json:"pi_usage,omitempty"`
	ReviewStatus             string     `json:"review_status,omitempty"`
	ReviewFindings           string     `json:"review_findings,omitempty"`
	ReviewUsage              *usage     `json:"review_usage,omitempty"`
	PRURL                    string     `json:"pr_url,omitempty"`
	FeedbackHash             string     `json:"feedback_hash,omitempty"`
	Status                   string     `json:"status"`
	OriginalStatus           string     `json:"original_status,omitempty"`
	ManualRepair             string     `json:"manual_repair,omitempty"`
	Error                    string     `json:"error,omitempty"`
	Budget                   *runBudget `json:"budget,omitempty"`
	BudgetExceeded           string     `json:"budget_exceeded,omitempty"`
	BehaviorContractEvidence []string   `json:"behavior_contract_evidence,omitempty"`
}

type runLock struct {
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

type reviewResult struct {
	Status   string `json:"status"`
	Findings string `json:"findings,omitempty"`
	Usage    *usage `json:"usage,omitempty"`
}

type usage struct {
	Input       float64    `json:"input"`
	Output      float64    `json:"output"`
	CacheRead   float64    `json:"cacheRead"`
	CacheWrite  float64    `json:"cacheWrite"`
	TotalTokens float64    `json:"totalTokens"`
	Cost        *usageCost `json:"cost,omitempty"`
}

type usageCost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
	Total      float64 `json:"total"`
}

type runBudget struct {
	WallClock      time.Duration `json:"-"`
	WallClockText  string        `json:"wall_clock,omitempty"`
	MaxTokens      float64       `json:"max_tokens,omitempty"`
	MaxCost        float64       `json:"max_cost,omitempty"`
	CommandTimeout time.Duration `json:"-"`
	CommandText    string        `json:"command_timeout,omitempty"`
	PiTimeout      time.Duration `json:"-"`
	PiText         string        `json:"pi_timeout,omitempty"`
	ReviewTimeout  time.Duration `json:"-"`
	ReviewText     string        `json:"review_timeout,omitempty"`
	MergeTimeout   time.Duration `json:"-"`
	MergeText      string        `json:"merge_timeout,omitempty"`
	GitHubTimeout  time.Duration `json:"-"`
	GitHubText     string        `json:"github_timeout,omitempty"`
}

func (b runBudget) active() *runBudget {
	if b.WallClock > 0 || b.MaxTokens > 0 || b.MaxCost > 0 || b.CommandTimeout > 0 || b.PiTimeout > 0 || b.ReviewTimeout > 0 || b.MergeTimeout > 0 || b.GitHubTimeout > 0 {
		return &b
	}
	return nil
}

type workflow struct {
	YAML string
	Body string
}

type issue struct {
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

type workflowState struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type runnerConfig struct {
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
	Budget         runBudget
}
