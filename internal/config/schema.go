package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Config is the normalized WORKFLOW.md configuration consumed by the runner.
type Config struct {
	Tracker   TrackerConfig
	Polling   PollingConfig
	Workspace WorkspaceConfig
	Hooks     HooksConfig
	Agent     AgentConfig
	Pi        PiConfig
	Budgets   Budget
	Compound  CompoundConfig
	GitHub    GitHubConfig
}

type TrackerConfig struct {
	Kind           string
	Endpoint       string
	APIKey         string
	ProjectSlug    string
	ActiveStates   []string
	NeedsInfoState string
	TerminalStates []string
}

type PollingConfig struct {
	Interval time.Duration
	Text     string
}

type WorkspaceConfig struct {
	Root       string
	BaseBranch string
}

type HooksConfig struct {
	Timeout      time.Duration
	TimeoutText  string
	AfterCreate  string
	BeforeRun    string
	AfterRun     string
	BeforeRemove string
}

type AgentConfig struct {
	MaxConcurrentAgents int
	MaxTurns            int
	MaxRetryBackoff     time.Duration
	MaxRetryBackoffText string
}

type PiConfig struct {
	Command       string
	ReviewCommand string
	AfterCreate   string
	BeforeRun     string
	AfterRun      string
}

type CompoundConfig struct {
	HandoffState       string
	RunningState       string
	NeedsInfoState     string
	DoneState          string
	AutoMerge          bool
	RequiredValidation []string
}

type GitHubConfig struct {
	AppSlug          string
	PRAuthorOverride string
}

// ParseConfig validates and normalizes workflow front matter while preserving
// the runner's historical defaults and environment expansion behavior.
func ParseConfig(yaml string) (Config, error) {
	trackerYAML := Section(yaml, "tracker")
	workspaceYAML := Section(yaml, "workspace")
	hooksYAML := Section(yaml, "hooks")
	agentYAML := Section(yaml, "agent")
	piYAML := Section(yaml, "pi")
	compoundYAML := Section(yaml, "compound")
	githubYAML := Section(yaml, "github")

	budgets, err := ParseBudgetValidated(yaml)
	if err != nil {
		return Config{}, err
	}

	config := Config{
		Tracker: TrackerConfig{
			Kind:           Scalar(trackerYAML, "  kind", "linear"),
			Endpoint:       Scalar(trackerYAML, "  endpoint", "https://api.linear.app/graphql"),
			APIKey:         Scalar(trackerYAML, "  api_key", ""),
			ProjectSlug:    Scalar(trackerYAML, "  project_slug", ""),
			ActiveStates:   ListUnder(trackerYAML, "active_states"),
			NeedsInfoState: Scalar(trackerYAML, "  needs_info_state", "Needs Info"),
			TerminalStates: ListUnder(trackerYAML, "terminal_states"),
		},
		Workspace: WorkspaceConfig{
			Root:       Scalar(workspaceYAML, "  root", ""),
			BaseBranch: BaseBranchFromWorkflow(yaml),
		},
		Hooks: HooksConfig{
			AfterCreate:  BlockUnder(hooksYAML, "after_create"),
			BeforeRun:    Scalar(hooksYAML, "  before_run", ""),
			AfterRun:     Scalar(hooksYAML, "  after_run", ""),
			BeforeRemove: Scalar(hooksYAML, "  before_remove", ""),
		},
		Agent: AgentConfig{
			MaxConcurrentAgents: intFromYAML(agentYAML, "max_concurrent_agents", 1),
			MaxTurns:            AgentMaxTurnsFromWorkflow(yaml),
		},
		Pi: PiConfig{
			Command:       CommandUnder(piYAML, "command", "pi --print --no-session --thinking low"),
			ReviewCommand: CommandUnder(piYAML, "review_command", ""),
			AfterCreate:   BlockUnder(piYAML, "after_create"),
			BeforeRun:     Scalar(piYAML, "  before_run", ""),
			AfterRun:      Scalar(piYAML, "  after_run", ""),
		},
		Budgets: budgets,
		Compound: CompoundConfig{
			HandoffState:       Scalar(compoundYAML, "  handoff_state", "Human Review"),
			RunningState:       Scalar(compoundYAML, "  running_state", "In Progress"),
			NeedsInfoState:     Scalar(compoundYAML, "  needs_info_state", "Needs Info"),
			DoneState:          Scalar(compoundYAML, "  done_state", "Done"),
			AutoMerge:          boolFromYAML(compoundYAML, "auto_merge", false),
			RequiredValidation: ListUnder(compoundYAML, "required_validation"),
		},
		GitHub: GitHubConfig{
			AppSlug:          Scalar(githubYAML, "  app_slug", ""),
			PRAuthorOverride: Scalar(githubYAML, "  pr_author_override", ""),
		},
	}
	if len(config.Tracker.ActiveStates) == 0 {
		config.Tracker.ActiveStates = ListUnder(yaml, "active_states")
	}

	if config.Tracker.ProjectSlug == "" {
		return Config{}, fmt.Errorf("WORKFLOW.md tracker.project_slug is required; WORKFLOW.md must configure tracker.project_slug and workspace.root")
	}
	if config.Workspace.Root == "" {
		return Config{}, fmt.Errorf("WORKFLOW.md workspace.root is required; WORKFLOW.md must configure tracker.project_slug and workspace.root")
	}
	if err := durationMS(&config.Polling.Interval, &config.Polling.Text, Section(yaml, "polling"), "interval_ms", "polling.interval_ms", 30*time.Second); err != nil {
		return Config{}, err
	}
	if err := durationMS(&config.Hooks.Timeout, &config.Hooks.TimeoutText, hooksYAML, "timeout_ms", "hooks.timeout_ms", 120*time.Second); err != nil {
		return Config{}, err
	}
	if err := durationMS(&config.Agent.MaxRetryBackoff, &config.Agent.MaxRetryBackoffText, agentYAML, "max_retry_backoff_ms", "agent.max_retry_backoff_ms", 300*time.Second); err != nil {
		return Config{}, err
	}
	return config, nil
}

// AgentMaxTurnsFromWorkflow returns the normalized agent.max_turns value used
// by the current runtime boundary. Missing, malformed, zero, or negative values
// preserve the historical single-attempt behavior by resolving to 1.
func AgentMaxTurnsFromWorkflow(yaml string) int {
	value := intFromYAML(Section(yaml, "agent"), "max_turns", 1)
	if value < 1 {
		return 1
	}
	return value
}

func durationMS(dst *time.Duration, text *string, yaml, key, path string, fallback time.Duration) error {
	value := Scalar(yaml, "  "+key, "")
	if value == "" {
		*dst, *text = fallback, fmt.Sprintf("%d", fallback.Milliseconds())
		return nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 {
		return fmt.Errorf("WORKFLOW.md %s must be a non-negative millisecond integer", path)
	}
	*dst, *text = time.Duration(parsed)*time.Millisecond, value
	return nil
}

func intFromYAML(yaml, key string, fallback int) int {
	value := Scalar(yaml, "  "+key, "")
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return fallback
	}
	return parsed
}

func boolFromYAML(yaml, key string, fallback bool) bool {
	value := strings.ToLower(Scalar(yaml, "  "+key, ""))
	if value == "" {
		return fallback
	}
	return value == "true" || value == "yes" || value == "1"
}
