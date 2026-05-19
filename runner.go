package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// run dispatches the selected mode. The default remains one-shot for targeted
// validation, while --continuous runs the full pilot loop in one command.
func run() error {
	loadDotEnvLocal(".env.local")

	workflowPath := "WORKFLOW.md"
	mergeApproved := false
	repair := false
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
	if workflowDir := filepath.Dir(workflowPath); workflowDir != "." && workflowDir != "" {
		loadDotEnvLocal(filepath.Join(workflowDir, ".env.local"))
	}

	wf, err := readWorkflow(workflowPath)
	if err != nil {
		return err
	}

	apiKey := scalar(wf.YAML, "  api_key", "")
	if apiKey == "" {
		return errors.New("LINEAR_API_KEY is required")
	}
	config := runnerConfig{
		WorkflowPath:   workflowPath,
		ProjectSlug:    scalar(wf.YAML, "  project_slug", ""),
		WorkspaceRoot:  scalar(wf.YAML, "  root", ""),
		RunningState:   scalar(wf.YAML, "  running_state", "In Progress"),
		HandoffState:   scalar(wf.YAML, "  handoff_state", "Human Review"),
		DoneState:      scalar(wf.YAML, "  done_state", "Done"),
		NeedsInfoState: scalar(wf.YAML, "  needs_info_state", "Needs Info"),
		ReadyState:     "Ready for Agent",
		BaseBranch:     "develop",
		ActiveStates:   listUnder(wf.YAML, "active_states"),
	}
	piYAML := section(wf.YAML, "pi")
	config.PiCommand = commandUnder(piYAML, "command", "pi --print --no-session --thinking low")
	config.ReviewCommand = commandUnder(piYAML, "review_command", "")
	config.AfterCreate = blockUnder(piYAML, "after_create")
	config.BeforeRun = scalar(piYAML, "  before_run", "")
	config.AfterRun = scalar(piYAML, "  after_run", "")
	config.Budget = parseBudget(wf.YAML)
	if config.Budget.GitHubTimeout > 0 {
		defaultGitHubCommandTimeout = config.Budget.GitHubTimeout
	}

	if config.ProjectSlug == "" || config.WorkspaceRoot == "" {
		return errors.New("WORKFLOW.md must configure tracker.project_slug and workspace.root")
	}

	client := linearClient{apiKey: apiKey, endpoint: scalar(wf.YAML, "  endpoint", "https://api.linear.app/graphql")}
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

// runContinuous keeps the pilot moving in one operator command with independent
// lanes: merge/check handling continues while the work lane is busy with an
// implementation attempt.
func runContinuous(client linearClient, wf workflow, config runnerConfig, maxCycles int) error {
	log("mode=continuous; lanes=merge,work; project=%s; states=%s", config.ProjectSlug, strings.Join(config.ActiveStates, ", "))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	scheduler := continuousScheduler{
		maxCycles: maxCycles,
		lanes: []continuousLane{
			{
				name:       "merge",
				idleDelay:  30 * time.Second,
				continuous: true,
				run: func() (bool, error) {
					doneIssues, err := client.issueIdentifiersByState(config.ProjectSlug, config.DoneState)
					if err != nil {
						return false, err
					}
					if err := cleanupWorkspaces(config.WorkspaceRoot, cleanupOptions{Apply: true, DoneIssues: doneIssues}); err != nil {
						return false, err
					}
					return true, mergeApprovedPRs(client, config)
				},
			},
			{
				name:      "work",
				idleDelay: 60 * time.Second,
				run: func() (bool, error) {
					return runOne(client, wf, config)
				},
			},
		},
	}
	return scheduler.run(ctx)
}

type continuousLane struct {
	name       string
	idleDelay  time.Duration
	continuous bool
	run        func() (bool, error)
}

type continuousScheduler struct {
	lanes     []continuousLane
	maxCycles int
}

func (s continuousScheduler) run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errs := make(chan error, len(s.lanes))
	var wg sync.WaitGroup
	for _, lane := range s.lanes {
		lane := lane
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := runContinuousLane(ctx, lane, s.maxCycles); err != nil {
				errs <- err
				cancel()
			}
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		select {
		case err := <-errs:
			return err
		default:
			return nil
		}
	case err := <-errs:
		cancel()
		<-done
		return err
	}
}

func runContinuousLane(ctx context.Context, lane continuousLane, maxCycles int) error {
	cycles := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		log("lane=%s cycle=%d starting", lane.name, cycles+1)
		didWork, err := lane.run()
		if err != nil {
			return err
		}
		cycles++
		if maxCycles > 0 && cycles >= maxCycles {
			log("lane=%s completed %d continuous cycle(s)", lane.name, cycles)
			return nil
		}

		delay := time.Duration(0)
		if lane.continuous || !didWork {
			delay = lane.idleDelay
		}
		if delay > 0 {
			if !didWork {
				log("lane=%s idle; sleeping %s", lane.name, delay)
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(delay):
			}
		}
	}
}

// runOne executes a single Linear issue attempt, including optional review
// handoff. It returns false when there is no eligible issue to process.
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
		if err := shellWithTimeout(config.AfterCreate, workspace, config.Budget.CommandTimeout); err != nil {
			if errors.Is(err, errCommandTimeout) {
				_ = client.createComment(candidate.ID, renderBudgetFailureComment(err.Error()))
				writeRunRecord(workspace, runRecordFor(candidate, workspace, config.PiCommand, "", runStarted, time.Now(), nil, nil, "", "timeout", err.Error(), config.Budget.active(), err.Error()))
			}
			return true, err
		}
	}
	if err := ensureIsolatedWorkspace(config.WorkspaceRoot, workspace, candidate.Identifier); err != nil {
		return true, err
	}
	if config.BeforeRun != "" {
		if err := shellWithTimeout(config.BeforeRun, workspace, config.Budget.CommandTimeout); err != nil {
			if errors.Is(err, errCommandTimeout) {
				_ = client.createComment(candidate.ID, renderBudgetFailureComment(err.Error()))
				writeRunRecord(workspace, runRecordFor(candidate, workspace, config.PiCommand, "", runStarted, time.Now(), nil, nil, "", "timeout", err.Error(), config.Budget.active(), err.Error()))
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
	prompt := renderPrompt(wf.Body, *candidate, 1) + fmt.Sprintf("\n\nLinear issue description:\n%s%s\n\n%s\n\n%s\n\nPi Symphony runner constraints:\n- Follow the Linear issue description exactly; do not infer broader implementation work from the title alone.\n- If GitHub PR feedback is present, address that feedback in the existing PR branch rather than starting unrelated work.\n- If required information is missing or the ticket is ambiguous/unsafe to implement, output NEEDS_INFO followed by numbered questions instead of guessing.\n- Run exactly once; do not ask for continuation.\n- Keep context usage minimal.\n- Create or update exactly one PR from branch %s into base branch %s; never target main.\n- Before opening or updating a PR, perform a focused self-review of the final diff for scope, secrets, validation, tenant/security risk, unrelated files, and behavior-contract evidence; fix any clear findings before PR handoff.\n- Stop after a scoped diff, validation notes, and PR handoff.\n- Do not post verbose free-form GitHub PR comments unless explicitly needed; the runner posts or updates the deterministic handoff summary when it detects the PR URL.\n- The runner will move the Linear issue to %s after it detects the PR URL, or to %s when NEEDS_INFO is detected.\n", candidate.Description, feedbackBlock, ticketContractPrompt(), behaviorContractPreflightPrompt(), expectedWorkspaceBranch(candidate.Identifier), config.BaseBranch, config.HandoffState, config.NeedsInfoState)
	promptPath := filepath.Join(workspace, ".pi-symphony-prompt.md")
	if err := os.WriteFile(promptPath, []byte(prompt), 0o600); err != nil {
		return true, err
	}

	githubEnv, githubAuth, err := githubAppEnvFromEnvironment()
	if err != nil {
		now := time.Now()
		writeRunRecord(workspace, runRecordFor(candidate, workspace, config.PiCommand, "github_app_error", now, now, nil, nil, "", "failed", err.Error(), config.Budget.active(), ""))
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
	piOutput, err := shellCaptureEnvWithOutputTimeout(fmt.Sprintf("%s @%s", config.PiCommand, shellQuote(promptPath)), workspace, githubEnv, true, config.Budget.PiTimeout)
	piEnded := time.Now()
	if err != nil {
		status := "failed"
		if errors.Is(err, errCommandTimeout) {
			status = "timeout"
			if commentErr := client.createComment(candidate.ID, renderBudgetFailureComment(err.Error())); commentErr != nil {
				log("failed to comment on %s: %v", candidate.Identifier, commentErr)
			}
		}
		writeRunRecord(workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, piEnded, parseUsage(piOutput), nil, firstPRURL(piOutput), status, err.Error(), config.Budget.active(), err.Error()))
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
		writeRunRecord(workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, nil, prURL, "budget_exceeded", exceeded, config.Budget.active(), exceeded))
		return true, fmt.Errorf("%s", exceeded)
	}
	if needsInfo := parseNeedsInfo(piOutput); needsInfo.NeedsInfo {
		if id := stateID(states, config.NeedsInfoState); id != "" {
			if err := client.updateIssueState(candidate.ID, id); err != nil {
				writeRunRecord(workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, nil, prURL, "needs_info_failed", err.Error(), config.Budget.active(), ""))
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
		writeRunRecord(workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, nil, "", "needs_info", strings.Join(needsInfo.Questions, "\n"), config.Budget.active(), ""))
		log("%s needs additional information; stopped without PR handoff", candidate.Identifier)
		return true, nil
	}
	if config.AfterRun != "" {
		if err := shellWithTimeout(config.AfterRun, workspace, config.Budget.CommandTimeout); err != nil {
			status := "failed"
			if errors.Is(err, errCommandTimeout) {
				status = "timeout"
				if commentErr := client.createComment(candidate.ID, renderBudgetFailureComment(err.Error())); commentErr != nil {
					log("failed to comment on %s: %v", candidate.Identifier, commentErr)
				}
			}
			writeRunRecord(workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, nil, prURL, status, err.Error(), config.Budget.active(), err.Error()))
			return true, err
		}
	}

	if prURL != "" {
		if reason, err := validatePRForHandoff(config, candidate, prURL); err != nil {
			writeRunRecord(workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, nil, prURL, "failed", err.Error(), config.Budget.active(), ""))
			return true, err
		} else if reason != "" {
			review := &reviewResult{Status: "failed", Findings: reason}
			if closeErr := closeInvalidPR(prURL, reason); closeErr != nil {
				log("failed to close invalid PR %s: %v", prURL, closeErr)
			}
			if id := stateID(states, config.ReadyState); id != "" {
				if err := client.updateIssueState(candidate.ID, id); err != nil {
					writeRunRecord(workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, review, prURL, "review_failed", err.Error(), config.Budget.active(), ""))
					return true, err
				}
			}
			comment := fmt.Sprintf("Go/Pi PR sanity check failed; moved back to %s.\n\nPR: %s\nReason: %s", config.ReadyState, prURL, reason)
			if err := client.createComment(candidate.ID, comment); err != nil {
				log("failed to comment on %s: %v", candidate.Identifier, err)
			}
			writeRunRecord(workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, review, prURL, "review_failed", "pr sanity check failed", config.Budget.active(), ""))
			log("PR sanity check failed for %s; moved back to %s: %s", candidate.Identifier, config.ReadyState, reason)
			return true, nil
		}
	}

	var review *reviewResult
	if prURL != "" && config.ReviewCommand != "" {
		reviewResult, err := runReview(config.ReviewCommand, workspace, candidate, prURL, githubEnv, config.Budget.ReviewTimeout)
		review = reviewResult
		if err != nil {
			status := "review_failed"
			if errors.Is(err, errCommandTimeout) {
				status = "timeout"
				if commentErr := client.createComment(candidate.ID, renderBudgetFailureComment(err.Error())); commentErr != nil {
					log("failed to comment on %s: %v", candidate.Identifier, commentErr)
				}
			}
			writeRunRecord(workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, review, prURL, status, err.Error(), config.Budget.active(), err.Error()))
			return true, err
		}
		if exceeded := budgetExceeded(config.Budget, piStart, piUsage, review.Usage); exceeded != "" {
			if err := client.createComment(candidate.ID, renderBudgetFailureComment(exceeded)); err != nil {
				log("failed to comment on %s: %v", candidate.Identifier, err)
			}
			writeRunRecord(workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, review, prURL, "budget_exceeded", exceeded, config.Budget.active(), exceeded))
			return true, fmt.Errorf("%s", exceeded)
		}
		if review != nil && review.Status != "passed" {
			if id := stateID(states, config.ReadyState); id != "" {
				if err := client.updateIssueState(candidate.ID, id); err != nil {
					writeRunRecord(workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, review, prURL, "review_failed", err.Error(), config.Budget.active(), ""))
					return true, err
				}
			}
			comment := fmt.Sprintf("Go/Pi review did not pass; moved back to %s.\n\nPR: %s\nReview status: %s\nFindings:\n%s", config.ReadyState, prURL, review.Status, review.Findings)
			if err := client.createComment(candidate.ID, comment); err != nil {
				log("failed to comment on %s: %v", candidate.Identifier, err)
			}
			writeRunRecord(workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, review, prURL, "review_failed", "review did not pass", config.Budget.active(), ""))
			log("review did not pass for %s; moved back to %s", candidate.Identifier, config.ReadyState)
			return true, nil
		}
	}
	if prURL != "" {
		summary := handoffSummary{IssueIdentifier: candidate.Identifier, IssueTitle: candidate.Title, IssueURL: candidate.URL, PRURL: prURL, PiUsage: piUsage, Review: review, Duration: time.Since(piStart), Validation: validationLines(piOutput), FollowUps: followUpLines(review)}
		if err := postOrUpdatePRHandoffComment(summary); err != nil {
			log("failed to post GitHub handoff comment for %s: %v", prURL, err)
		}
		if id := stateID(states, config.HandoffState); id != "" {
			if err := client.updateIssueState(candidate.ID, id); err != nil {
				writeRunRecord(workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, review, prURL, "failed", err.Error(), config.Budget.active(), ""))
				return true, err
			}
			log("moved %s to %s", candidate.Identifier, config.HandoffState)
		}
		comment := renderLinearHandoffComment(summary)
		if err := client.createComment(candidate.ID, comment); err != nil {
			log("failed to comment on %s: %v", candidate.Identifier, err)
		}
	}
	writeRunRecord(workspace, runRecordFor(candidate, workspace, config.PiCommand, githubAuth, piStart, time.Now(), piUsage, review, prURL, "success", "", config.Budget.active(), ""))
	log("completed one Pi run for %s; inspect %s", candidate.Identifier, workspace)
	return true, nil
}

var openPRsByIssueForSelection = openPRsByIssue

func nextRunnableCandidate(client linearClient, config runnerConfig) (*issue, *pullRequestSummary, error) {
	candidates, err := client.candidates(config.ProjectSlug, config.ActiveStates)
	if err != nil || len(candidates) == 0 {
		return nil, nil, err
	}
	prsByIssue, err := openPRsByIssueForSelection(config)
	if err != nil {
		return nil, nil, err
	}
	candidates = orderCandidates(candidates, config.ReadyState)
	blockedCount := 0
	for i := range candidates {
		pr := prsByIssue[candidates[i].Identifier]
		decision := reconcileIssue(config, candidates[i], pr)
		if decision.Lifecycle == lifecycleBlocked && strings.Contains(strings.Join(decision.Blockers, ","), "blocked label") {
			blockedCount++
			log("skipping %s: blocked label", candidates[i].Identifier)
			continue
		}
		if candidates[i].State.Name == config.ReadyState && decision.CanRun {
			return &candidates[i], pr, nil
		}
		log("skipping %s: lifecycle=%s blockers=%s next=%s", candidates[i].Identifier, decision.Lifecycle, strings.Join(decision.Blockers, "; "), decision.NextAction)
	}
	for i := range candidates {
		pr := prsByIssue[candidates[i].Identifier]
		decision := reconcileIssue(config, candidates[i], pr)
		if decision.Lifecycle == lifecycleBlocked && strings.Contains(strings.Join(decision.Blockers, ","), "blocked label") {
			continue
		}
		if decision.CanRun {
			return &candidates[i], pr, nil
		}
	}
	if blockedCount == len(candidates) {
		log("all eligible issues are blocked by labels")
		return nil, nil, nil
	}
	log("all eligible issues are waiting on prior review-failure findings, terminal run artifacts, or active locks")
	return nil, nil, nil
}

func openPRsByIssue(config runnerConfig) (map[string]*pullRequestSummary, error) {
	github, ctx, cancel, err := githubClientWithTimeout(config.Budget.MergeTimeout)
	if err != nil {
		return nil, err
	}
	defer cancel()
	prs, err := github.OpenPullRequests(ctx)
	if err != nil {
		return nil, fmt.Errorf("GitHub API open PR metadata lookup failed: %w", err)
	}
	prs = symphonyPRs(prs)
	byIssue := map[string]*pullRequestSummary{}
	for i := range prs {
		identifier := issueIdentifierFromBranch(prs[i].HeadRefName)
		if identifier == "" {
			continue
		}
		copy := prs[i]
		byIssue[identifier] = &copy
	}
	return byIssue, nil
}

func isRunnableWorkspace(workspaceRoot, identifier string) bool {
	return isRunnableWorkspaceForCandidate(workspaceRoot, issue{Identifier: identifier}, runnerConfig{ReadyState: "Ready for Agent"})
}

func isRunnableWorkspaceForCandidate(workspaceRoot string, candidate issue, config runnerConfig) bool {
	workspace := filepath.Join(workspaceRoot, candidate.Identifier)
	if hasRunLock(workspace) {
		log("skipping %s because an active run lock exists", candidate.Identifier)
		return false
	}
	if hasUnresolvedReviewFailure(workspaceRoot, candidate.Identifier) {
		return false
	}
	if record, ok := reusableRunRecord(workspace); ok {
		if feedbackRetryAvailable(workspace, &candidate, record, config) {
			log("%s has terminal artifact but captured PR feedback is available; allowing retry", candidate.Identifier)
			return true
		}
		log("skipping %s because a terminal run artifact already exists", candidate.Identifier)
		return false
	}
	return true
}

func feedbackRetryAvailable(workspace string, candidate *issue, record runRecord, config runnerConfig) bool {
	if candidate == nil || candidate.State.Name != config.ReadyState || record.Status != "success" || record.PRURL == "" {
		return false
	}
	feedback, err := readPRFeedback(workspace)
	return err == nil && strings.TrimSpace(feedback) != ""
}

func reusableRunRecord(workspace string) (runRecord, bool) {
	data, err := os.ReadFile(filepath.Join(workspace, ".pi-symphony-run.json"))
	if err != nil {
		return runRecord{}, false
	}
	var record runRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return runRecord{}, false
	}
	if record.Status == "success" && record.PRURL != "" {
		return record, true
	}
	return runRecord{}, false
}

func orderCandidates(candidates []issue, readyState string) []issue {
	ordered := append([]issue(nil), candidates...)
	sort.SliceStable(ordered, func(i, j int) bool {
		left := candidateSortKey(ordered[i], readyState)
		right := candidateSortKey(ordered[j], readyState)
		if left.stateRank != right.stateRank {
			return left.stateRank < right.stateRank
		}
		if left.safetyRank != right.safetyRank {
			return left.safetyRank < right.safetyRank
		}
		if left.priorityRank != right.priorityRank {
			return left.priorityRank < right.priorityRank
		}
		if !left.createdAt.Equal(right.createdAt) {
			return left.createdAt.Before(right.createdAt)
		}
		return ordered[i].Identifier < ordered[j].Identifier
	})
	return ordered
}

type candidateKey struct {
	stateRank    int
	safetyRank   int
	priorityRank int
	createdAt    time.Time
}

func candidateSortKey(candidate issue, readyState string) candidateKey {
	stateRank := 1
	if candidate.State.Name == readyState {
		stateRank = 0
	}
	priorityRank := candidate.Priority
	if priorityRank <= 0 {
		priorityRank = 99
	}
	createdAt, err := time.Parse(time.RFC3339, candidate.CreatedAt)
	if err != nil {
		createdAt = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return candidateKey{stateRank: stateRank, safetyRank: safetyRank(candidate), priorityRank: priorityRank, createdAt: createdAt}
}

func safetyRank(candidate issue) int {
	labels := labelSet(candidate)
	if labels["runner-safety"] || labels["harness"] {
		return 0
	}
	if labels["docs-only"] || labels["low-risk"] {
		return 1
	}
	return 2
}

func isBlockedCandidate(candidate issue) bool {
	labels := labelSet(candidate)
	return labels["blocked"] || labels["needs-info"]
}

func labelSet(candidate issue) map[string]bool {
	out := map[string]bool{}
	for _, label := range candidate.Labels.Nodes {
		out[strings.ToLower(strings.TrimSpace(label.Name))] = true
	}
	return out
}

func candidateOrderReason(candidate issue, readyState string) string {
	key := candidateSortKey(candidate, readyState)
	priority := "none"
	if candidate.Priority > 0 {
		priority = fmt.Sprintf("P%d", candidate.Priority)
	}
	labels := make([]string, 0, len(candidate.Labels.Nodes))
	for _, label := range candidate.Labels.Nodes {
		labels = append(labels, label.Name)
	}
	if len(labels) == 0 {
		labels = append(labels, "none")
	}
	return fmt.Sprintf("state_rank=%d safety_rank=%d priority=%s created_at=%s labels=%s", key.stateRank, key.safetyRank, priority, candidate.CreatedAt, strings.Join(labels, ","))
}

func hasUnresolvedReviewFailure(workspaceRoot, identifier string) bool {
	data, err := os.ReadFile(filepath.Join(workspaceRoot, identifier, ".pi-symphony-run.json"))
	if err != nil {
		return false
	}
	var record runRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return false
	}
	return record.Status == "review_failed" && record.PRURL != ""
}

type needsInfoResult struct {
	NeedsInfo bool
	Questions []string
}

func parseNeedsInfo(output string) needsInfoResult {
	text := assistantText(output)
	if text == "" {
		text = output
	}
	lines := strings.Split(text, "\n")
	found := false
	var questions []string
	for _, line := range lines {
		clean := strings.TrimSpace(line)
		if strings.Contains(clean, "NEEDS_INFO") {
			found = true
			continue
		}
		if !found || clean == "" {
			continue
		}
		trimmed := strings.TrimLeft(clean, "-• \t")
		if isNumberedQuestion(trimmed) {
			questions = append(questions, sanitizeMarkdownLine(trimmed))
		}
	}
	return needsInfoResult{NeedsInfo: found, Questions: questions}
}

func isNumberedQuestion(line string) bool {
	dot := strings.Index(line, ".")
	paren := strings.Index(line, ")")
	idx := dot
	if idx == -1 || (paren != -1 && paren < idx) {
		idx = paren
	}
	if idx <= 0 || idx > 3 || idx >= len(line)-1 {
		return false
	}
	for _, r := range line[:idx] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return strings.TrimSpace(line[idx+1:]) != ""
}

func renderNeedsInfoComment(questions []string) string {
	var builder strings.Builder
	builder.WriteString("Go/Pi run stopped because the ticket needs additional information. Please answer the questions below, then move the issue back to Ready for Agent.\n\n")
	if len(questions) == 0 {
		builder.WriteString("1. Please clarify the missing requirements so the agent can proceed safely.")
		return builder.String()
	}
	for i, question := range questions {
		fmt.Fprintf(&builder, "%d. %s\n", i+1, strings.TrimSpace(stripQuestionNumber(question)))
	}
	return truncateMarkdown(strings.TrimSpace(builder.String()), 2000)
}

func stripQuestionNumber(question string) string {
	trimmed := strings.TrimSpace(question)
	for i, r := range trimmed {
		if (r == '.' || r == ')') && i > 0 {
			return strings.TrimSpace(trimmed[i+1:])
		}
	}
	return trimmed
}

func runRecordFor(candidate *issue, workspace, piCommand, githubAuth string, startedAt, endedAt time.Time, piUsage *usage, review *reviewResult, prURL, status, errorMessage string, budget *runBudget, budgetExceeded string) runRecord {
	reviewStatus := ""
	reviewFindings := ""
	var reviewUsage *usage
	if review != nil {
		reviewStatus = review.Status
		reviewFindings = review.Findings
		reviewUsage = review.Usage
	}
	branch, _ := currentGitBranch(workspace)
	root := filepath.Dir(workspace)
	feedback, _ := readPRFeedback(workspace)
	record := runRecord{IssueIdentifier: candidate.Identifier, IssueID: candidate.ID, IssueTitle: candidate.Title, IssueURL: candidate.URL, Workspace: workspace, WorkspaceRoot: root, Branch: branch, ExpectedBranch: expectedWorkspaceBranch(candidate.Identifier), PiCommand: piCommand, GitHubAuth: githubAuth, StartedAt: startedAt, EndedAt: endedAt, DurationMS: endedAt.Sub(startedAt).Milliseconds(), PiUsage: piUsage, ReviewStatus: reviewStatus, ReviewFindings: reviewFindings, ReviewUsage: reviewUsage, PRURL: prURL, FeedbackHash: feedbackHash(feedback), Status: status, Error: errorMessage, Budget: budget, BudgetExceeded: budgetExceeded}
	record.BehaviorContractEvidence = behaviorContractEvidenceForRun(record)
	record.BehaviorContractEvidence = append(record.BehaviorContractEvidence, ticketContractEvidenceForRun(record)...)
	return record
}

func behaviorContractPreflightPrompt() string {
	return `Behavior-contract preflight for refactors, replacements, and rewrites:
- Before changing code, commands, dependencies, integrations, workflows, or state-machine logic, inventory the existing observable contract: inputs/outputs, side effects, cleanup, error handling, security/ownership assumptions, state transitions, and hidden operational contracts.
- Add a parity checklist to the PR body or tracker handoff: behavior preserved, behavior intentionally changed with justification, and unknown behavior that needs clarification.
- Use TDD or characterization tests for old observable behavior before proving the new abstraction; tests only around the new design are not enough.
- State a complexity/LOC budget before implementation: expected files touched, expected LOC direction, why any net growth is acceptable, what bespoke code is removed, and when the work must split.
- If the existing contract cannot be determined safely, output NEEDS_INFO instead of guessing.`
}

func behaviorContractEvidenceForRun(record runRecord) []string {
	evidence := []string{"implementation_prompt_required_behavior_contract_preflight"}
	if record.ReviewStatus != "" {
		evidence = append(evidence, "review_prompt_required_behavior_contract_parity_check")
	}
	if record.ReviewStatus == "passed" {
		evidence = append(evidence, "review_passed_behavior_contract_gate")
	}
	if record.ReviewStatus == "failed" {
		evidence = append(evidence, "review_failed_behavior_contract_or_scope_gate")
	}
	if strings.HasPrefix(record.Status, "needs_info") {
		evidence = append(evidence, "needs_info_used_for_unknown_behavior_contract")
	}
	if strings.TrimSpace(record.Error) != "" || strings.TrimSpace(record.ReviewFindings) != "" {
		evidence = append(evidence, "findings_recorded_for_behavior_contract_audit")
	}
	return evidence
}

func expectedWorkspaceBranch(identifier string) string {
	return "symphony/" + strings.TrimSpace(identifier) + "-workspace"
}

type prHandoffDetails struct {
	Number       int    `json:"number"`
	URL          string `json:"url"`
	BaseRefName  string `json:"baseRefName"`
	HeadRefName  string `json:"headRefName"`
	ChangedFiles int    `json:"changedFiles"`
	Additions    int    `json:"additions"`
	Deletions    int    `json:"deletions"`
}

func validatePRForHandoff(config runnerConfig, candidate *issue, prURL string) (string, error) {
	github, ctx, cancel, err := githubClientWithTimeout(config.Budget.GitHubTimeout)
	if err != nil {
		return "", err
	}
	defer cancel()
	details, err := github.PullRequestHandoffDetails(ctx, prURL)
	if err != nil {
		return "", fmt.Errorf("GitHub API PR handoff lookup failed for %s: %w", prURL, err)
	}
	return prHandoffBlockReason(config, candidate, details), nil
}

func prHandoffBlockReason(config runnerConfig, candidate *issue, details prHandoffDetails) string {
	var reasons []string
	baseBranch := strings.TrimSpace(config.BaseBranch)
	if baseBranch == "" {
		baseBranch = "develop"
	}
	if !strings.EqualFold(details.BaseRefName, baseBranch) {
		reasons = append(reasons, fmt.Sprintf("PR base branch is %q; expected %q", emptyAsUnknown(details.BaseRefName), baseBranch))
	}
	expectedBranch := expectedWorkspaceBranch(candidate.Identifier)
	if details.HeadRefName != expectedBranch {
		reasons = append(reasons, fmt.Sprintf("PR head branch is %q; expected %q", emptyAsUnknown(details.HeadRefName), expectedBranch))
	}
	if details.ChangedFiles > 80 {
		reasons = append(reasons, fmt.Sprintf("PR changes %d files, exceeding the scoped-run limit of 80", details.ChangedFiles))
	}
	if details.Additions > 5000 {
		reasons = append(reasons, fmt.Sprintf("PR adds %d lines, exceeding the scoped-run limit of 5000", details.Additions))
	}
	return strings.Join(reasons, "; ")
}

func closeInvalidPR(prURL, reason string) error {
	comment := fmt.Sprintf("Closing because the Pi Symphony runner PR sanity check failed before handoff.\n\nReason: %s\n\nDo not merge this PR as-is; retry the Linear issue only after fixing branch/base/scope controls.", reason)
	return shellWithTimeout(fmt.Sprintf("gh pr close %s --comment %s", shellQuote(prURL), shellQuote(comment)), "", defaultGitHubCommandTimeout)
}

func ensureIsolatedWorkspace(workspaceRoot, workspace, identifier string) error {
	if err := assertSafeDeletePath(workspaceRoot, workspace); err != nil {
		return err
	}
	topLevel, err := shellCaptureQuiet("git rev-parse --show-toplevel", workspace)
	if err != nil {
		return fmt.Errorf("workspace %s is not a git checkout: %w", workspace, err)
	}
	topAbs, err := filepath.Abs(strings.TrimSpace(topLevel))
	if err != nil {
		return err
	}
	workspaceAbs, err := filepath.Abs(workspace)
	if err != nil {
		return err
	}
	if filepath.Clean(topAbs) != filepath.Clean(workspaceAbs) {
		return fmt.Errorf("refusing shared git checkout: top-level %s does not match workspace %s", strings.TrimSpace(topLevel), workspace)
	}
	branch := expectedWorkspaceBranch(identifier)
	current, err := currentGitBranch(workspace)
	if err != nil {
		return err
	}
	if current == branch {
		return nil
	}
	if current != "" && strings.HasPrefix(current, "symphony/") {
		return fmt.Errorf("workspace %s is on unexpected Symphony branch %q; expected %q", workspace, current, branch)
	}
	if err := shell("git switch -C "+shellQuote(branch), workspace); err != nil {
		return err
	}
	return nil
}

func writeRunRecord(workspace string, record runRecord) {
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		log("failed to encode run record: %v", err)
		return
	}
	path := filepath.Join(workspace, ".pi-symphony-run.json")
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		log("failed to write run record: %v", err)
		return
	}
	log("wrote run record: %s", path)
	writeEvaluationArtifact(workspace, record)
}

func stateID(states []workflowState, name string) string {
	for _, state := range states {
		if state.Name == name {
			return state.ID
		}
	}
	return ""
}

func renderPrompt(template string, issue issue, attempt int) string {
	replacer := strings.NewReplacer("{{issue.identifier}}", issue.Identifier, "{{issue.title}}", issue.Title, "{{issue.description}}", issue.Description, "{{issue.url}}", issue.URL, "{{issue.state}}", issue.State.Name, "{{attempt}}", fmt.Sprint(attempt))
	return replacer.Replace(template)
}
