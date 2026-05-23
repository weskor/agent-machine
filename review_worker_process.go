package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/weskor/pi-symphony/internal/agentruntime"
	cfg "github.com/weskor/pi-symphony/internal/config"
	"github.com/weskor/pi-symphony/internal/state"
)

func runReviewReadyAttempt(client linearClient, wf workflow, config runnerConfig, stateStore *state.Store) (bool, error) {
	if stateStore == nil {
		return false, fmt.Errorf("SQLite state store unavailable for review worker at %s", state.DefaultDBPath(config.WorkspaceRoot))
	}
	claim, didWork, err := claimNextReviewReadyAttempt(client, wf, config, stateStore)
	if err != nil || claim == nil {
		return didWork, err
	}
	return executeReviewReadyAttempt(client, config, stateStore, *claim)
}

func claimNextReviewReadyAttempt(client linearClient, wf workflow, config runnerConfig, stateStore *state.Store) (*claimedRunAttempt, bool, error) {
	if strings.TrimSpace(config.ReviewCommand) == "" {
		log("review worker idle: review command is not configured")
		return nil, false, nil
	}
	if removed, err := cleanupStaleRunLocksWithState(stateStore, config.WorkspaceRoot, time.Now()); err != nil {
		return nil, false, err
	} else if removed > 0 {
		log("removed %d stale/dead run lock(s) before review selection", removed)
	}
	candidates, err := client.candidates(config.ProjectSlug, config.ActiveStates)
	if err != nil || len(candidates) == 0 {
		return nil, false, err
	}
	prsByIssue, err := openPRsByIssueForSelection(config)
	if err != nil {
		return nil, false, err
	}
	readiness := newReviewReadinessModule(config.WorkspaceRoot)
	for _, candidate := range orderCandidates(candidates, config.ReadyState) {
		pr := prsByIssue[candidate.Identifier]
		if pr == nil || !readiness.ShouldResume(candidate.Identifier, *pr) {
			continue
		}
		decision := reconcileCandidateForSelection(config, candidate, pr, stateStore)
		if !decision.CanRun || decision.NextAction != "run_semantic_review_after_checks_ready" {
			log("skipping review resume for %s: lifecycle=%s blockers=%s next=%s", candidate.Identifier, decision.Lifecycle, strings.Join(decision.Blockers, "; "), decision.NextAction)
			continue
		}
		workspace, err := safeWorkspacePath(config.WorkspaceRoot, candidate.Identifier)
		if err != nil {
			return nil, true, err
		}
		progressStarted := time.Now().UTC()
		writeRunProgress(config.WorkspaceRoot, runProgressForIssue(&candidate, workspace, "review_resume_selected", progressStarted))
		runtime := newPiCLIRuntime()
		if _, err := runtime.Preflight(context.Background(), agentruntime.PreflightInput{ImplementationCommand: config.PiCommand, ReviewCommand: config.ReviewCommand, MaxTurns: cfg.AgentMaxTurnsFromWorkflow(wf.YAML)}); err != nil {
			snapshot := runProgressForIssue(&candidate, workspace, "failed", progressStarted)
			snapshot.Error = err.Error()
			writeRunProgress(config.WorkspaceRoot, snapshot)
			return nil, true, err
		}
		branch, _ := currentGitBranch(workspace)
		lock, releaseLock, err := acquireRunLockWithState(stateStore, workspace, &candidate, branch, time.Now())
		if err != nil {
			if errors.Is(err, errRunLocked) {
				log("%v", err)
				return nil, false, nil
			}
			return nil, true, err
		}
		return &claimedRunAttempt{Candidate: &candidate, SelectedPR: pr, Workspace: workspace, Branch: lock.Branch, ProgressStarted: progressStarted, ReleaseLock: releaseLock}, true, nil
	}
	log("no review-ready issues")
	return nil, false, nil
}

func executeReviewReadyAttempt(client linearClient, config runnerConfig, stateStore *state.Store, claimed claimedRunAttempt) (bool, error) {
	if claimed.ReleaseLock != nil {
		defer claimed.ReleaseLock()
	}
	candidate := claimed.Candidate
	if candidate == nil || claimed.SelectedPR == nil {
		return false, nil
	}
	states, err := client.workflowStates(candidate.Team.ID)
	if err != nil {
		return true, err
	}
	githubEnv, githubAuth, err := githubAppEnvFromEnvironment()
	if err != nil {
		now := time.Now()
		writeRunRecordWithCommandState(stateStore, claimed.Workspace, runRecordFor(candidate, claimed.Workspace, config.PiCommand, "github_app_error", now, now, nil, nil, claimed.SelectedPR.URL, runAttemptStatusFailed, err.Error(), config.Budget.Active(), ""))
		return true, err
	}
	return resumeReviewReadyRun(client, stateStore, config, candidate, states, claimed.Workspace, claimed.Branch, githubEnv, githubAuth, claimed.ProgressStarted, time.Now(), claimed.SelectedPR)
}
