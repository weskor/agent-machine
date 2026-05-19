package main

import (
	"github.com/weskor/pi-symphony/internal/cli"
	cfg "github.com/weskor/pi-symphony/internal/config"
)

func cliDependencies() cli.Dependencies[linearClient] {
	return cli.Dependencies[linearClient]{
		ConfigureGitHubRepositoryFromWorkflow: configureGitHubRepositoryFromWorkflow,
		SetGitHubTimeout: func(budget cfg.Budget) {
			if budget.GitHubTimeout > 0 {
				defaultGitHubCommandTimeout = budget.GitHubTimeout
			}
		},
		NewLinearClient: func(apiKey, endpoint string) linearClient {
			return linearClient{apiKey: apiKey, endpoint: endpoint}
		},
		IssueIdentifiersByState: func(client linearClient, projectSlug, state string) (map[string]bool, error) {
			return client.issueIdentifiersByState(projectSlug, state)
		},
		BackfillStateFromArtifacts: func(root string) (cli.BackfillSummary, error) {
			summary, err := backfillStateFromArtifacts(root)
			return cli.BackfillSummary{
				Scanned:              summary.Scanned,
				Seeded:               summary.Seeded,
				ReconciliationNeeded: summary.ReconciliationNeeded,
				Skipped:              convertBackfillSkipped(summary.Skipped),
			}, err
		},
		RepairArtifacts: repairArtifacts,
		CleanupWorkspaces: func(root string, options cli.CleanupOptions) error {
			return cleanupWorkspaces(root, cleanupOptions{Apply: options.Apply, DoneIssues: options.DoneIssues})
		},
		PrintStatus: func(client linearClient, config cli.Config) error {
			return printStatus(client, runnerConfigFromCLI(config))
		},
		MergeApprovedPRs: func(client linearClient, config cli.Config) error {
			return mergeApprovedPRs(client, runnerConfigFromCLI(config))
		},
		RunContinuous: func(client linearClient, wf cfg.Workflow, config cli.Config, maxCycles int) error {
			return runContinuous(client, wf, runnerConfigFromCLI(config), maxCycles)
		},
		RunOne: func(client linearClient, wf cfg.Workflow, config cli.Config) error {
			_, err := runOne(client, wf, runnerConfigFromCLI(config))
			return err
		},
	}
}

func runnerConfigFromCLI(config cli.Config) runnerConfig {
	return runnerConfig{
		WorkflowPath:   config.WorkflowPath,
		ProjectSlug:    config.ProjectSlug,
		WorkspaceRoot:  config.WorkspaceRoot,
		RunningState:   config.RunningState,
		HandoffState:   config.HandoffState,
		DoneState:      config.DoneState,
		NeedsInfoState: config.NeedsInfoState,
		ReadyState:     config.ReadyState,
		BaseBranch:     config.BaseBranch,
		ActiveStates:   config.ActiveStates,
		PiCommand:      config.PiCommand,
		ReviewCommand:  config.ReviewCommand,
		AfterCreate:    config.AfterCreate,
		BeforeRun:      config.BeforeRun,
		AfterRun:       config.AfterRun,
		Budget:         config.Budget,
	}
}

func convertBackfillSkipped(skipped []backfillSkip) []cli.BackfillSkipped {
	converted := make([]cli.BackfillSkipped, 0, len(skipped))
	for _, item := range skipped {
		converted = append(converted, cli.BackfillSkipped{Workspace: item.Workspace, Reason: item.Reason})
	}
	return converted
}
