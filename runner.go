package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	cfg "github.com/weskor/pi-symphony/internal/config"
)

// run dispatches the selected mode. The default remains one-shot for targeted
// validation, while --continuous runs the full pilot loop in one command.
func run() error {
	loadDotEnvLocal(".env.local")

	workflowPath := "WORKFLOW.md"
	mergeApproved := false
	repair := false
	backfillState := false
	cleanup := false
	status := false
	cleanupApply := false
	continuous := false
	maxCycles := 0
	for _, arg := range os.Args[1:] {
		switch arg {
		case "--merge-approved":
			mergeApproved = true
		case "--repair-artifacts":
			repair = true
		case "--backfill-state":
			backfillState = true
		case "--cleanup-workspaces":
			cleanup = true
		case "--status":
			status = true
		case "--apply":
			cleanupApply = true
		case "--continuous", "--daemon":
			continuous = true
		case "--once":
			// explicit default
		default:
			if value, ok := strings.CutPrefix(arg, "--cycles="); ok {
				fmt.Sscanf(value, "%d", &maxCycles)
			} else {
				workflowPath = arg
			}
		}
	}
	loadNearestDotEnvLocal(workflowPath)
	configureGitHubRepositoryFromWorkflow(workflowPath)

	wf, err := cfg.ReadWorkflow(workflowPath)
	if err != nil {
		return err
	}

	workspaceYAML := cfg.Section(wf.YAML, "workspace")
	config := runnerConfig{
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
	if config.Budget.GitHubTimeout > 0 {
		defaultGitHubCommandTimeout = config.Budget.GitHubTimeout
	}

	if config.ProjectSlug == "" || config.WorkspaceRoot == "" {
		return errors.New("WORKFLOW.md must configure tracker.project_slug and workspace.root")
	}

	if backfillState {
		summary, err := backfillStateFromArtifacts(config.WorkspaceRoot)
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

	client := linearClient{apiKey: apiKey, endpoint: cfg.Scalar(wf.YAML, "  endpoint", "https://api.linear.app/graphql")}
	if repair {
		return repairArtifacts(config.WorkspaceRoot)
	}
	if cleanup {
		doneIssues, err := client.issueIdentifiersByState(config.ProjectSlug, config.DoneState)
		if err != nil {
			return err
		}
		return cleanupWorkspaces(config.WorkspaceRoot, cleanupOptions{Apply: cleanupApply, DoneIssues: doneIssues})
	}
	if status {
		return printStatus(client, config)
	}
	if mergeApproved {
		return mergeApprovedPRs(client, config)
	}
	if continuous {
		return runContinuous(client, wf, config, maxCycles)
	}
	_, err = runOne(client, wf, config)
	return err
}
