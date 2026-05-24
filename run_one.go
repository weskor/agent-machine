package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/weskor/pi-symphony/internal/agentruntime"
	"github.com/weskor/pi-symphony/internal/state"
)

type claimedRunAttempt struct {
	Candidate                   *issue
	SelectedPR                  *pullRequestSummary
	Workspace                   string
	Branch                      string
	ProgressStarted             time.Time
	ImplementationWorkerTaskKey string
	ReviewWorkerTaskKey         string
	ReleaseLock                 func()
}

func claimNextRunAttempt(client linearClient, proj project, config runnerConfig, stateStore *state.Store) (*claimedRunAttempt, bool, error) {
	return claimNextRunAttemptContext(context.Background(), client, proj, config, stateStore)
}

func claimNextRunAttemptContext(ctx context.Context, client linearClient, proj project, config runnerConfig, stateStore *state.Store) (*claimedRunAttempt, bool, error) {
	return claimNextRunAttemptWithOptionsContext(ctx, client, proj, config, stateStore, candidateSelectionOptions{})
}

func claimNextRunAttemptWithOptions(client linearClient, proj project, config runnerConfig, stateStore *state.Store, options candidateSelectionOptions) (*claimedRunAttempt, bool, error) {
	return claimNextRunAttemptWithOptionsContext(context.Background(), client, proj, config, stateStore, options)
}

func claimNextRunAttemptWithOptionsContext(ctx context.Context, client linearClient, proj project, config runnerConfig, stateStore *state.Store, options candidateSelectionOptions) (*claimedRunAttempt, bool, error) {
	if removed, err := cleanupStaleRunLocksWithStateContext(ctx, stateStore, config.WorkspaceRoot, time.Now()); err != nil {
		return nil, false, err
	} else if removed > 0 {
		log("removed %d stale/dead run lock(s) before candidate selection", removed)
	}
	candidate, selectedPR, err := nextRunnableCandidateWithOptionsContext(ctx, client, config, stateStore, options)
	if err != nil {
		return nil, false, err
	}
	if candidate == nil {
		log("no eligible issues")
		return nil, false, nil
	}

	progressStarted := time.Now().UTC()
	log("picked %s: %s (%s)", candidate.Identifier, candidate.Title, candidateOrderReason(*candidate, config.ReadyState))
	workspace, err := safeWorkspacePath(config.WorkspaceRoot, candidate.Identifier)
	if err != nil {
		return nil, true, err
	}
	writeRunProgress(config.WorkspaceRoot, runProgressForIssue(candidate, workspace, "selected", progressStarted))
	writeRunProgress(config.WorkspaceRoot, runProgressForIssue(candidate, workspace, "preflight", progressStarted))
	runtime, err := newAgentRuntime(config.RuntimeProvider)
	if err != nil {
		decision := decideAttemptLifecycle(attemptLifecycleInput{Phase: attemptLifecyclePhasePreflight, RuntimeErrorKind: "configuration", Error: err.Error()})
		snapshot := runProgressForIssue(candidate, workspace, "failed", progressStarted)
		snapshot.Error = err.Error()
		snapshot.NextAction = decision.NextAction
		writeRunProgress(config.WorkspaceRoot, snapshot)
		return nil, true, err
	}
	if _, err := runtime.Preflight(ctx, agentruntime.PreflightInput{ImplementationCommand: configuredRuntimeCommand(config), ReviewCommand: config.ReviewCommand}); err != nil {
		decision := decideAttemptLifecycle(attemptLifecycleInput{Phase: attemptLifecyclePhasePreflight, RuntimeErrorKind: "configuration", Error: err.Error()})
		snapshot := runProgressForIssue(candidate, workspace, "failed", progressStarted)
		snapshot.Error = err.Error()
		snapshot.NextAction = decision.NextAction
		writeRunProgress(config.WorkspaceRoot, snapshot)
		return nil, true, err
	}
	branch, _ := currentGitBranch(workspace)
	implementationTask, taskClaimed, err := claimImplementationWorkerTask(ctx, stateStore, candidate, workspace, branch, progressStarted)
	if err != nil {
		return nil, true, err
	}
	if !taskClaimed {
		log("%s implementation task is already claimed; skipping duplicate dispatch", candidate.Identifier)
		return nil, false, nil
	}
	lock, releaseLock, err := acquireRunLockWithStateContext(ctx, stateStore, workspace, candidate, branch, time.Now())
	if err != nil {
		completeClaimedImplementationWorkerTask(ctx, stateStore, implementationTask, "failed", false, "run_lock_unavailable", err.Error(), progressStarted, time.Now().UTC())
		if errors.Is(err, errRunLocked) {
			log("%v", err)
			return nil, false, nil
		}
		return nil, true, err
	}
	return &claimedRunAttempt{Candidate: candidate, SelectedPR: selectedPR, Workspace: workspace, Branch: lock.Branch, ProgressStarted: progressStarted, ImplementationWorkerTaskKey: implementationTask.TaskKey, ReleaseLock: releaseLock}, true, nil
}

func executeClaimedRunAttempt(ctx context.Context, client linearClient, proj project, config runnerConfig, stateStore *state.Store, claimed claimedRunAttempt) (didWork bool, err error) {
	candidate := claimed.Candidate
	selectedPR := claimed.SelectedPR
	workspace := claimed.Workspace
	branch := claimed.Branch
	progressStarted := claimed.ProgressStarted
	if claimed.ImplementationWorkerTaskKey != "" {
		defer func() {
			status := "completed"
			reason := "attempt_completed"
			errorText := ""
			if err != nil {
				status = "failed"
				reason = "attempt_failed"
				errorText = err.Error()
			}
			task := state.WorkerTask{TaskKey: claimed.ImplementationWorkerTaskKey, Role: implementationWorkerRole, IssueKey: candidate.Identifier, IssueID: candidate.ID, Attempt: 1}
			completeErr := completeClaimedImplementationWorkerTask(ctx, stateStore, task, status, didWork, reason, errorText, progressStarted, time.Now().UTC())
			if err == nil && completeErr != nil {
				err = completeErr
			}
		}()
	}
	if claimed.ReleaseLock != nil {
		defer claimed.ReleaseLock()
	}
	claimedProgress := runProgressForIssue(candidate, workspace, "claimed", progressStarted)
	claimedProgress.Branch = branch
	writeRunProgress(config.WorkspaceRoot, claimedProgress)
	emitRunAttemptEventContext(ctx, stateStore, state.EventAttemptStarted, candidate, branch, map[string]any{"workspace": workspace, "branch": branch})
	states, err := client.workflowStatesContext(ctx, candidate.Team.ID)
	if err != nil {
		return true, err
	}
	linearStatus := linearStatusWorker{client: client, candidate: candidate, states: states}
	if candidate.State.Name == config.ReadyState {
		if _, err := linearStatus.MoveToContext(ctx, config.RunningState); err != nil {
			return true, err
		}
	}
	runStarted := time.Now()
	implementation := implementationWorker{client: client, project: proj, config: config, stateStore: stateStore, candidate: candidate, selectedPR: selectedPR, states: states, workspace: workspace, branch: branch, progressStarted: progressStarted, runStarted: runStarted}
	if err := implementation.Prepare(ctx); err != nil {
		return true, err
	}

	githubEnv, githubAuth, err := githubAppEnvFromEnvironment()
	if err != nil {
		now := time.Now()
		writeRunRecordWithCommandStateContext(ctx, stateStore, workspace, runRecordFor(candidate, workspace, configuredRuntimeCommand(config), "github_app_error", now, now, nil, nil, "", runAttemptStatusFailed, err.Error(), config.Budget.Active(), ""))
		return true, err
	}
	if githubAuth != "" {
		log("github auth: %s", githubAuth)
	}
	if githubAuth == "github_app_installation" {
		if err := configureGitHubAppCommitIdentity(workspace, config.Budget.CommandTimeout); err != nil {
			return true, err
		}
	}
	heartbeatRunLockWithStateContext(ctx, stateStore, workspace, time.Now())

	if selectedPR != nil && config.ReviewCommand != "" {
		decision := reconcileCandidateForSelectionContext(ctx, config, *candidate, selectedPR, stateStore)
		if decision.CanRun && decision.NextAction == "run_semantic_review_after_checks_ready" {
			return resumeReviewReadyRunContext(ctx, client, stateStore, config, candidate, states, workspace, branch, githubEnv, githubAuth, progressStarted, runStarted, selectedPR)
		}
	}

	implementationResult, err := implementation.Run(ctx, githubEnv, githubAuth)
	if err != nil || implementationResult.Terminal {
		return true, err
	}
	prURL := implementationResult.PRURL
	runtimeUsage := implementationResult.Usage
	piOutput := implementationResult.Output
	piStart := implementationResult.Started
	scopeResult, err := checkScopeGuardContext(ctx, candidate.Description, workspace, config.BaseBranch)
	if err != nil {
		decision := decideAttemptLifecycle(attemptLifecycleInput{Phase: attemptLifecyclePhaseScopeGuard, PRURL: prURL, ScopeError: err.Error()})
		writeRunRecordWithCommandStateContext(ctx, stateStore, workspace, runRecordFor(candidate, workspace, configuredRuntimeCommand(config), githubAuth, piStart, time.Now(), runtimeUsage, nil, prURL, decision.Status, err.Error(), config.Budget.Active(), err.Error()))
		return true, err
	}
	if scopeResult.Blocks() {
		reason := scopeResult.Summary()
		decision := decideAttemptLifecycle(attemptLifecycleInput{Phase: attemptLifecyclePhaseScopeGuard, PRURL: prURL, ScopeResult: scopeResult})
		review := &reviewResult{Status: decision.ReviewStatus, Classification: decision.ReviewClassification, Findings: reason}
		if _, err := linearStatus.MoveToContext(ctx, config.ReadyState); err != nil {
			writeRunRecordWithCommandStateContext(ctx, stateStore, workspace, runRecordFor(candidate, workspace, configuredRuntimeCommand(config), githubAuth, piStart, time.Now(), runtimeUsage, review, prURL, decision.Status, err.Error(), config.Budget.Active(), ""))
			return true, err
		}
		comment := fmt.Sprintf("Scope guard failed before handoff; moved back to %s.\n\nPR: %s\nReason: %s", config.ReadyState, prURL, reason)
		if err := linearStatus.CommentContext(ctx, comment); err != nil {
			log("failed to comment on %s: %v", candidate.Identifier, err)
		}
		if err := writeRunRecordWithCommandStateContext(ctx, stateStore, workspace, runRecordFor(candidate, workspace, configuredRuntimeCommand(config), githubAuth, piStart, time.Now(), runtimeUsage, review, prURL, decision.Status, "scope guard failed", config.Budget.Active(), "")); err != nil {
			return true, err
		}
		log("scope guard failed for %s; moved back to %s: %s", candidate.Identifier, config.ReadyState, reason)
		return true, nil
	}
	if strings.TrimSpace(scopeResult.Summary()) != "" {
		log("scope guard: %s", scopeResult.Summary())
	}

	validation := append([]string(nil), implementationResult.Validation...)
	if len(validation) == 0 {
		validation = validationLines(piOutput)
	}
	if strings.TrimSpace(scopeResult.Summary()) != "" {
		validation = append(validation, "Scope guard: "+scopeResult.Summary())
	} else if scopeResult.Checked {
		validation = append(validation, "Scope guard: changed files matched the Linear ticket path contract.")
	}

	prURL, err = ensureRunnerPRHandoffFromInputContext(ctx, config, prHandoffInput{Candidate: candidate, Workspace: workspace, AgentPRURL: prURL, ProgressStarted: progressStarted, AttemptStartedAt: piStart, RuntimeUsage: runtimeUsage, ScopeResult: scopeResult, Validation: validation, GitHubAuth: githubAuth, StateStore: stateStore}, githubEnv)
	if err != nil {
		decision := decideAttemptLifecycle(attemptLifecycleInput{Phase: attemptLifecyclePhaseHandoff, PRURL: prURL, Error: err.Error()})
		writeRunRecordWithCommandStateContext(ctx, stateStore, workspace, runRecordFor(candidate, workspace, configuredRuntimeCommand(config), githubAuth, piStart, time.Now(), runtimeUsage, nil, prURL, decision.Status, err.Error(), config.Budget.Active(), ""))
		return true, err
	}
	handoff := runProgressForIssue(candidate, workspace, "handoff_pr", progressStarted)
	handoff.Branch = branch
	handoff.PRURL = prURL
	writeRunProgress(config.WorkspaceRoot, handoff)

	reviewResult, err := reviewWorker{client: client, config: config, stateStore: stateStore, candidate: candidate, states: states, workspace: workspace, branch: branch, progressStarted: progressStarted, startedAt: piStart, runtimeUsage: runtimeUsage, prURL: prURL, githubEnv: githubEnv, githubAuth: githubAuth, scopeResult: scopeResult, validation: validation}.Execute(ctx)
	if err != nil || reviewResult.Terminal {
		return true, err
	}
	review := reviewResult.Review
	didWork, err = completeAttemptHandoff(ctx, handoffCompletion{client: client, config: config, stateStore: stateStore, candidate: candidate, states: states, workspace: workspace, branch: branch, progressStarted: progressStarted, startedAt: piStart, runtimeUsage: runtimeUsage, review: review, prURL: prURL, validation: validation, githubAuth: githubAuth})
	if err == nil {
		log("completed one Pi run for %s; inspect %s", candidate.Identifier, workspace)
	}
	return didWork, err
}

var openPRsByIssueForSelection = openPRsByIssue

func emitRunAttemptEvent(store *state.Store, eventType string, candidate *issue, runID string, payload map[string]any) {
	emitRunAttemptEventContext(context.Background(), store, eventType, candidate, runID, payload)
}

func emitRunAttemptEventContext(ctx context.Context, store *state.Store, eventType string, candidate *issue, runID string, payload map[string]any) {
	if store == nil || candidate == nil {
		return
	}
	if payload == nil {
		payload = map[string]any{}
	}
	if _, err := store.AppendEvent(ctx, state.EventInput{OccurredAt: time.Now().UTC(), IssueKey: candidate.Identifier, IssueID: candidate.ID, Attempt: 1, RunID: runID, Source: "runner.run_attempt", Type: eventType, Payload: payload}); err != nil {
		log("failed to append orchestration event %s for %s: %v", eventType, candidate.Identifier, err)
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func normalizedAttemptEnvelope(result agentruntime.AttemptResult) agentruntime.AttemptOutcomeEnvelope {
	envelope := result.Envelope
	if envelope.RuntimeOutcome == "" {
		envelope.RuntimeOutcome = result.AttemptOutcome
	}
	if envelope.PRURL == "" {
		envelope.PRURL = result.PRURL
	}
	if envelope.RawOutput == "" {
		envelope.RawOutput = result.Output
	}
	if envelope.Usage == nil {
		envelope.Usage = result.Usage
	}
	if envelope.ErrorKind == "" {
		envelope.ErrorKind = result.ErrorKind
	}
	if envelope.Error == "" {
		envelope.Error = result.Error
	}
	return envelope
}

func commandFailureLifecycleDecision(phase attemptLifecyclePhase, err error, timeout bool) attemptLifecycleDecision {
	input := attemptLifecycleInput{Phase: phase, RuntimeOutcome: runAttemptStatusFailed}
	if err != nil {
		input.Error = err.Error()
	}
	if timeout {
		input.RuntimeOutcome = runAttemptStatusTimeout
		input.RuntimeErrorKind = runAttemptStatusTimeout
	}
	return decideAttemptLifecycle(input)
}

func budgetLifecycleDecision(phase attemptLifecyclePhase, prURL, exceeded string) attemptLifecycleDecision {
	return decideAttemptLifecycle(attemptLifecycleInput{
		Phase:          phase,
		PRURL:          prURL,
		BudgetExceeded: exceeded,
	})
}

func needsInfoLifecycleDecision(questions []string, runtimeOutcome, errorMessage string) attemptLifecycleDecision {
	return decideAttemptLifecycle(attemptLifecycleInput{
		Phase:              attemptLifecyclePhaseNeedsInfo,
		RuntimeOutcome:     runtimeOutcome,
		Error:              errorMessage,
		NeedsInfoQuestions: questions,
	})
}
