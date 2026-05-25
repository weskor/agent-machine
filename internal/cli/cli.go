package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	cfg "github.com/weskor/pi-symphony/internal/config"
)

type Config struct {
	ConfigPath             string
	RepositoryRemote       string
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
	ConfigureGitHubRepositoryFromConfig func(string)
	SetGitHubTimeout                    func(cfg.Budget)
	NewLinearClient                     func(apiKey, endpoint string) Client
	IssueIdentifiersByState             func(Client, string, string) (map[string]bool, error)
	BackfillStateFromArtifacts          func(string) (BackfillSummary, error)
	RepairArtifacts                     func(string) error
	RepairWorkerTask                    func(string, string) error
	CleanupWorkspaces                   func(string, CleanupOptions) error
	PrintStatus                         func(Client, Config) error
	PrintRunProgress                    func(string, string) error
	Explain                             func(Client, Config) error
	SurfaceSnapshot                     func(Config) error
	MergeApprovedPRs                    func(Client, Config) error
	RunContinuous                       func(Client, cfg.Project, Config, int) error
	RunWorker                           func(Client, cfg.Project, Config, string) error
}

type parsedArgs struct {
	configPath    string
	envFiles      []string
	mode          string
	cleanupApply  bool
	maxCycles     int
	runStatusID   string
	workerRole    string
	repairTaskKey string
}

const (
	modeMerge       = "merge-approved"
	modeRepair      = "repair-artifacts"
	modeRepairTask  = "repair-worker-task"
	modeBackfill    = "backfill-state"
	modeCleanup     = "cleanup-workspaces"
	modeStatus      = "status"
	modeRunStatus   = "run-status"
	modeExplain     = "explain"
	modeSurface     = "surface-snapshot"
	modeContinuous  = "continuous"
	modeWorker      = "worker"
	modeConfigPrint = "config-print"
	modeRemoved     = "removed"
)

// Run parses CLI args, loads local environment, reads the project, validates
// required config, and dispatches the selected mode.
func Run[Client any](args []string, deps Dependencies[Client]) error {
	parsed := parseArgs(args)
	for _, envFile := range parsed.envFiles {
		loadDotEnvLocal(envFile)
	}
	loadDotEnvLocal(filepath.Join(filepath.Dir(parsed.configPath), ".env.local"))

	proj, config, err := LoadProjectConfig(parsed.configPath)
	if err != nil {
		return err
	}
	if deps.ConfigureGitHubRepositoryFromConfig != nil {
		deps.ConfigureGitHubRepositoryFromConfig(parsed.configPath)
	}
	if deps.SetGitHubTimeout != nil {
		deps.SetGitHubTimeout(config.Budget)
	}
	if parsed.mode == modeConfigPrint {
		return printResolvedConfig(config)
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
			return errors.New("run-status requires an issue identifier, for example run-status CAG-123")
		}
		return deps.PrintRunProgress(config.WorkspaceRoot, parsed.runStatusID)
	}
	if parsed.mode == modeSurface {
		return deps.SurfaceSnapshot(config)
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
		return deps.RunContinuous(client, proj, config, parsed.maxCycles)
	case modeWorker:
		if strings.TrimSpace(parsed.workerRole) == "" {
			return errors.New("--worker requires a role, for example --worker=status")
		}
		return deps.RunWorker(client, proj, config, parsed.workerRole)
	default:
		return fmt.Errorf("unsupported CLI mode %q", parsed.mode)
	}
}

func parseArgs(args []string) parsedArgs {
	parsed := parsedArgs{configPath: cfg.DefaultConfigPath}
	modeRank := -1
	setMode := func(mode string, rank int) {
		if rank > modeRank {
			parsed.mode = mode
			modeRank = rank
		}
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "start":
			setMode(modeContinuous, 1)
		case "status":
			setMode(modeStatus, 3)
		case "run-status":
			setMode(modeRunStatus, 3)
			if i+1 < len(args) {
				parsed.runStatusID = args[i+1]
				i++
			}
		case "explain":
			setMode(modeExplain, 7)
		case "snapshot":
			setMode(modeSurface, 7)
		case "surface":
			if i+1 < len(args) && args[i+1] == "snapshot" {
				setMode(modeSurface, 7)
				i++
			}
		case "merge-approved":
			setMode(modeMerge, 2)
		case "repair-artifacts":
			setMode(modeRepair, 5)
		case "backfill-state":
			setMode(modeBackfill, 6)
		case "cleanup-workspaces":
			setMode(modeCleanup, 4)
		case "config":
			if i+1 < len(args) && args[i+1] == "print" {
				setMode(modeConfigPrint, 9)
				i++
			}
		case "worker":
			setMode(modeWorker, 8)
			if i+1 < len(args) {
				parsed.workerRole = args[i+1]
				i++
			}
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
		case "--surface-snapshot":
			setMode(modeSurface, 7)
		case "--apply":
			parsed.cleanupApply = true
		case "--continuous", "--daemon":
			setMode(modeContinuous, 1)
		case "--once":
			setMode(modeRemoved, 0)
		case "--config", "-c":
			if i+1 < len(args) {
				parsed.configPath = args[i+1]
				i++
			}
		case "--env-file":
			if i+1 < len(args) {
				parsed.envFiles = append(parsed.envFiles, args[i+1])
				i++
			}
		default:
			if value, ok := strings.CutPrefix(arg, "--cycles="); ok {
				fmt.Sscanf(value, "%d", &parsed.maxCycles)
			} else if value, ok := strings.CutPrefix(arg, "--config="); ok {
				parsed.configPath = value
			} else if value, ok := strings.CutPrefix(arg, "--env-file="); ok {
				parsed.envFiles = append(parsed.envFiles, value)
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
				parsed.configPath = arg
			}
		}
	}
	return parsed
}

func LoadProjectConfig(configPath string) (cfg.Project, Config, error) {
	proj, err := cfg.ReadProject(configPath)
	if err != nil {
		return cfg.Project{}, Config{}, err
	}

	schema, err := cfg.ParseConfig(proj.YAML)
	if err != nil {
		return cfg.Project{}, Config{}, err
	}
	configDir := filepath.Dir(configPath)

	config := Config{
		ConfigPath:       configPath,
		RepositoryRemote: schema.Repository.Remote,
		APIKey:           schema.Tracker.APIKey,
		Endpoint:         schema.Tracker.Endpoint,
		ProjectSlug:      schema.Tracker.ProjectSlug,
		WorkspaceRoot:    resolveConfigRelative(configDir, schema.Workspace.Root),
		RunningState:     schema.Workflow.RunningState,
		HandoffState:     schema.Workflow.HandoffState,
		DoneState:        schema.Workflow.DoneState,
		NeedsInfoState:   schema.Workflow.NeedsInfoState,
		ReadyState:       "Ready for Agent",
		BaseBranch:       schema.Workspace.BaseBranch,
		ActiveStates:     schema.Tracker.ActiveStates,
	}
	config.RuntimeProvider = schema.Runtime.Provider
	config.RuntimeCommand = schema.Runtime.Command
	config.PiCommand = schema.Runtime.Command
	config.ReviewCommand = schema.Runtime.ReviewCommand
	config.ReviewGuidance = schema.Review.Guidance
	config.AfterCreate = firstNonEmpty(schema.Hooks.AfterCreate, schema.Pi.AfterCreate)
	config.BeforeRun = firstNonEmpty(schema.Hooks.BeforeRun, schema.Pi.BeforeRun)
	config.AfterRun = firstNonEmpty(schema.Hooks.AfterRun, schema.Pi.AfterRun)
	config.Budget = schema.Budgets
	config.GitHubAppSlug = schema.GitHub.AppSlug
	config.GitHubPRAuthorOverride = schema.GitHub.PRAuthorOverride
	return proj, config, nil
}

func resolveConfigRelative(configDir, value string) string {
	if strings.TrimSpace(value) == "" || filepath.IsAbs(value) {
		return value
	}
	return filepath.Clean(filepath.Join(configDir, value))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func printResolvedConfig(config Config) error {
	redacted := config
	if redacted.APIKey != "" {
		redacted.APIKey = "[redacted]"
	}
	data, err := json.MarshalIndent(redacted, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}
