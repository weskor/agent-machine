package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/weskor/pi-symphony/internal/agentruntime"
	cfg "github.com/weskor/pi-symphony/internal/config"
	sh "github.com/weskor/pi-symphony/internal/shell"
	"github.com/weskor/pi-symphony/internal/state"
)

func runOne(client linearClient, wf workflow, config runnerConfig) (bool, error) {
	log("mode=once; project=%s; states=%s", config.ProjectSlug, strings.Join(config.ActiveStates, ", "))
	stateStore, stateDBPath := commandScopedStateStore(context.Background(), config.WorkspaceRoot, "run-one")
	if stateStore == nil {
		return false, fmt.Errorf("SQLite state store unavailable for run-one at %s", stateDBPath)
	}
	defer stateStore.Close()
	claim, didWork, err := claimNextRunAttempt(client, wf, config, stateStore)
	if err != nil || claim == nil {
		return didWork, err
	}
	return executeClaimedRunAttempt(client, wf, config, stateStore, *claim)
}

type claimedRunAttempt struct {
	Candidate       *issue
	SelectedPR      *pullRequestSummary
	Workspace       string
	Branch          string
	ProgressStarted time.Time
	ReleaseLock     func()
}

func claimNextRunAttempt(client linearClient, wf workflow, config runnerConfig, stateStore *state.Store) (*claimedRunAttempt, bool, error) {
	if removed, err := cleanupStaleRunLocksWithState(stateStore, config.WorkspaceRoot, time.Now()); err != nil {
		return nil, false, err
	} else if removed > 0 {
		log("removed %d stale/dead run lock(s) before candidate selection", removed)
	}
	candidate, selectedPR, err := nextRunnableCandidate(client, config, stateStore)
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
	runtime := newPiCLIRuntime()
	writeRunProgress(config.WorkspaceRoot, runProgressForIssue(candidate, workspace, "preflight", progressStarted))
	if _, err := runtime.Preflight(context.Background(), agentruntime.PreflightInput{ImplementationCommand: config.PiCommand, ReviewCommand: config.ReviewCommand, MaxTurns: cfg.AgentMaxTurnsFromWorkflow(wf.YAML)}); err != nil {
		decision := decideAttemptLifecycle(attemptLifecycleInput{Phase: attemptLifecyclePhasePreflight, RuntimeErrorKind: "configuration", Error: err.Error()})
		snapshot := runProgressForIssue(candidate, workspace, "failed", progressStarted)
		snapshot.Error = err.Error()
		snapshot.NextAction = decision.NextAction
		writeRunProgress(config.WorkspaceRoot, snapshot)
		return nil, true, err
	}
	branch, _ := currentGitBranch(workspace)
	if existing, ok := reusableRunRecord(workspace); ok {
		if feedbackRetryAvailable(workspace, candidate, existing, config, selectedPR) {
			log("%s has terminal artifact but PR feedback is pending; retrying existing PR %s", candidate.Identifier, existing.PRURL)
		} else {
			log("%s already has terminal run artifact status=%s pr=%s; skipping duplicate work", candidate.Identifier, existing.Status, existing.PRURL)
			return nil, true, nil
		}
	}
	lock, releaseLock, err := acquireRunLockWithState(stateStore, workspace, candidate, branch, time.Now())
	if err != nil {
		if errors.Is(err, errRunLocked) {
			log("%v", err)
			return nil, false, nil
		}
		return nil, true, err
	}
	return &claimedRunAttempt{Candidate: candidate, SelectedPR: selectedPR, Workspace: workspace, Branch: lock.Branch, ProgressStarted: progressStarted, ReleaseLock: releaseLock}, true, nil
}

func executeClaimedRunAttempt(client linearClient, wf workflow, config runnerConfig, stateStore *state.Store, claimed claimedRunAttempt) (bool, error) {
	candidate := claimed.Candidate
	selectedPR := claimed.SelectedPR
	workspace := claimed.Workspace
	branch := claimed.Branch
	progressStarted := claimed.ProgressStarted
	if claimed.ReleaseLock != nil {
		defer claimed.ReleaseLock()
	}
	claimedProgress := runProgressForIssue(candidate, workspace, "claimed", progressStarted)
	claimedProgress.Branch = branch
	writeRunProgress(config.WorkspaceRoot, claimedProgress)
	emitRunAttemptEvent(stateStore, state.EventAttemptStarted, candidate, branch, map[string]any{"workspace": workspace, "branch": branch})
	states, err := client.workflowStates(candidate.Team.ID)
	if err != nil {
		return true, err
	}
	if candidate.State.Name == config.ReadyState {
		if id := stateID(states, config.RunningState); id != "" {
			if err := client.updateIssueState(candidate.ID, id); err != nil {
				return true, err
			}
			candidate.State.Name = config.RunningState
			log("moved %s to %s", candidate.Identifier, config.RunningState)
		}
	}
	runStarted := time.Now()
	implementation := implementationWorker{client: client, workflow: wf, config: config, stateStore: stateStore, candidate: candidate, selectedPR: selectedPR, states: states, workspace: workspace, branch: branch, progressStarted: progressStarted, runStarted: runStarted}
	if err := implementation.Prepare(); err != nil {
		return true, err
	}

	githubEnv, githubAuth, err := githubAppEnvFromEnvironment()
	if err != nil {
		now := time.Now()
		writeRunRecordWithCommandState(stateStore, workspace, runRecordFor(candidate, workspace, config.PiCommand, "github_app_error", now, now, nil, nil, "", runAttemptStatusFailed, err.Error(), config.Budget.Active(), ""))
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
	heartbeatRunLockWithState(stateStore, workspace, time.Now())

	reviewReadiness := newReviewReadinessModule(config.WorkspaceRoot)
	if selectedPR != nil && config.ReviewCommand != "" && reviewReadiness.ShouldResume(candidate.Identifier, *selectedPR) {
		return resumeReviewReadyRun(client, stateStore, config, candidate, states, workspace, branch, githubEnv, githubAuth, progressStarted, runStarted, selectedPR)
	}

	implementationResult, err := implementation.Run(context.Background(), githubEnv, githubAuth)
	if err != nil || implementationResult.Terminal {
		return true, err
	}
	prURL := implementationResult.PRURL
	piUsage := implementationResult.Usage
	piOutput := implementationResult.Output
	piStart := implementationResult.Started
	scopeResult, err := checkScopeGuard(candidate.Description, workspace, config.BaseBranch)
	if err != nil {
		decision := decideAttemptLifecycle(attemptLifecycleInput{Phase: attemptLifecyclePhaseScopeGuard, PRURL: prURL, ScopeError: err.Error()})
		writeRunRecordWithCommandState(stateStore, workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, nil, prURL, decision.Status, err.Error(), config.Budget.Active(), err.Error()))
		return true, err
	}
	if scopeResult.Blocks() {
		reason := scopeResult.Summary()
		decision := decideAttemptLifecycle(attemptLifecycleInput{Phase: attemptLifecyclePhaseScopeGuard, PRURL: prURL, ScopeResult: scopeResult})
		review := &reviewResult{Status: decision.ReviewStatus, Classification: decision.ReviewClassification, Findings: reason}
		if id := stateID(states, config.ReadyState); id != "" {
			if err := client.updateIssueState(candidate.ID, id); err != nil {
				writeRunRecordWithCommandState(stateStore, workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, review, prURL, decision.Status, err.Error(), config.Budget.Active(), ""))
				return true, err
			}
		}
		comment := fmt.Sprintf("Scope guard failed before handoff; moved back to %s.\n\nPR: %s\nReason: %s", config.ReadyState, prURL, reason)
		if err := client.createComment(candidate.ID, comment); err != nil {
			log("failed to comment on %s: %v", candidate.Identifier, err)
		}
		if err := writeRunRecordWithCommandState(stateStore, workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, review, prURL, decision.Status, "scope guard failed", config.Budget.Active(), "")); err != nil {
			return true, err
		}
		log("scope guard failed for %s; moved back to %s: %s", candidate.Identifier, config.ReadyState, reason)
		return true, nil
	}
	if strings.TrimSpace(scopeResult.Summary()) != "" {
		log("scope guard: %s", scopeResult.Summary())
	}

	prURL, err = ensureRunnerPRHandoff(config, candidate, workspace, prURL, githubEnv)
	if err != nil {
		decision := decideAttemptLifecycle(attemptLifecycleInput{Phase: attemptLifecyclePhaseHandoff, PRURL: prURL, Error: err.Error()})
		writeRunRecordWithCommandState(stateStore, workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, nil, prURL, decision.Status, err.Error(), config.Budget.Active(), ""))
		return true, err
	}
	handoff := runProgressForIssue(candidate, workspace, "handoff_pr", progressStarted)
	handoff.Branch = branch
	handoff.PRURL = prURL
	writeRunProgress(config.WorkspaceRoot, handoff)

	validation := validationLines(piOutput)
	if strings.TrimSpace(scopeResult.Summary()) != "" {
		validation = append(validation, "Scope guard: "+scopeResult.Summary())
	} else if scopeResult.Checked {
		validation = append(validation, "Scope guard: changed files matched the Linear ticket path contract.")
	}

	var reviewEvidence *reviewEvidence
	if prURL != "" && config.ReviewCommand != "" {
		evidence, err := collectReviewEvidence(config, candidate, workspace, prURL, scopeResult, validation)
		if err != nil {
			writeRunRecordWithCommandState(stateStore, workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, nil, prURL, runAttemptStatusFailed, err.Error(), config.Budget.Active(), err.Error()))
			return true, err
		}
		reviewEvidence = &evidence
		if err := reviewEvidenceNotReadyError(*reviewEvidence); err != nil {
			decision := reviewReadiness.NotReadyDecision(prURL, *reviewEvidence)
			notReady := reviewReadiness.NotReadyProgress(candidate, workspace, branch, prURL, progressStarted, *reviewEvidence)
			writeRunProgress(config.WorkspaceRoot, notReady)
			writeRunRecordWithCommandState(stateStore, workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, nil, prURL, decision.Status, err.Error(), config.Budget.Active(), err.Error()))
			return true, nil
		}
	}

	var review *reviewResult
	if prURL != "" && config.ReviewCommand != "" {
		reviewing := runProgressForIssue(candidate, workspace, "reviewing", progressStarted)
		reviewing.Branch = branch
		reviewing.PRURL = prURL
		if reviewEvidence != nil {
			reviewing.ChecksStatus = reviewEvidence.ChecksStatus
		}
		writeRunProgress(config.WorkspaceRoot, reviewing)
		reviewResult, err := runReview(config.ReviewCommand, workspace, candidate, prURL, githubEnv, config.Budget.ReviewTimeout, reviewEvidence)
		review = reviewResult
		if err != nil {
			status := runAttemptStatusReviewFailed
			if errors.Is(err, sh.ErrCommandTimeout) {
				status = runAttemptStatusTimeout
				if commentErr := client.createComment(candidate.ID, renderBudgetFailureComment(err.Error())); commentErr != nil {
					log("failed to comment on %s: %v", candidate.Identifier, commentErr)
				}
			}
			writeRunRecordWithCommandState(stateStore, workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, review, prURL, status, err.Error(), config.Budget.Active(), err.Error()))
			return true, err
		}
		if exceeded := budgetExceeded(config.Budget, piStart, piUsage, review.Usage); exceeded != "" {
			decision := budgetLifecycleDecision(attemptLifecyclePhaseReview, prURL, exceeded)
			if err := client.createComment(candidate.ID, renderBudgetFailureComment(exceeded)); err != nil {
				log("failed to comment on %s: %v", candidate.Identifier, err)
			}
			writeRunRecordWithCommandState(stateStore, workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, review, prURL, decision.Status, exceeded, config.Budget.Active(), exceeded))
			return true, fmt.Errorf("%s", exceeded)
		}
		if review != nil && review.Status != "passed" && !reviewFailureRoutesToHumanHandoff(review, prURL) {
			if id := stateID(states, config.ReadyState); id != "" {
				if err := client.updateIssueState(candidate.ID, id); err != nil {
					writeRunRecordWithCommandState(stateStore, workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, review, prURL, runAttemptStatusReviewFailed, err.Error(), config.Budget.Active(), ""))
					return true, err
				}
			}
			comment := fmt.Sprintf("Go/Pi review did not pass; moved back to %s.\n\nPR: %s\nReview status: %s\nFindings:\n%s", config.ReadyState, prURL, review.Status, review.Findings)
			if err := client.createComment(candidate.ID, comment); err != nil {
				log("failed to comment on %s: %v", candidate.Identifier, err)
			}
			if err := writeRunRecordWithCommandState(stateStore, workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, review, prURL, runAttemptStatusReviewFailed, "review did not pass", config.Budget.Active(), "")); err != nil {
				return true, err
			}
			log("review did not pass for %s; moved back to %s", candidate.Identifier, config.ReadyState)
			return true, nil
		}
		if reviewFailureRoutesToHumanHandoff(review, prURL) {
			log("review failed for %s with missing evidence only; routing to %s", candidate.Identifier, config.HandoffState)
		}
	}
	if prURL != "" {
		logHandoffRunSummary(candidate.Identifier, prURL, review, validation)
		classificationRecord := runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, review, prURL, runAttemptStatusSuccess, "", config.Budget.Active(), "")
		classification := classifyRunRecord(workspace, classificationRecord)
		summary := handoffSummary{IssueIdentifier: candidate.Identifier, IssueTitle: candidate.Title, IssueURL: candidate.URL, PRURL: prURL, PiUsage: piUsage, Review: review, Duration: time.Since(piStart), Validation: validation, FollowUps: followUpLines(review), Classification: &classification}
		if err := postOrUpdatePRHandoffComment(summary); err != nil {
			log("failed to post GitHub handoff comment for %s: %v", prURL, err)
		}
		if id := stateID(states, config.HandoffState); id != "" {
			if err := client.updateIssueState(candidate.ID, id); err != nil {
				writeRunRecordWithCommandState(stateStore, workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, review, prURL, runAttemptStatusFailed, err.Error(), config.Budget.Active(), ""))
				return true, err
			}
			log("moved %s to %s", candidate.Identifier, config.HandoffState)
		}
		comment := renderLinearHandoffComment(summary)
		if err := client.createComment(candidate.ID, comment); err != nil {
			log("failed to comment on %s: %v", candidate.Identifier, err)
		}
	}
	if err := writeRunRecordWithCommandState(stateStore, workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, review, prURL, runAttemptStatusSuccess, "", config.Budget.Active(), "")); err != nil {
		return true, err
	}
	log("completed one Pi run for %s; inspect %s", candidate.Identifier, workspace)
	return true, nil
}

var openPRsByIssueForSelection = openPRsByIssue

func emitRunAttemptEvent(store *state.Store, eventType string, candidate *issue, runID string, payload map[string]any) {
	if store == nil || candidate == nil {
		return
	}
	if payload == nil {
		payload = map[string]any{}
	}
	if _, err := store.AppendEvent(context.Background(), state.EventInput{OccurredAt: time.Now().UTC(), IssueKey: candidate.Identifier, IssueID: candidate.ID, Attempt: 1, RunID: runID, Source: "runner.run_attempt", Type: eventType, Payload: payload}); err != nil {
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
