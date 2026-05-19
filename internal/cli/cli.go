package cli

import (
	"errors"
	"fmt"
	"strings"

	cfg "github.com/weskor/pi-symphony/internal/config"
)

type Config struct {
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
	Budget         cfg.Budget
}

type BackfillSummary struct {
	Scanned              int
	Seeded               int
	ReconciliationNeeded int
	Skipped              []BackfillSkipped
}

type BackfillSkipped struct {
	Workspace string
	Reason    string
}

type CleanupOptions struct {
	Apply      bool
	DoneIssues map[string]bool
}

type Dependencies[Client any] struct {
	ConfigureGitHubRepositoryFromWorkflow func(string)
	SetGitHubTimeout                      func(cfg.Budget)
	NewLinearClient                       func(apiKey, endpoint string) Client
	IssueIdentifiersByState               func(Client, string, string) (map[string]bool, error)
	BackfillStateFromArtifacts            func(string) (BackfillSummary, error)
	RepairArtifacts                       func(string) error
	CleanupWorkspaces                     func(string, CleanupOptions) error
	PrintStatus                           func(Client, Config) error
	MergeApprovedPRs                      func(Client, Config) error
	RunContinuous                         func(Client, cfg.Workflow, Config, int) error
	RunOne                                func(Client, cfg.Workflow, Config) error
}

type parsedArgs struct {
	workflowPath string
	mode         string
	cleanupApply bool
	maxCycles    int
}

const (
	modeOnce       = "once"
	modeMerge      = "merge-approved"
	modeRepair     = "repair-artifacts"
	modeBackfill   = "backfill-state"
	modeCleanup    = "cleanup-workspaces"
	modeStatus     = "status"
	modeContinuous = "continuous"
)

// Run parses CLI args, loads local environment, reads the workflow, validates
// required config, and dispatches the selected mode.
func Run[Client any](args []string, deps Dependencies[Client]) error {
	parsed := parseArgs(args)
	loadDotEnvLocal(".env.local")
	loadNearestDotEnvLocal(parsed.workflowPath)
	if deps.ConfigureGitHubRepositoryFromWorkflow != nil {
		deps.ConfigureGitHubRepositoryFromWorkflow(parsed.workflowPath)
	}

	wf, config, err := LoadWorkflowConfig(parsed.workflowPath)
	if err != nil {
		return err
	}
	if deps.SetGitHubTimeout != nil {
		deps.SetGitHubTimeout(config.Budget)
	}

	if parsed.mode == modeBackfill {
		summary, err := deps.BackfillStateFromArtifacts(config.WorkspaceRoot)
		if err != nil {
			return err
		}
		fmt.Printf("backfilled SQLite state from %s: scanned=%d seeded=%d reconciliation_needed=%d skipped=%d\n", config.WorkspaceRoot, summary.Scanned, summary.Seeded, summary.ReconciliationNeeded, len(summary.Skipped))
		for _, skipped := range summary.Skipped {
			fmt.Printf("skipped %s: %s\n", skipped.Workspace, skipped.Reason)
		}
		return nil
	}

	apiKey := cfg.Scalar(wf.YAML, "  api_key", "")
	if apiKey == "" {
		return errors.New("LINEAR_API_KEY is required")
	}
	client := deps.NewLinearClient(apiKey, cfg.Scalar(wf.YAML, "  endpoint", "https://api.linear.app/graphql"))

	switch parsed.mode {
	case modeRepair:
		return deps.RepairArtifacts(config.WorkspaceRoot)
	case modeCleanup:
		doneIssues, err := deps.IssueIdentifiersByState(client, config.ProjectSlug, config.DoneState)
		if err != nil {
			return err
		}
		return deps.CleanupWorkspaces(config.WorkspaceRoot, CleanupOptions{Apply: parsed.cleanupApply, DoneIssues: doneIssues})
	case modeStatus:
		return deps.PrintStatus(client, config)
	case modeMerge:
		return deps.MergeApprovedPRs(client, config)
	case modeContinuous:
		return deps.RunContinuous(client, wf, config, parsed.maxCycles)
	default:
		return deps.RunOne(client, wf, config)
	}
}

func parseArgs(args []string) parsedArgs {
	parsed := parsedArgs{workflowPath: "WORKFLOW.md", mode: modeOnce}
	modeRank := 0
	setMode := func(mode string, rank int) {
		if rank > modeRank {
			parsed.mode = mode
			modeRank = rank
		}
	}
	for _, arg := range args {
		switch arg {
		case "--merge-approved":
			setMode(modeMerge, 2)
		case "--repair-artifacts":
			setMode(modeRepair, 5)
		case "--backfill-state":
			setMode(modeBackfill, 6)
		case "--cleanup-workspaces":
			setMode(modeCleanup, 4)
		case "--status":
			setMode(modeStatus, 3)
		case "--apply":
			parsed.cleanupApply = true
		case "--continuous", "--daemon":
			setMode(modeContinuous, 1)
		case "--once":
			// explicit default
		default:
			if value, ok := strings.CutPrefix(arg, "--cycles="); ok {
				fmt.Sscanf(value, "%d", &parsed.maxCycles)
			} else {
				parsed.workflowPath = arg
			}
		}
	}
	return parsed
}

func LoadWorkflowConfig(workflowPath string) (cfg.Workflow, Config, error) {
	wf, err := cfg.ReadWorkflow(workflowPath)
	if err != nil {
		return cfg.Workflow{}, Config{}, err
	}

	workspaceYAML := cfg.Section(wf.YAML, "workspace")
	config := Config{
		WorkflowPath:   workflowPath,
		ProjectSlug:    cfg.Scalar(wf.YAML, "  project_slug", ""),
		WorkspaceRoot:  cfg.Scalar(workspaceYAML, "  root", ""),
		RunningState:   cfg.Scalar(wf.YAML, "  running_state", "In Progress"),
		HandoffState:   cfg.Scalar(wf.YAML, "  handoff_state", "Human Review"),
		DoneState:      cfg.Scalar(wf.YAML, "  done_state", "Done"),
		NeedsInfoState: cfg.Scalar(wf.YAML, "  needs_info_state", "Needs Info"),
		ReadyState:     "Ready for Agent",
		BaseBranch:     cfg.BaseBranchFromWorkflow(wf.YAML),
		ActiveStates:   cfg.ListUnder(wf.YAML, "active_states"),
	}
	piYAML := cfg.Section(wf.YAML, "pi")
	config.PiCommand = cfg.CommandUnder(piYAML, "command", "pi --print --no-session --thinking low")
	config.ReviewCommand = cfg.CommandUnder(piYAML, "review_command", "")
	config.AfterCreate = cfg.BlockUnder(piYAML, "after_create")
	config.BeforeRun = cfg.Scalar(piYAML, "  before_run", "")
	config.AfterRun = cfg.Scalar(piYAML, "  after_run", "")
	config.Budget = cfg.ParseBudget(wf.YAML)

	if config.ProjectSlug == "" || config.WorkspaceRoot == "" {
		return cfg.Workflow{}, Config{}, errors.New("WORKFLOW.md must configure tracker.project_slug and workspace.root")
	}
	return wf, config, nil
}
