package orchestrator

import (
	"github.com/weskor/pi-symphony/internal/cli"
	cfg "github.com/weskor/pi-symphony/internal/config"
)

// Runner is the top-level orchestration facade. It composes the runner's
// extracted modules behind one high-level Interface so callers can enter
// through policy-preserving modes instead of wiring individual modules.
type Runner[Client any, Config any] struct {
	ConfigureGitHubRepositoryFromWorkflow func(string)
	SetGitHubTimeout                      func(cfg.Budget)
	NewLinearClient                       func(apiKey, endpoint string) Client
	IssueIdentifiersByState               func(Client, string, string) (map[string]bool, error)
	BackfillStateFromArtifacts            func(string) (cli.BackfillSummary, error)
	RepairArtifacts                       func(string) error
	CleanupWorkspaces                     func(string, cli.CleanupOptions) error
	PrintStatus                           func(Client, Config) error
	PrintRunProgress                      func(string, string) error
	Explain                               func(Client, Config) error
	MergeApprovedPRs                      func(Client, Config) error
	RunContinuous                         func(Client, cfg.Workflow, Config, int) error
	RunOne                                func(Client, cfg.Workflow, Config) (bool, error)
	FromCLIConfig                         func(cli.Config) Config
}

// CLIDependencies adapts the orchestration facade to the existing CLI Module.
// The CLI remains responsible for parsing, environment loading, workflow
// loading, validation, and mode dispatch; the facade owns the composed runner
// operations behind those modes.
func (r Runner[Client, Config]) CLIDependencies() cli.Dependencies[Client] {
	return cli.Dependencies[Client]{
		ConfigureGitHubRepositoryFromWorkflow: r.ConfigureGitHubRepositoryFromWorkflow,
		SetGitHubTimeout:                      r.SetGitHubTimeout,
		NewLinearClient:                       r.NewLinearClient,
		IssueIdentifiersByState:               r.IssueIdentifiersByState,
		BackfillStateFromArtifacts:            r.BackfillStateFromArtifacts,
		RepairArtifacts:                       r.RepairArtifacts,
		CleanupWorkspaces:                     r.CleanupWorkspaces,
		PrintStatus: func(client Client, config cli.Config) error {
			return r.PrintStatus(client, r.FromCLIConfig(config))
		},
		PrintRunProgress: r.PrintRunProgress,
		Explain: func(client Client, config cli.Config) error {
			return r.Explain(client, r.FromCLIConfig(config))
		},
		MergeApprovedPRs: func(client Client, config cli.Config) error {
			return r.MergeApprovedPRs(client, r.FromCLIConfig(config))
		},
		RunContinuous: func(client Client, wf cfg.Workflow, config cli.Config, maxCycles int) error {
			return r.RunContinuous(client, wf, r.FromCLIConfig(config), maxCycles)
		},
		RunOne: func(client Client, wf cfg.Workflow, config cli.Config) error {
			_, err := r.RunOne(client, wf, r.FromCLIConfig(config))
			return err
		},
	}
}
