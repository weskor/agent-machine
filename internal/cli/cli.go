package cli

import (
	"errors"
	"fmt"
	"strings"

	cfg "github.com/weskor/pi-symphony/internal/config"
)

type Config struct {
	WorkflowPath           string
	APIKey                 string
	Endpoint               string
	ProjectSlug            string
	WorkspaceRoot          string
	RunningState           string
	HandoffState           string
	DoneState              string
	NeedsInfoState         string
	ReadyState             string
	BaseBranch             string
	ActiveStates           []string
	RuntimeProvider        string
	RuntimeCommand         string
	PiCommand              string
	ReviewCommand          string
	ReviewGuidance         string
	AfterCreate            string
	BeforeRun              string
	AfterRun               string
	Budget                 cfg.Budget
	GitHubAppSlug          string
	GitHubPRAuthorOverride string
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
	RepairWorkerTask                      func(string, string) error
	CleanupWorkspaces                     func(string, CleanupOptions) error
	PrintStatus                           func(Client, Config) error
	PrintRunProgress                      func(string, string) error
	Explain                               func(Client, Config) error
	MergeApprovedPRs                      func(Client, Config) error
	RunContinuous                         func(Client, cfg.Workflow, Config, int) error
	RunWorker                             func(Client, cfg.Workflow, Config, string) error
}

type parsedArgs struct {
	workflowPath  string
	mode          string
	cleanupApply  bool
	maxCycles     int
	runStatusID   string
	workerRole    string
	repairTaskKey string
}

const (
	modeMerge      = "merge-approved"
	modeRepair     = "repair-artifacts"
	modeRepairTask = "repair-worker-task"
	modeBackfill   = "backfill-state"
	modeCleanup    = "cleanup-workspaces"
	modeStatus     = "status"
	modeRunStatus  = "run-status"
	modeExplain    = "explain"
	modeContinuous = "continuous"
	modeWorker     = "worker"
	modeRemoved    = "removed"
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
	if parsed.mode == modeRunStatus {
		if parsed.runStatusID == "" {
			return errors.New("--run-status requires an issue identifier, for example --run-status=CAG-123")
		}
		return deps.PrintRunProgress(config.WorkspaceRoot, parsed.runStatusID)
	}
	if parsed.mode == modeRepairTask {
		if strings.TrimSpace(parsed.repairTaskKey) == "" {
			return errors.New("--repair-worker-task requires a task key")
		}
		return deps.RepairWorkerTask(config.WorkspaceRoot, parsed.repairTaskKey)
	}
	if parsed.mode == "" {
		return errors.New("no CLI mode selected; use --continuous for the production loop, --status, --explain, or --worker=<role>")
	}
	if parsed.mode == modeRemoved {
		return errors.New("--once has been removed; use --continuous for production or --worker=implementation for one implementation worker process")
	}

	if config.APIKey == "" {
		return errors.New("LINEAR_API_KEY is required")
	}
	client := deps.NewLinearClient(config.APIKey, config.Endpoint)

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
	case modeExplain:
		return deps.Explain(client, config)
	case modeMerge:
		return deps.MergeApprovedPRs(client, config)
	case modeContinuous:
		return deps.RunContinuous(client, wf, config, parsed.maxCycles)
	case modeWorker:
		if strings.TrimSpace(parsed.workerRole) == "" {
			return errors.New("--worker requires a role, for example --worker=status")
		}
		return deps.RunWorker(client, wf, config, parsed.workerRole)
	default:
		return fmt.Errorf("unsupported CLI mode %q", parsed.mode)
	}
}

func parseArgs(args []string) parsedArgs {
	parsed := parsedArgs{workflowPath: "WORKFLOW.md"}
	modeRank := -1
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
		case "--run-status":
			setMode(modeRunStatus, 3)
		case "--explain", "--dry-run":
			setMode(modeExplain, 7)
		case "--apply":
			parsed.cleanupApply = true
		case "--continuous", "--daemon":
			setMode(modeContinuous, 1)
		case "--once":
			setMode(modeRemoved, 0)
		default:
			if value, ok := strings.CutPrefix(arg, "--cycles="); ok {
				fmt.Sscanf(value, "%d", &parsed.maxCycles)
			} else if value, ok := strings.CutPrefix(arg, "--run-status="); ok {
				setMode(modeRunStatus, 3)
				parsed.runStatusID = value
			} else if value, ok := strings.CutPrefix(arg, "--worker="); ok {
				setMode(modeWorker, 8)
				parsed.workerRole = value
			} else if value, ok := strings.CutPrefix(arg, "--repair-worker-task="); ok {
				setMode(modeRepairTask, 7)
				parsed.repairTaskKey = value
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

	schema, err := cfg.ParseConfig(wf.YAML)
	if err != nil {
		return cfg.Workflow{}, Config{}, err
	}

	config := Config{
		WorkflowPath:   workflowPath,
		APIKey:         schema.Tracker.APIKey,
		Endpoint:       schema.Tracker.Endpoint,
		ProjectSlug:    schema.Tracker.ProjectSlug,
		WorkspaceRoot:  schema.Workspace.Root,
		RunningState:   schema.Compound.RunningState,
		HandoffState:   schema.Compound.HandoffState,
		DoneState:      schema.Compound.DoneState,
		NeedsInfoState: schema.Compound.NeedsInfoState,
		ReadyState:     "Ready for Agent",
		BaseBranch:     schema.Workspace.BaseBranch,
		ActiveStates:   schema.Tracker.ActiveStates,
	}
	config.RuntimeProvider = schema.Runtime.Provider
	config.RuntimeCommand = schema.Runtime.Command
	config.PiCommand = schema.Runtime.Command
	config.ReviewCommand = schema.Runtime.ReviewCommand
	config.ReviewGuidance = schema.Review.Guidance
	config.AfterCreate = schema.Pi.AfterCreate
	config.BeforeRun = schema.Pi.BeforeRun
	config.AfterRun = schema.Pi.AfterRun
	config.Budget = schema.Budgets
	config.GitHubAppSlug = schema.GitHub.AppSlug
	config.GitHubPRAuthorOverride = schema.GitHub.PRAuthorOverride
	return wf, config, nil
}
