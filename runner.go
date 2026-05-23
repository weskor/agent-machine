package main

import (
	"context"

	"github.com/weskor/pi-symphony/internal/cli"
	cfg "github.com/weskor/pi-symphony/internal/config"
	"github.com/weskor/pi-symphony/internal/orchestrator"
)

func cliDependencies() cli.Dependencies[linearClient] {
	return orchestratorRunner().CLIDependencies()
}

func orchestratorRunner() orchestrator.Runner[linearClient, runnerConfig] {
	setup := orchestrator.SetupDependencies[linearClient]{
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
	}
	modes := orchestrator.ModeOperationFuncs[linearClient, runnerConfig]{
		BackfillFunc: func(root string) (cli.BackfillSummary, error) {
			summary, err := backfillStateFromArtifacts(root)
			return cli.BackfillSummary{
				Scanned:              summary.Scanned,
				Seeded:               summary.Seeded,
				ReconciliationNeeded: summary.ReconciliationNeeded,
				Skipped:              convertBackfillSkipped(summary.Skipped),
			}, err
		},
		RepairFunc: repairArtifacts,
		CleanupFunc: func(root string, options cli.CleanupOptions) error {
			store, _ := commandScopedStateStore(context.Background(), root, "cleanup")
			if store != nil {
				defer store.Close()
			}
			return cleanupWorkspaces(root, cleanupOptions{Apply: options.Apply, DoneIssues: options.DoneIssues, StateStore: store})
		},
		StatusFunc: func(client linearClient, config runnerConfig) error {
			return printStatus(client, config)
		},
		RunStatusFunc: func(workspaceRoot, issueIdentifier string) error {
			return printRunProgress(workspaceRoot, issueIdentifier)
		},
		ExplainFunc: func(client linearClient, config runnerConfig) error {
			return printExplain(client, config)
		},
		MergeFunc: func(client linearClient, config runnerConfig) error {
			return mergeApprovedPRs(client, config)
		},
		ContinuousFunc: func(client linearClient, wf cfg.Workflow, config runnerConfig, maxCycles int) error {
			return runContinuous(client, wf, config, maxCycles)
		},
		RunOneFunc: func(client linearClient, wf cfg.Workflow, config runnerConfig) (bool, error) {
			return runOne(client, wf, config)
		},
	}
	return orchestrator.NewRunner(setup, modes, runnerConfigFromCLI)
}

func runnerConfigFromCLI(config cli.Config) runnerConfig {
	return runnerConfig{
		WorkflowPath:           config.WorkflowPath,
		ProjectSlug:            config.ProjectSlug,
		WorkspaceRoot:          config.WorkspaceRoot,
		RunningState:           config.RunningState,
		HandoffState:           config.HandoffState,
		DoneState:              config.DoneState,
		NeedsInfoState:         config.NeedsInfoState,
		ReadyState:             config.ReadyState,
		BaseBranch:             config.BaseBranch,
		ActiveStates:           config.ActiveStates,
		PiCommand:              config.PiCommand,
		ReviewCommand:          config.ReviewCommand,
		ReviewGuidance:         config.ReviewGuidance,
		AfterCreate:            config.AfterCreate,
		BeforeRun:              config.BeforeRun,
		AfterRun:               config.AfterRun,
		Budget:                 config.Budget,
		GitHubAppSlug:          config.GitHubAppSlug,
		GitHubPRAuthorOverride: config.GitHubPRAuthorOverride,
	}
}

func convertBackfillSkipped(skipped []backfillSkip) []cli.BackfillSkipped {
	converted := make([]cli.BackfillSkipped, 0, len(skipped))
	for _, item := range skipped {
		converted = append(converted, cli.BackfillSkipped{Workspace: item.Workspace, Reason: item.Reason})
	}
	return converted
}
