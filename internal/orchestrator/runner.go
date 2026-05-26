package orchestrator

import (
	"github.com/weskor/agent-machine/internal/cli"
	cfg "github.com/weskor/agent-machine/internal/config"
)

// SetupDependencies are CLI-owned wiring hooks used before a mode enters the
// runner facade. They keep parsing, environment loading, project loading, and
// client construction separate from orchestration execution.
type SetupDependencies[Client any] struct {
	ConfigureGitHubRepositoryFromConfig func(string)
	SetGitHubTimeout                    func(cfg.Budget)
	NewLinearClient                     func(apiKey, endpoint string) Client
	IssueIdentifiersByState             func(Client, string, string) (map[string]bool, error)
}

// ModeRunner is the policy-preserving runner interface exposed to product
// surfaces. Callers enter by mode instead of wiring individual runner modules.
type ModeRunner[Client any, Config any] interface {
	Backfill(string) (cli.BackfillSummary, error)
	Repair(string) error
	RepairWorkerTask(string, string) error
	Cleanup(string, cli.CleanupOptions) error
	Status(Client, Config) error
	RunStatus(string, string) error
	RunLedger(string, string) error
	Explain(Client, Config) error
	SurfaceSnapshot(Config) error
	Doctor(Config) error
	Merge(Client, Config) error
	Continuous(Client, cfg.Project, Config, int) error
	Worker(Client, cfg.Project, Config, string) error
}

// ModeOperationFuncs adapts the existing root runner functions to ModeRunner.
// Its callbacks are mode-level operations, not the runner's internal dependency
// graph, so future product surfaces can reuse the same orchestration policy.
type ModeOperationFuncs[Client any, Config any] struct {
	BackfillFunc   func(string) (cli.BackfillSummary, error)
	RepairFunc     func(string) error
	RepairTaskFunc func(string, string) error
	CleanupFunc    func(string, cli.CleanupOptions) error
	StatusFunc     func(Client, Config) error
	RunStatusFunc  func(string, string) error
	RunLedgerFunc  func(string, string) error
	ExplainFunc    func(Client, Config) error
	SnapshotFunc   func(Config) error
	DoctorFunc     func(Config) error
	MergeFunc      func(Client, Config) error
	ContinuousFunc func(Client, cfg.Project, Config, int) error
	WorkerFunc     func(Client, cfg.Project, Config, string) error
}

func (m ModeOperationFuncs[Client, Config]) Backfill(root string) (cli.BackfillSummary, error) {
	return m.BackfillFunc(root)
}

func (m ModeOperationFuncs[Client, Config]) Repair(root string) error { return m.RepairFunc(root) }

func (m ModeOperationFuncs[Client, Config]) RepairWorkerTask(root, taskKey string) error {
	return m.RepairTaskFunc(root, taskKey)
}

func (m ModeOperationFuncs[Client, Config]) Cleanup(root string, options cli.CleanupOptions) error {
	return m.CleanupFunc(root, options)
}

func (m ModeOperationFuncs[Client, Config]) Status(client Client, config Config) error {
	return m.StatusFunc(client, config)
}

func (m ModeOperationFuncs[Client, Config]) RunStatus(workspaceRoot, issueIdentifier string) error {
	return m.RunStatusFunc(workspaceRoot, issueIdentifier)
}

func (m ModeOperationFuncs[Client, Config]) RunLedger(workspaceRoot, issueIdentifier string) error {
	return m.RunLedgerFunc(workspaceRoot, issueIdentifier)
}

func (m ModeOperationFuncs[Client, Config]) Explain(client Client, config Config) error {
	return m.ExplainFunc(client, config)
}

func (m ModeOperationFuncs[Client, Config]) SurfaceSnapshot(config Config) error {
	return m.SnapshotFunc(config)
}

func (m ModeOperationFuncs[Client, Config]) Doctor(config Config) error {
	return m.DoctorFunc(config)
}

func (m ModeOperationFuncs[Client, Config]) Merge(client Client, config Config) error {
	return m.MergeFunc(client, config)
}

func (m ModeOperationFuncs[Client, Config]) Continuous(client Client, proj cfg.Project, config Config, maxCycles int) error {
	return m.ContinuousFunc(client, proj, config, maxCycles)
}

func (m ModeOperationFuncs[Client, Config]) Worker(client Client, proj cfg.Project, config Config, role string) error {
	return m.WorkerFunc(client, proj, config, role)
}

// Runner is the top-level orchestration facade. It composes extracted runner
// modules behind mode-level methods rather than exposing a mutable callback bag.
type Runner[Client any, Config any] interface {
	CLIDependencies() cli.Dependencies[Client]
}

type runner[Client any, Config any] struct {
	setup         SetupDependencies[Client]
	modes         ModeRunner[Client, Config]
	fromCLIConfig func(cli.Config) Config
}

func NewRunner[Client any, Config any](setup SetupDependencies[Client], modes ModeRunner[Client, Config], fromCLIConfig func(cli.Config) Config) Runner[Client, Config] {
	return runner[Client, Config]{setup: setup, modes: modes, fromCLIConfig: fromCLIConfig}
}

// CLIDependencies adapts the orchestration facade to the existing CLI Module.
// The CLI remains responsible for parsing, environment loading, project
// loading, validation, and mode dispatch; the facade owns the composed runner
// operations behind those modes.
func (r runner[Client, Config]) CLIDependencies() cli.Dependencies[Client] {
	return cli.Dependencies[Client]{
		ConfigureGitHubRepositoryFromConfig: r.setup.ConfigureGitHubRepositoryFromConfig,
		SetGitHubTimeout:                    r.setup.SetGitHubTimeout,
		NewLinearClient:                     r.setup.NewLinearClient,
		IssueIdentifiersByState:             r.setup.IssueIdentifiersByState,
		BackfillStateFromArtifacts:          r.modes.Backfill,
		RepairArtifacts:                     r.modes.Repair,
		RepairWorkerTask:                    r.modes.RepairWorkerTask,
		CleanupWorkspaces:                   r.modes.Cleanup,
		PrintStatus: func(client Client, config cli.Config) error {
			return r.modes.Status(client, r.fromCLIConfig(config))
		},
		PrintRunProgress: r.modes.RunStatus,
		PrintRunLedger:   r.modes.RunLedger,
		Explain: func(client Client, config cli.Config) error {
			return r.modes.Explain(client, r.fromCLIConfig(config))
		},
		SurfaceSnapshot: func(config cli.Config) error {
			return r.modes.SurfaceSnapshot(r.fromCLIConfig(config))
		},
		Doctor: func(config cli.Config) error {
			return r.modes.Doctor(r.fromCLIConfig(config))
		},
		MergeApprovedPRs: func(client Client, config cli.Config) error {
			return r.modes.Merge(client, r.fromCLIConfig(config))
		},
		RunContinuous: func(client Client, proj cfg.Project, config cli.Config, maxCycles int) error {
			return r.modes.Continuous(client, proj, r.fromCLIConfig(config), maxCycles)
		},
		RunWorker: func(client Client, proj cfg.Project, config cli.Config, role string) error {
			return r.modes.Worker(client, proj, r.fromCLIConfig(config), role)
		},
	}
}
