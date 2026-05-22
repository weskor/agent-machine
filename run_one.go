package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
		snapshot := runProgressForIssue(candidate, workspace, "failed", progressStarted)
		snapshot.Error = err.Error()
		snapshot.NextAction = "fix_runtime_configuration_before_retry"
		writeRunProgress(config.WorkspaceRoot, snapshot)
		return nil, true, err
	}
	branch, _ := currentGitBranch(workspace)
	if existing, ok := reusableRunRecord(workspace); ok {
		if feedbackRetryAvailable(workspace, candidate, existing, config) {
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

	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return true, err
	}
	prepared := runProgressForIssue(candidate, workspace, "workspace_prepared", progressStarted)
	prepared.Branch = branch
	writeRunProgress(config.WorkspaceRoot, prepared)
	if isEmptyIgnoringRunLock(workspace) && strings.TrimSpace(config.AfterCreate) != "" {
		if err := sh.RunWithTimeout(config.AfterCreate, workspace, config.Budget.CommandTimeout); err != nil {
			if errors.Is(err, sh.ErrCommandTimeout) {
				_ = client.createComment(candidate.ID, renderBudgetFailureComment(err.Error()))
				writeRunRecordWithCommandState(stateStore, workspace, runRecordFor(candidate, workspace, config.PiCommand, "", runStarted, time.Now(), nil, nil, "", runAttemptStatusTimeout, err.Error(), config.Budget.Active(), err.Error()))
			}
			return true, err
		}
	}
	if err := ensureIsolatedWorkspace(config.WorkspaceRoot, workspace, candidate.Identifier); err != nil {
		return true, err
	}
	if config.BeforeRun != "" {
		if err := sh.RunWithTimeout(config.BeforeRun, workspace, config.Budget.CommandTimeout); err != nil {
			if errors.Is(err, sh.ErrCommandTimeout) {
				_ = client.createComment(candidate.ID, renderBudgetFailureComment(err.Error()))
				writeRunRecordWithCommandState(stateStore, workspace, runRecordFor(candidate, workspace, config.PiCommand, "", runStarted, time.Now(), nil, nil, "", runAttemptStatusTimeout, err.Error(), config.Budget.Active(), err.Error()))
			}
			return true, err
		}
	}
	if selectedPR != nil && selectedPR.ReviewDecision == "CHANGES_REQUESTED" {
		feedback, err := collectPRFeedback(selectedPR.Number)
		if err != nil {
			return true, err
		}
		if err := writePRFeedback(workspace, selectedPR.Number, feedback); err != nil {
			return true, err
		}
		log("captured PR feedback for %s before retrying %s", selectedPR.URL, candidate.Identifier)
	}

	feedback, err := readPRFeedback(workspace)
	if err != nil {
		return true, err
	}
	feedbackBlock := ""
	if strings.TrimSpace(feedback) != "" {
		feedbackBlock = fmt.Sprintf("\n\nGitHub PR feedback to address before handoff:\n%s\n", feedback)
	}
	prompt := renderPrompt(wf.Body, *candidate, 1) + fmt.Sprintf("\n\nLinear issue description:\n%s%s\n\n%s\n\n%s\n\nPi Symphony runner constraints:\n- Follow the Linear issue description exactly; do not infer broader implementation work from the title alone.\n- If GitHub PR feedback is present, address that feedback in the existing PR branch rather than starting unrelated work.\n- If required information is missing or the ticket is ambiguous/unsafe to implement, output NEEDS_INFO followed by numbered questions instead of guessing.\n- Run exactly once; do not ask for continuation.\n- Keep context usage minimal.\n- Leave scoped code/test/doc changes in this workspace and include validation notes.\n- Do not create, update, push, or comment on a GitHub PR; the Pi Symphony runner will commit, push, create or update exactly one PR from branch %s into base branch %s, and post deterministic handoff comments.\n- Before finishing, perform a focused self-review of the final diff for scope, secrets, validation, tenant/security risk, unrelated files, and behavior-contract evidence; fix any clear findings.\n- Stop after the scoped diff and validation notes.\n- The runner will move the Linear issue to %s after runner PR handoff, or to %s when NEEDS_INFO is detected.\n", candidate.Description, feedbackBlock, ticketContractPrompt(), behaviorContractPreflightPrompt(), expectedWorkspaceBranch(candidate.Identifier), config.BaseBranch, config.HandoffState, config.NeedsInfoState)
	promptPath := filepath.Join(workspace, ".pi-symphony-prompt.md")
	if err := os.WriteFile(promptPath, []byte(prompt), 0o600); err != nil {
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

	if selectedPR != nil && config.ReviewCommand != "" && shouldResumeReviewReadiness(config.WorkspaceRoot, candidate.Identifier, *selectedPR) {
		return resumeReviewReadyRun(client, stateStore, config, candidate, states, workspace, branch, githubEnv, githubAuth, progressStarted, runStarted, selectedPR)
	}

	runtime := newPiCLIRuntime()
	attempt, err := runtime.StartAttempt(context.Background(), agentruntime.StartAttemptInput{IssueID: candidate.ID, IssueIdentifier: candidate.Identifier, Workspace: workspace, Branch: branch, ExpectedBranch: expectedWorkspaceBranch(candidate.Identifier), Attempt: 1, WorkingDir: workspace, Command: config.PiCommand, PromptPath: promptPath, Timeouts: agentruntime.AttemptTimeouts{WallClock: config.Budget.WallClock, Command: config.Budget.CommandTimeout, Review: config.Budget.ReviewTimeout}, Environment: githubEnv})
	if err != nil {
		return true, err
	}
	emitRunAttemptEvent(stateStore, state.EventRuntimeStarted, candidate, branch, map[string]any{"attempt_id": attempt.ID})
	implementing := runProgressForIssue(candidate, workspace, "implementing", progressStarted)
	implementing.Branch = branch
	writeRunProgress(config.WorkspaceRoot, implementing)
	piResult, err := runtime.RunAttempt(context.Background(), attempt.ID, agentruntime.RunAttemptInput{Command: config.PiCommand, PromptPath: promptPath, WorkingDir: workspace, Timeout: config.Budget.PiTimeout, Environment: githubEnv}, agentruntime.NoopSink{})
	piStart := piResult.StartedAt
	piEnded := piResult.EndedAt
	piOutput := piResult.Output
	emitRunAttemptEvent(stateStore, state.EventRuntimeFinished, candidate, branch, map[string]any{"attempt_id": attempt.ID, "outcome": piResult.AttemptOutcome, "pr_url": piResult.PRURL, "error": errorString(err)})
	if err != nil {
		status := runAttemptStatusFailed
		if piResult.AttemptOutcome == agentruntime.AttemptOutcomeTimeout || errors.Is(err, sh.ErrCommandTimeout) {
			status = runAttemptStatusTimeout
			if commentErr := client.createComment(candidate.ID, renderBudgetFailureComment(err.Error())); commentErr != nil {
				log("failed to comment on %s: %v", candidate.Identifier, commentErr)
			}
		}
		writeRunRecordWithCommandState(stateStore, workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, piEnded, usageFromRuntime(piResult.Usage), nil, piResult.PRURL, status, err.Error(), config.Budget.Active(), err.Error()))
		return true, err
	}
	piUsage := usageFromRuntime(piResult.Usage)
	if piUsage != nil {
		log("pi usage: input=%.0f output=%.0f cacheRead=%.0f total=%.0f cost=$%.4f", piUsage.Input, piUsage.Output, piUsage.CacheRead, piUsage.TotalTokens, piUsage.TotalCost())
	} else {
		log("pi usage: unavailable")
	}
	prURL := piResult.PRURL
	validating := runProgressForIssue(candidate, workspace, "validating", progressStarted)
	validating.Branch = branch
	validating.PRURL = prURL
	writeRunProgress(config.WorkspaceRoot, validating)
	log("pi run duration: %s", piEnded.Sub(piStart).Round(time.Second))
	if exceeded := budgetExceeded(config.Budget, piStart, piUsage); exceeded != "" {
		if err := client.createComment(candidate.ID, renderBudgetFailureComment(exceeded)); err != nil {
			log("failed to comment on %s: %v", candidate.Identifier, err)
		}
		writeRunRecordWithCommandState(stateStore, workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, nil, prURL, runAttemptStatusBudgetExceeded, exceeded, config.Budget.Active(), exceeded))
		return true, fmt.Errorf("%s", exceeded)
	}
	if needsInfo := parseNeedsInfo(piOutput); needsInfo.NeedsInfo {
		if id := stateID(states, config.NeedsInfoState); id != "" {
			if err := client.updateIssueState(candidate.ID, id); err != nil {
				writeRunRecordWithCommandState(stateStore, workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, nil, prURL, runAttemptStatusNeedsInfoFail, err.Error(), config.Budget.Active(), ""))
				return true, err
			}
			log("moved %s to %s", candidate.Identifier, config.NeedsInfoState)
		} else {
			log("needs-info state %q was not found for %s", config.NeedsInfoState, candidate.Identifier)
		}
		comment := renderNeedsInfoComment(needsInfo.Questions)
		if err := client.createComment(candidate.ID, comment); err != nil {
			log("failed to comment on %s: %v", candidate.Identifier, err)
		}
		if err := writeRunRecordWithCommandState(stateStore, workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, nil, "", runAttemptStatusNeedsInfo, strings.Join(needsInfo.Questions, "\n"), config.Budget.Active(), "")); err != nil {
			return true, err
		}
		log("%s needs additional information; stopped without PR handoff", candidate.Identifier)
		return true, nil
	}
	if config.AfterRun != "" {
		if err := sh.RunWithTimeout(config.AfterRun, workspace, config.Budget.CommandTimeout); err != nil {
			status := runAttemptStatusFailed
			if errors.Is(err, sh.ErrCommandTimeout) {
				status = runAttemptStatusTimeout
				if commentErr := client.createComment(candidate.ID, renderBudgetFailureComment(err.Error())); commentErr != nil {
					log("failed to comment on %s: %v", candidate.Identifier, commentErr)
				}
			}
			writeRunRecordWithCommandState(stateStore, workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, nil, prURL, status, err.Error(), config.Budget.Active(), err.Error()))
			return true, err
		}
	}
	scopeResult, err := checkScopeGuard(candidate.Description, workspace, config.BaseBranch)
	if err != nil {
		writeRunRecordWithCommandState(stateStore, workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, nil, prURL, runAttemptStatusFailed, err.Error(), config.Budget.Active(), err.Error()))
		return true, err
	}
	if scopeResult.Blocks() {
		reason := scopeResult.Summary()
		review := &reviewResult{Status: runAttemptStatusFailed, Classification: reviewClassificationBehaviorSpecBlocker, Findings: reason}
		if id := stateID(states, config.ReadyState); id != "" {
			if err := client.updateIssueState(candidate.ID, id); err != nil {
				writeRunRecordWithCommandState(stateStore, workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, review, prURL, runAttemptStatusReviewFailed, err.Error(), config.Budget.Active(), ""))
				return true, err
			}
		}
		comment := fmt.Sprintf("Scope guard failed before handoff; moved back to %s.\n\nPR: %s\nReason: %s", config.ReadyState, prURL, reason)
		if err := client.createComment(candidate.ID, comment); err != nil {
			log("failed to comment on %s: %v", candidate.Identifier, err)
		}
		if err := writeRunRecordWithCommandState(stateStore, workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, review, prURL, runAttemptStatusReviewFailed, "scope guard failed", config.Budget.Active(), "")); err != nil {
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
		writeRunRecordWithCommandState(stateStore, workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, nil, prURL, runAttemptStatusFailed, err.Error(), config.Budget.Active(), ""))
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
			notReady := runProgressForIssue(candidate, workspace, "review_not_ready", progressStarted)
			notReady.Branch = branch
			notReady.PRURL = prURL
			notReady.Status = "waiting_for_checks"
			notReady.ChecksStatus = reviewEvidence.ChecksStatus
			notReady.NextAction = "wait_for_github_checks_then_retry"
			if reviewEvidence.ChecksStatus == "failed" {
				notReady.NextAction = "fix_failing_github_checks_before_review"
			}
			notReady.Error = reviewEvidence.ChecksSummary
			writeRunProgress(config.WorkspaceRoot, notReady)
			writeRunRecordWithCommandState(stateStore, workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, nil, prURL, runAttemptStatusReviewNotReady, err.Error(), config.Budget.Active(), err.Error()))
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
			if err := client.createComment(candidate.ID, renderBudgetFailureComment(exceeded)); err != nil {
				log("failed to comment on %s: %v", candidate.Identifier, err)
			}
			writeRunRecordWithCommandState(stateStore, workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, review, prURL, runAttemptStatusBudgetExceeded, exceeded, config.Budget.Active(), exceeded))
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

func shouldResumeReviewReadiness(workspaceRoot, issueIdentifier string, pr pullRequestSummary) bool {
	snapshot, err := readRunProgress(workspaceRoot, issueIdentifier)
	if err != nil {
		return false
	}
	if snapshot.Phase != "review_not_ready" {
		return false
	}
	if strings.TrimSpace(snapshot.PRURL) != "" && snapshot.PRURL != pr.URL {
		return false
	}
	status, _ := reviewChecksStatus(pr.StatusCheckRollup)
	return status == "success"
}

func resumeReviewReadyRun(client linearClient, stateStore *state.Store, config runnerConfig, candidate *issue, states []workflowState, workspace, branch string, githubEnv map[string]string, githubAuth string, progressStarted, runStarted time.Time, selectedPR *pullRequestSummary) (bool, error) {
	prURL := selectedPR.URL
	scopeResult, err := checkScopeGuard(candidate.Description, workspace, config.BaseBranch)
	if err != nil {
		writeRunRecordWithCommandState(stateStore, workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, runStarted, time.Now(), nil, nil, prURL, runAttemptStatusFailed, err.Error(), config.Budget.Active(), err.Error()))
		return true, err
	}
	validation := []string{"Implementation was preserved from prior runner-owned PR handoff; semantic review resumed after GitHub checks became terminal."}
	if strings.TrimSpace(scopeResult.Summary()) != "" {
		validation = append(validation, "Scope guard: "+scopeResult.Summary())
	} else if scopeResult.Checked {
		validation = append(validation, "Scope guard: changed files matched the Linear ticket path contract.")
	}
	evidence, err := collectReviewEvidence(config, candidate, workspace, prURL, scopeResult, validation)
	if err != nil {
		writeRunRecordWithCommandState(stateStore, workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, runStarted, time.Now(), nil, nil, prURL, runAttemptStatusFailed, err.Error(), config.Budget.Active(), err.Error()))
		return true, err
	}
	if err := reviewEvidenceNotReadyError(evidence); err != nil {
		notReady := runProgressForIssue(candidate, workspace, "review_not_ready", progressStarted)
		notReady.Branch = branch
		notReady.PRURL = prURL
		notReady.Status = "waiting_for_checks"
		notReady.ChecksStatus = evidence.ChecksStatus
		notReady.NextAction = "wait_for_github_checks_then_retry"
		notReady.Error = evidence.ChecksSummary
		writeRunProgress(config.WorkspaceRoot, notReady)
		writeRunRecordWithCommandState(stateStore, workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, runStarted, time.Now(), nil, nil, prURL, runAttemptStatusReviewNotReady, err.Error(), config.Budget.Active(), err.Error()))
		return true, nil
	}
	reviewing := runProgressForIssue(candidate, workspace, "reviewing", progressStarted)
	reviewing.Branch = branch
	reviewing.PRURL = prURL
	reviewing.ChecksStatus = evidence.ChecksStatus
	writeRunProgress(config.WorkspaceRoot, reviewing)
	review, err := runReview(config.ReviewCommand, workspace, candidate, prURL, githubEnv, config.Budget.ReviewTimeout, &evidence)
	if err != nil {
		status := runAttemptStatusReviewFailed
		if errors.Is(err, sh.ErrCommandTimeout) {
			status = runAttemptStatusTimeout
			_ = client.createComment(candidate.ID, renderBudgetFailureComment(err.Error()))
		}
		writeRunRecordWithCommandState(stateStore, workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, runStarted, time.Now(), nil, review, prURL, status, err.Error(), config.Budget.Active(), err.Error()))
		return true, err
	}
	if review != nil && review.Status != "passed" && !reviewFailureRoutesToHumanHandoff(review, prURL) {
		if id := stateID(states, config.ReadyState); id != "" {
			if err := client.updateIssueState(candidate.ID, id); err != nil {
				writeRunRecordWithCommandState(stateStore, workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, runStarted, time.Now(), nil, review, prURL, runAttemptStatusReviewFailed, err.Error(), config.Budget.Active(), ""))
				return true, err
			}
		}
		_ = client.createComment(candidate.ID, fmt.Sprintf("Go/Pi review did not pass; moved back to %s.\n\nPR: %s\nReview status: %s\nFindings:\n%s", config.ReadyState, prURL, review.Status, review.Findings))
		writeRunRecordWithCommandState(stateStore, workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, runStarted, time.Now(), nil, review, prURL, runAttemptStatusReviewFailed, "review did not pass", config.Budget.Active(), ""))
		return true, nil
	}
	classificationRecord := runRecordFor(candidate, workspace, config.PiCommand, githubAuth, runStarted, time.Now(), nil, review, prURL, runAttemptStatusSuccess, "", config.Budget.Active(), "")
	classification := classifyRunRecord(workspace, classificationRecord)
	summary := handoffSummary{IssueIdentifier: candidate.Identifier, IssueTitle: candidate.Title, IssueURL: candidate.URL, PRURL: prURL, Review: review, Duration: time.Since(runStarted), Validation: validation, FollowUps: followUpLines(review), Classification: &classification}
	if err := postOrUpdatePRHandoffComment(summary); err != nil {
		log("failed to post GitHub handoff comment for %s: %v", prURL, err)
	}
	if id := stateID(states, config.HandoffState); id != "" {
		if err := client.updateIssueState(candidate.ID, id); err != nil {
			writeRunRecordWithCommandState(stateStore, workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, runStarted, time.Now(), nil, review, prURL, runAttemptStatusFailed, err.Error(), config.Budget.Active(), ""))
			return true, err
		}
	}
	_ = client.createComment(candidate.ID, renderLinearHandoffComment(summary))
	if err := writeRunRecordWithCommandState(stateStore, workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, runStarted, time.Now(), nil, review, prURL, runAttemptStatusSuccess, "", config.Budget.Active(), "")); err != nil {
		return true, err
	}
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
