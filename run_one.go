package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	sh "github.com/weskor/pi-symphony/internal/shell"
)

func runOne(client linearClient, wf workflow, config runnerConfig) (bool, error) {
	log("mode=once; project=%s; states=%s", config.ProjectSlug, strings.Join(config.ActiveStates, ", "))
	if removed, err := cleanupStaleRunLocks(config.WorkspaceRoot, time.Now()); err != nil {
		return false, err
	} else if removed > 0 {
		log("removed %d stale/dead run lock(s) before candidate selection", removed)
	}
	candidate, selectedPR, err := nextRunnableCandidate(client, config)
	if err != nil {
		return false, err
	}
	if candidate == nil {
		log("no eligible issues")
		return false, nil
	}

	log("picked %s: %s (%s)", candidate.Identifier, candidate.Title, candidateOrderReason(*candidate, config.ReadyState))
	workspace, err := safeWorkspacePath(config.WorkspaceRoot, candidate.Identifier)
	if err != nil {
		return true, err
	}
	branch, _ := currentGitBranch(workspace)
	if existing, ok := reusableRunRecord(workspace); ok {
		if feedbackRetryAvailable(workspace, candidate, existing, config) {
			log("%s has terminal artifact but PR feedback is pending; retrying existing PR %s", candidate.Identifier, existing.PRURL)
		} else {
			log("%s already has terminal run artifact status=%s pr=%s; skipping duplicate work", candidate.Identifier, existing.Status, existing.PRURL)
			return true, nil
		}
	}
	lock, releaseLock, err := acquireRunLock(workspace, candidate, branch, time.Now())
	if err != nil {
		if errors.Is(err, errRunLocked) {
			log("%v", err)
			return false, nil
		}
		return true, err
	}
	defer releaseLock()
	branch = lock.Branch
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
	if isEmptyIgnoringRunLock(workspace) && strings.TrimSpace(config.AfterCreate) != "" {
		if err := sh.RunWithTimeout(config.AfterCreate, workspace, config.Budget.CommandTimeout); err != nil {
			if errors.Is(err, sh.ErrCommandTimeout) {
				_ = client.createComment(candidate.ID, renderBudgetFailureComment(err.Error()))
				writeRunRecord(workspace, runRecordFor(candidate, workspace, config.PiCommand, "", runStarted, time.Now(), nil, nil, "", runAttemptStatusTimeout, err.Error(), config.Budget.Active(), err.Error()))
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
				writeRunRecord(workspace, runRecordFor(candidate, workspace, config.PiCommand, "", runStarted, time.Now(), nil, nil, "", runAttemptStatusTimeout, err.Error(), config.Budget.Active(), err.Error()))
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
	prompt := renderPrompt(wf.Body, *candidate, 1) + fmt.Sprintf("\n\nLinear issue description:\n%s%s\n\n%s\n\n%s\n\nPi Symphony runner constraints:\n- Follow the Linear issue description exactly; do not infer broader implementation work from the title alone.\n- If GitHub PR feedback is present, address that feedback in the existing PR branch rather than starting unrelated work.\n- If required information is missing or the ticket is ambiguous/unsafe to implement, output NEEDS_INFO followed by numbered questions instead of guessing.\n- Run exactly once; do not ask for continuation.\n- Keep context usage minimal.\n- Create or update exactly one PR from branch %s into base branch %s; never target another base branch.\n- Before opening or updating a PR, perform a focused self-review of the final diff for scope, secrets, validation, tenant/security risk, unrelated files, and behavior-contract evidence; fix any clear findings before PR handoff.\n- Stop after a scoped diff, validation notes, and PR handoff.\n- Do not post verbose free-form GitHub PR comments unless explicitly needed; the runner posts or updates the deterministic handoff summary when it detects the PR URL.\n- The runner will move the Linear issue to %s after it detects the PR URL, or to %s when NEEDS_INFO is detected.\n", candidate.Description, feedbackBlock, ticketContractPrompt(), behaviorContractPreflightPrompt(), expectedWorkspaceBranch(candidate.Identifier), config.BaseBranch, config.HandoffState, config.NeedsInfoState)
	promptPath := filepath.Join(workspace, ".pi-symphony-prompt.md")
	if err := os.WriteFile(promptPath, []byte(prompt), 0o600); err != nil {
		return true, err
	}

	githubEnv, githubAuth, err := githubAppEnvFromEnvironment()
	if err != nil {
		now := time.Now()
		writeRunRecord(workspace, runRecordFor(candidate, workspace, config.PiCommand, "github_app_error", now, now, nil, nil, "", runAttemptStatusFailed, err.Error(), config.Budget.Active(), ""))
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
	heartbeatRunLock(workspace, time.Now())

	piStart := time.Now()
	piOutput, err := sh.CaptureEnvWithOutputTimeout(fmt.Sprintf("%s @%s", config.PiCommand, sh.Quote(promptPath)), workspace, githubEnv, true, config.Budget.PiTimeout)
	piEnded := time.Now()
	if err != nil {
		status := runAttemptStatusFailed
		if errors.Is(err, sh.ErrCommandTimeout) {
			status = runAttemptStatusTimeout
			if commentErr := client.createComment(candidate.ID, renderBudgetFailureComment(err.Error())); commentErr != nil {
				log("failed to comment on %s: %v", candidate.Identifier, commentErr)
			}
		}
		writeRunRecord(workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, piEnded, parseUsage(piOutput), nil, firstPRURL(piOutput), status, err.Error(), config.Budget.Active(), err.Error()))
		return true, err
	}
	piUsage := parseUsage(piOutput)
	if piUsage != nil {
		log("pi usage: input=%.0f output=%.0f cacheRead=%.0f total=%.0f cost=$%.4f", piUsage.Input, piUsage.Output, piUsage.CacheRead, piUsage.TotalTokens, piUsage.totalCost())
	} else {
		log("pi usage: unavailable")
	}
	prURL := firstPRURL(piOutput)
	log("pi run duration: %s", piEnded.Sub(piStart).Round(time.Second))
	if exceeded := budgetExceeded(config.Budget, piStart, piUsage); exceeded != "" {
		if err := client.createComment(candidate.ID, renderBudgetFailureComment(exceeded)); err != nil {
			log("failed to comment on %s: %v", candidate.Identifier, err)
		}
		writeRunRecord(workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, nil, prURL, runAttemptStatusBudgetExceeded, exceeded, config.Budget.Active(), exceeded))
		return true, fmt.Errorf("%s", exceeded)
	}
	if needsInfo := parseNeedsInfo(piOutput); needsInfo.NeedsInfo {
		if id := stateID(states, config.NeedsInfoState); id != "" {
			if err := client.updateIssueState(candidate.ID, id); err != nil {
				writeRunRecord(workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, nil, prURL, runAttemptStatusNeedsInfoFail, err.Error(), config.Budget.Active(), ""))
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
		writeRunRecord(workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, nil, "", runAttemptStatusNeedsInfo, strings.Join(needsInfo.Questions, "\n"), config.Budget.Active(), ""))
		log("%s needs additional information; stopped without PR handoff", candidate.Identifier)
		return true, nil
	}
	if prURL == "" {
		missingPRErr := "missing PR URL"
		writeRunRecord(workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, nil, "", runAttemptStatusFailed, missingPRErr, config.Budget.Active(), ""))
		log("%s failed without PR handoff: %s", candidate.Identifier, missingPRErr)
		return true, fmt.Errorf("%s", missingPRErr)
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
			writeRunRecord(workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, nil, prURL, status, err.Error(), config.Budget.Active(), err.Error()))
			return true, err
		}
	}

	if prURL != "" {
		if reason, err := validatePRForHandoff(config, candidate, prURL); err != nil {
			writeRunRecord(workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, nil, prURL, runAttemptStatusFailed, err.Error(), config.Budget.Active(), ""))
			return true, err
		} else if reason != "" {
			review := &reviewResult{Status: runAttemptStatusFailed, Findings: reason}
			if closeErr := closeInvalidPR(prURL, reason); closeErr != nil {
				log("failed to close invalid PR %s: %v", prURL, closeErr)
			}
			if id := stateID(states, config.ReadyState); id != "" {
				if err := client.updateIssueState(candidate.ID, id); err != nil {
					writeRunRecord(workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, review, prURL, runAttemptStatusReviewFailed, err.Error(), config.Budget.Active(), ""))
					return true, err
				}
			}
			comment := fmt.Sprintf("Go/Pi PR sanity check failed; moved back to %s.\n\nPR: %s\nReason: %s", config.ReadyState, prURL, reason)
			if err := client.createComment(candidate.ID, comment); err != nil {
				log("failed to comment on %s: %v", candidate.Identifier, err)
			}
			writeRunRecord(workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, review, prURL, runAttemptStatusReviewFailed, "pr sanity check failed", config.Budget.Active(), ""))
			log("PR sanity check failed for %s; moved back to %s: %s", candidate.Identifier, config.ReadyState, reason)
			return true, nil
		}
	}

	var review *reviewResult
	if prURL != "" && config.ReviewCommand != "" {
		reviewResult, err := runReview(config.ReviewCommand, workspace, candidate, prURL, githubEnv, config.Budget.ReviewTimeout)
		review = reviewResult
		if err != nil {
			status := runAttemptStatusReviewFailed
			if errors.Is(err, sh.ErrCommandTimeout) {
				status = runAttemptStatusTimeout
				if commentErr := client.createComment(candidate.ID, renderBudgetFailureComment(err.Error())); commentErr != nil {
					log("failed to comment on %s: %v", candidate.Identifier, commentErr)
				}
			}
			writeRunRecord(workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, review, prURL, status, err.Error(), config.Budget.Active(), err.Error()))
			return true, err
		}
		if exceeded := budgetExceeded(config.Budget, piStart, piUsage, review.Usage); exceeded != "" {
			if err := client.createComment(candidate.ID, renderBudgetFailureComment(exceeded)); err != nil {
				log("failed to comment on %s: %v", candidate.Identifier, err)
			}
			writeRunRecord(workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, review, prURL, runAttemptStatusBudgetExceeded, exceeded, config.Budget.Active(), exceeded))
			return true, fmt.Errorf("%s", exceeded)
		}
		if review != nil && review.Status != "passed" && !reviewFailureRoutesToHumanHandoff(review, prURL) {
			if id := stateID(states, config.ReadyState); id != "" {
				if err := client.updateIssueState(candidate.ID, id); err != nil {
					writeRunRecord(workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, review, prURL, runAttemptStatusReviewFailed, err.Error(), config.Budget.Active(), ""))
					return true, err
				}
			}
			comment := fmt.Sprintf("Go/Pi review did not pass; moved back to %s.\n\nPR: %s\nReview status: %s\nFindings:\n%s", config.ReadyState, prURL, review.Status, review.Findings)
			if err := client.createComment(candidate.ID, comment); err != nil {
				log("failed to comment on %s: %v", candidate.Identifier, err)
			}
			writeRunRecord(workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, review, prURL, runAttemptStatusReviewFailed, "review did not pass", config.Budget.Active(), ""))
			log("review did not pass for %s; moved back to %s", candidate.Identifier, config.ReadyState)
			return true, nil
		}
		if reviewFailureRoutesToHumanHandoff(review, prURL) {
			log("review failed for %s with missing evidence only; routing to %s", candidate.Identifier, config.HandoffState)
		}
	}
	if prURL != "" {
		summary := handoffSummary{IssueIdentifier: candidate.Identifier, IssueTitle: candidate.Title, IssueURL: candidate.URL, PRURL: prURL, PiUsage: piUsage, Review: review, Duration: time.Since(piStart), Validation: validationLines(piOutput), FollowUps: followUpLines(review)}
		if err := postOrUpdatePRHandoffComment(summary); err != nil {
			log("failed to post GitHub handoff comment for %s: %v", prURL, err)
		}
		if id := stateID(states, config.HandoffState); id != "" {
			if err := client.updateIssueState(candidate.ID, id); err != nil {
				writeRunRecord(workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, review, prURL, runAttemptStatusFailed, err.Error(), config.Budget.Active(), ""))
				return true, err
			}
			log("moved %s to %s", candidate.Identifier, config.HandoffState)
		}
		comment := renderLinearHandoffComment(summary)
		if err := client.createComment(candidate.ID, comment); err != nil {
			log("failed to comment on %s: %v", candidate.Identifier, err)
		}
	}
	writeRunRecord(workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, review, prURL, runAttemptStatusSuccess, "", config.Budget.Active(), ""))
	log("completed one Pi run for %s; inspect %s", candidate.Identifier, workspace)
	return true, nil
}

var openPRsByIssueForSelection = openPRsByIssue
