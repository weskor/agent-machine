package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	defaultRuntimeProvider = "codex_cli"
	legacyPiRuntime        = "pi_cli"
	defaultCodexCommand    = "codex --ask-for-approval never exec --ignore-user-config --ignore-rules --ephemeral --sandbox workspace-write"
	defaultPiCommand       = "pi --print --no-session --thinking low"
)

// Config is the normalized WORKFLOW.md configuration consumed by the runner.
type Config struct {
	Tracker   TrackerConfig
	Polling   PollingConfig
	Workspace WorkspaceConfig
	Hooks     HooksConfig
	Agent     AgentConfig
	Runtime   RuntimeConfig
	Pi        PiConfig
	Review    ReviewConfig
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
	MaxRetryBackoff     time.Duration
	MaxRetryBackoffText string
	RuntimeProvider     string
}

type RuntimeConfig struct {
	Provider      string
	Command       string
	ReviewCommand string
}

type PiConfig struct {
	Command       string
	ReviewCommand string
	AfterCreate   string
	BeforeRun     string
	AfterRun      string
}

type ReviewConfig struct {
	Guidance string
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
	runtimeYAML := Section(yaml, "runtime")
	piYAML := Section(yaml, "pi")
	reviewYAML := Section(yaml, "review")
	compoundYAML := Section(yaml, "compound")
	githubYAML := Section(yaml, "github")

	budgets, err := ParseBudgetValidated(yaml)
	if err != nil {
		return Config{}, err
	}

	legacyPiCommand := CommandUnder(piYAML, "command", "")
	legacyPiReviewCommand := CommandUnder(piYAML, "review_command", "")
	runtimeProvider := runtimeProviderFromYAML(runtimeYAML, agentYAML, legacyPiCommand, legacyPiReviewCommand)
	runtimeCommand := CommandUnder(runtimeYAML, "command", CommandUnder(piYAML, "command", defaultRuntimeCommand(runtimeProvider)))
	runtimeReviewCommand := CommandUnder(runtimeYAML, "review_command", CommandUnder(piYAML, "review_command", ""))

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
			RuntimeProvider:     runtimeProvider,
		},
		Runtime: RuntimeConfig{
			Provider:      runtimeProvider,
			Command:       runtimeCommand,
			ReviewCommand: runtimeReviewCommand,
		},
		Pi: PiConfig{
			Command:       runtimeCommand,
			ReviewCommand: runtimeReviewCommand,
			AfterCreate:   BlockUnder(piYAML, "after_create"),
			BeforeRun:     Scalar(piYAML, "  before_run", ""),
			AfterRun:      Scalar(piYAML, "  after_run", ""),
		},
		Review: ReviewConfig{
			Guidance: BlockUnder(reviewYAML, "guidance"),
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

func runtimeProviderFromYAML(runtimeYAML, agentYAML, legacyPiCommand, legacyPiReviewCommand string) string {
	if provider := Scalar(runtimeYAML, "  provider", ""); provider != "" {
		return provider
	}
	if provider := Scalar(agentYAML, "  runtime_provider", ""); provider != "" {
		return provider
	}
	if legacyPiCommand != "" || legacyPiReviewCommand != "" {
		return legacyPiRuntime
	}
	return defaultRuntimeProvider
}

func defaultRuntimeCommand(provider string) string {
	if strings.TrimSpace(provider) == legacyPiRuntime {
		return defaultPiCommand
	}
	return defaultCodexCommand
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
