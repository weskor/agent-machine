package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/weskor/agent-machine/internal/agentruntime"
	backfillstate "github.com/weskor/agent-machine/internal/backfill"
	"github.com/weskor/agent-machine/internal/cli"
	cfg "github.com/weskor/agent-machine/internal/config"
	"github.com/weskor/agent-machine/internal/doctor"
	"github.com/weskor/agent-machine/internal/orchestrator"
	"github.com/weskor/agent-machine/internal/runtimeadapter"
	"github.com/weskor/agent-machine/internal/workertask"
)

func cliDependencies() cli.Dependencies[linearClient] {
	return orchestratorRunner().CLIDependencies()
}

func orchestratorRunner() orchestrator.Runner[linearClient, runnerConfig] {
	setup := orchestrator.SetupDependencies[linearClient]{
		ConfigureGitHubRepositoryFromConfig: configureGitHubRepositoryFromConfig,
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
			summary, err := backfillstate.StateFromArtifacts(root, artifactManager(), stateProjectionCore())
			return cli.BackfillSummary{
				Scanned:              summary.Scanned,
				Seeded:               summary.Seeded,
				ReconciliationNeeded: summary.ReconciliationNeeded,
				Skipped:              convertBackfillSkipped(summary.Skipped),
			}, err
		},
		RepairFunc: repairArtifacts,
		RepairTaskFunc: func(root, taskKey string) error {
			return repairWorkerTaskReconciliation(root, taskKey)
		},
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
		RunLedgerFunc: func(workspaceRoot, issueIdentifier string) error {
			return printRunLedger(workspaceRoot, issueIdentifier)
		},
		ExplainFunc: func(client linearClient, config runnerConfig) error {
			return printExplain(client, config)
		},
		SnapshotFunc: func(config runnerConfig) error {
			return printSurfaceSnapshot(config)
		},
		TUIFunc: func(config runnerConfig) error {
			return launchTUI(config)
		},
		DoctorFunc: func(config runnerConfig) error {
			return printDoctor(config)
		},
		MergeFunc: func(client linearClient, config runnerConfig) error {
			return mergeApprovedPRs(client, config)
		},
		ContinuousFunc: func(client linearClient, proj cfg.Project, config runnerConfig, maxCycles int) error {
			return runContinuous(client, proj, config, maxCycles)
		},
		WorkerFunc: func(client linearClient, proj cfg.Project, config runnerConfig, role string) error {
			return runSelectedWorker(client, proj, config, role)
		},
	}
	return orchestrator.NewRunner(setup, modes, runnerConfigFromCLI)
}

func repairWorkerTaskReconciliation(workspaceRoot, taskKey string) error {
	taskKey = strings.TrimSpace(taskKey)
	if taskKey == "" {
		return fmt.Errorf("repair worker task: task key is required")
	}
	store, stateDBPath := commandScopedStateStore(context.Background(), workspaceRoot, "repair-worker-task")
	if store == nil {
		return fmt.Errorf("SQLite state store unavailable for worker task repair at %s", stateDBPath)
	}
	defer store.Close()
	task, err := workertask.RequeueReconciliationNeeded(context.Background(), store, taskKey, time.Now().UTC())
	if err != nil {
		return err
	}
	log("requeued reconciliation-needed worker task %s role=%s issue=%s", task.TaskKey, task.Role, emptyAsNA(task.IssueKey))
	return nil
}

func runnerConfigFromCLI(config cli.Config) runnerConfig {
	return runnerConfig{
		ConfigPath:             config.ConfigPath,
		RepositoryRemote:       config.RepositoryRemote,
		RepositoryProvider:     config.RepositoryProvider,
		APIKey:                 config.APIKey,
		ProjectSlug:            config.ProjectSlug,
		WorkspaceRoot:          config.WorkspaceRoot,
		RunningState:           config.RunningState,
		HandoffState:           config.HandoffState,
		DoneState:              config.DoneState,
		NeedsInfoState:         config.NeedsInfoState,
		ReadyState:             config.ReadyState,
		BaseBranch:             config.BaseBranch,
		ActiveStates:           config.ActiveStates,
		RuntimeProvider:        config.RuntimeProvider,
		RuntimeCommand:         config.RuntimeCommand,
		PiCommand:              config.PiCommand,
		ReviewCommand:          config.ReviewCommand,
		ReviewGuidance:         config.ReviewGuidance,
		PromptPath:             config.PromptPath,
		AfterCreate:            config.AfterCreate,
		BeforeRun:              config.BeforeRun,
		AfterRun:               config.AfterRun,
		Budget:                 config.Budget,
		GitHubAppSlug:          config.GitHubAppSlug,
		GitHubPRAuthorOverride: config.GitHubPRAuthorOverride,
		GitLabEndpoint:         config.GitLabEndpoint,
		GitLabProject:          config.GitLabProject,
		GitLabPRAuthorOverride: config.GitLabPRAuthorOverride,
	}
}

func launchTUI(config runnerConfig) error {
	configPath, err := filepath.Abs(config.ConfigPath)
	if err != nil {
		return fmt.Errorf("launch TUI: resolve config path: %w", err)
	}
	if tuiBin, ok := findTUIBinary(); ok {
		return runTUICommand(tuiBin, []string{"--config", configPath}, "")
	}
	tuiDir, err := findTUIDir()
	if err != nil {
		return err
	}
	if _, err := exec.LookPath("bun"); err != nil {
		return fmt.Errorf("launch TUI: no compiled agent-machine-tui binary found and bun is unavailable; put a compiled TUI helper beside am, set AM_TUI_BIN, install Bun for source checkouts, or run `go run . status --config %s`: %w", configPath, err)
	}
	return runTUICommand("bun", []string{"run", "start", "--", "--config", configPath}, tuiDir)
}

func runTUICommand(name string, args []string, dir string) error {
	command := exec.Command(name, args...)
	command.Dir = dir
	command.Env = tuiEnvironment(os.Environ())
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}

func tuiEnvironment(env []string) []string {
	if envHasKey(env, "AM_BIN") {
		return env
	}
	executable, err := os.Executable()
	if err != nil || strings.TrimSpace(executable) == "" {
		return env
	}
	return append(env, "AM_BIN="+executable)
}

func envHasKey(env []string, key string) bool {
	prefix := key + "="
	for _, value := range env {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func findTUIBinary() (string, bool) {
	for _, candidate := range tuiBinaryCandidates() {
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate, true
		}
	}
	return "", false
}

func tuiBinaryCandidates() []string {
	cwd, _ := os.Getwd()
	executable, _ := os.Executable()
	return tuiBinaryCandidatePaths(cwd, executable, os.Getenv("AM_TUI_BIN"))
}

func tuiBinaryCandidatePaths(cwd, executable, override string) []string {
	name := tuiExecutableName()
	distName := tuiDistExecutableName()
	var candidates []string
	if strings.TrimSpace(override) != "" {
		candidates = append(candidates, override)
	}
	if strings.TrimSpace(executable) != "" {
		candidates = append(candidates, filepath.Join(filepath.Dir(executable), name))
	}
	if strings.TrimSpace(cwd) != "" {
		candidates = append(candidates,
			filepath.Join(cwd, name),
			filepath.Join(cwd, "bin", name),
			filepath.Join(cwd, "dist", "tui", distName),
			filepath.Join(cwd, "tui", "dist", name),
		)
	}
	return candidates
}

func tuiExecutableName() string {
	if runtime.GOOS == "windows" {
		return "agent-machine-tui.exe"
	}
	return "agent-machine-tui"
}

func tuiDistExecutableName() string {
	name := fmt.Sprintf("agent-machine-tui_%s_%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

func findTUIDir() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	candidates := []string{
		filepath.Join(cwd, "tui"),
		filepath.Join(filepath.Dir(cwd), "tui"),
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(filepath.Join(candidate, "package.json")); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("launch TUI: could not find tui/package.json from %s", cwd)
}

func printDoctor(config runnerConfig) error {
	report := doctor.Evaluate(context.Background(), doctor.Config{
		ConfigPath:         config.ConfigPath,
		ProjectSlug:        config.ProjectSlug,
		LinearAPIKey:       config.APIKey,
		RepositoryProvider: config.RepositoryProvider,
		RepositoryRemote:   config.RepositoryRemote,
		WorkspaceRoot:      config.WorkspaceRoot,
		PromptPath:         config.PromptPath,
		RuntimeProvider:    config.RuntimeProvider,
		RuntimeCommand:     config.RuntimeImplementationCommand(),
		ReviewCommand:      config.ReviewCommand,
		GitLabProject:      config.GitLabProject,
	}, func(provider string) (doctor.Runtime, error) {
		return newAgentRuntime(provider)
	}, os.LookupEnv)
	if err := report.Write(os.Stdout); err != nil {
		return err
	}
	return report.Err()
}

func convertBackfillSkipped(skipped []backfillstate.Skip) []cli.BackfillSkipped {
	converted := make([]cli.BackfillSkipped, 0, len(skipped))
	for _, item := range skipped {
		converted = append(converted, cli.BackfillSkipped{Workspace: item.Workspace, Reason: item.Reason})
	}
	return converted
}

func parseUsage(output string) *usage {
	return runtimeadapter.ParseUsage(output)
}

func newAgentRuntime(provider string) (agentruntime.AgentRuntime, error) {
	return runtimeadapter.New(provider, runtimeadapter.Dependencies{
		CurrentRepository:  currentGitHubRepo,
		NeedsInfoQuestions: needsInfoQuestionsToRuntime,
		Logf:               log,
	})
}

func recordRuntimeUsage(record runRecord) *usage {
	return runtimeadapter.RecordRuntimeUsage(record)
}

func usageFromRuntime(u *agentruntime.AttemptUsage) *usage {
	return runtimeadapter.UsageFromRuntime(u)
}

func reviewResultFromRuntime(result agentruntime.ReviewResult) *reviewResult {
	return runtimeadapter.ReviewResultFromRuntime(result)
}

func firstPRURL(output string) string {
	return runtimeadapter.FirstPRURL(output, currentGitHubRepo)
}

func firstPRURLFromClaudeOutput(output string) string {
	return runtimeadapter.FirstPRURLFromClaudeOutput(output, currentGitHubRepo)
}

func claudeNeedsInfoQuestionsToRuntime(output string) []string {
	return runtimeadapter.ClaudeNeedsInfoQuestions(output, needsInfoQuestionsToRuntime)
}

func claudeReviewStatus(output string) string {
	return runtimeadapter.ClaudeReviewStatus(output)
}

func claudeReviewClassification(status, output string) string {
	return runtimeadapter.ClaudeReviewClassification(status, output)
}

func needsInfoQuestionsToRuntime(output string) []string {
	needsInfo := parseNeedsInfo(output)
	if !needsInfo.NeedsInfo {
		return nil
	}
	return needsInfo.Questions
}

func usageSummary(u *usage) string {
	return runtimeadapter.UsageSummary(u)
}

func assistantText(output string) string {
	return runtimeadapter.AssistantText(output)
}

func reviewSummary(r *reviewResult) string {
	return runtimeadapter.ReviewSummary(r)
}
