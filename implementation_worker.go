package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/weskor/agent-machine/internal/agentruntime"
	sh "github.com/weskor/agent-machine/internal/shell"
	"github.com/weskor/agent-machine/internal/state"
)

type implementationWorker struct {
	client          linearClient
	project         project
	config          runnerConfig
	stateStore      *state.Store
	candidate       *issue
	selectedPR      *pullRequestSummary
	states          []workflowState
	workspace       string
	branch          string
	progressStarted time.Time
	runStarted      time.Time
}

type implementationWorkerResult struct {
	PRURL      string
	Usage      *usage
	Output     string
	Validation []string
	Started    time.Time
	Terminal   bool
}

func (w implementationWorker) Prepare(ctx context.Context) error {
	linearStatus := linearStatusWorker{client: w.client, candidate: w.candidate, states: w.states}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(w.workspace, 0o755); err != nil {
		return err
	}
	prepared := runProgressForIssue(w.candidate, w.workspace, "workspace_prepared", w.progressStarted)
	prepared.Branch = w.branch
	writeRunProgress(w.config.WorkspaceRoot, prepared)
	if isEmptyIgnoringRunLock(w.workspace) {
		bootstrapCommand := strings.TrimSpace(w.config.AfterCreate)
		if bootstrapCommand == "" && strings.TrimSpace(w.config.RepositoryRemote) != "" {
			bootstrapCommand = fmt.Sprintf("git clone --branch %s %s .", sh.Quote(w.config.BaseBranch), sh.Quote(w.config.RepositoryRemote))
		}
		if bootstrapCommand != "" {
			if err := sh.RunWithContextTimeout(ctx, bootstrapCommand, w.workspace, w.config.Budget.CommandTimeout); err != nil {
				if errors.Is(err, sh.ErrCommandTimeout) {
					decision := commandFailureLifecycleDecision(attemptLifecyclePhaseWorkspace, err, true)
					_ = linearStatus.CommentContext(ctx, renderBudgetFailureComment(err.Error()))
					writeRunRecordWithCommandStateContext(ctx, w.stateStore, w.workspace, runRecordFor(w.candidate, w.workspace, configuredRuntimeCommand(w.config), "", w.runStarted, time.Now(), nil, nil, "", decision.Status, err.Error(), w.config.Budget.Active(), err.Error()))
				}
				return err
			}
		}
	}
	if err := ensureIsolatedWorkspaceContext(ctx, w.config.WorkspaceRoot, w.workspace, w.candidate.Identifier); err != nil {
		return err
	}
	if w.config.BeforeRun != "" {
		if err := sh.RunWithContextTimeout(ctx, w.config.BeforeRun, w.workspace, w.config.Budget.CommandTimeout); err != nil {
			if errors.Is(err, sh.ErrCommandTimeout) {
				decision := commandFailureLifecycleDecision(attemptLifecyclePhaseWorkspace, err, true)
				_ = linearStatus.CommentContext(ctx, renderBudgetFailureComment(err.Error()))
				writeRunRecordWithCommandStateContext(ctx, w.stateStore, w.workspace, runRecordFor(w.candidate, w.workspace, configuredRuntimeCommand(w.config), "", w.runStarted, time.Now(), nil, nil, "", decision.Status, err.Error(), w.config.Budget.Active(), err.Error()))
			}
			return err
		}
	}
	if w.selectedPR != nil && w.selectedPR.ReviewDecision == "CHANGES_REQUESTED" {
		feedback, err := collectPRFeedback(w.selectedPR.URL, w.selectedPR.Number)
		if err != nil {
			return err
		}
		if err := writePRFeedback(w.workspace, w.selectedPR.Number, feedback); err != nil {
			return err
		}
		log("captured PR feedback for %s before retrying %s", w.selectedPR.URL, w.candidate.Identifier)
	}

	feedback, err := readPRFeedback(w.workspace)
	if err != nil {
		return err
	}
	feedback = repairReviewFailedPromptFeedback(w.stateStore, w.candidate.Identifier, feedback)
	prompt := implementationPrompt(w.project.Prompt, w.candidate, feedback, w.config)
	promptPath := filepath.Join(w.workspace, ".am-prompt.md")
	if err := os.WriteFile(promptPath, []byte(prompt), 0o600); err != nil {
		return err
	}
	return nil
}

func (w implementationWorker) Run(ctx context.Context, githubEnv map[string]string, githubAuth string) (implementationWorkerResult, error) {
	linearStatus := linearStatusWorker{client: w.client, candidate: w.candidate, states: w.states}
	promptPath := filepath.Join(w.workspace, ".am-prompt.md")
	runtime, err := newAgentRuntime(w.config.RuntimeProvider)
	if err != nil {
		return implementationWorkerResult{}, err
	}
	attempt, err := runtime.StartAttempt(ctx, agentruntime.StartAttemptInput{IssueID: w.candidate.ID, IssueIdentifier: w.candidate.Identifier, Workspace: w.workspace, Branch: w.branch, ExpectedBranch: expectedWorkspaceBranch(w.candidate.Identifier), Attempt: 1, WorkingDir: w.workspace, Command: configuredRuntimeCommand(w.config), PromptPath: promptPath, Timeouts: agentruntime.AttemptTimeouts{WallClock: w.config.Budget.WallClock, Command: w.config.Budget.CommandTimeout, Review: w.config.Budget.ReviewTimeout}, Environment: githubEnv})
	if err != nil {
		return implementationWorkerResult{}, err
	}
	emitRunAttemptEventContext(ctx, w.stateStore, state.EventRuntimeStarted, w.candidate, w.branch, map[string]any{"attempt_id": attempt.ID})
	implementing := runProgressForIssue(w.candidate, w.workspace, "implementing", w.progressStarted)
	implementing.Branch = w.branch
	writeRunProgress(w.config.WorkspaceRoot, implementing)
	piResult, err := runtime.RunAttempt(ctx, attempt.ID, agentruntime.RunAttemptInput{Command: configuredRuntimeCommand(w.config), PromptPath: promptPath, WorkingDir: w.workspace, Timeout: w.config.Budget.RuntimeDuration(), Environment: githubEnv}, agentruntime.NoopSink{})
	piEnvelope := normalizedAttemptEnvelope(piResult)
	piStart := piResult.StartedAt
	piEnded := piResult.EndedAt
	result := implementationWorkerResult{PRURL: piEnvelope.PRURL, Usage: usageFromRuntime(piEnvelope.Usage), Output: piEnvelope.RawOutput, Validation: append([]string(nil), piEnvelope.Validation...), Started: piStart}
	emitRunAttemptEventContext(ctx, w.stateStore, state.EventRuntimeFinished, w.candidate, w.branch, map[string]any{"attempt_id": attempt.ID, "outcome": piEnvelope.RuntimeOutcome, "pr_url": piEnvelope.PRURL, "error": errorString(err)})
	if err != nil {
		timeout := piEnvelope.RuntimeOutcome == agentruntime.AttemptOutcomeTimeout || errors.Is(err, sh.ErrCommandTimeout)
		decision := commandFailureLifecycleDecision(attemptLifecyclePhaseImplementation, err, timeout)
		if timeout {
			if commentErr := linearStatus.CommentContext(ctx, renderBudgetFailureComment(err.Error())); commentErr != nil {
				log("failed to comment on %s: %v", w.candidate.Identifier, commentErr)
			}
		}
		writeRunRecordWithCommandStateContext(ctx, w.stateStore, w.workspace, runRecordFor(w.candidate, w.workspace, configuredRuntimeCommand(w.config), githubAuth, piStart, piEnded, result.Usage, nil, piEnvelope.PRURL, decision.Status, err.Error(), w.config.Budget.Active(), err.Error()))
		result.Terminal = true
		return result, err
	}
	if result.Usage != nil {
		log("runtime usage: input=%.0f output=%.0f cacheRead=%.0f total=%.0f cost=$%.4f", result.Usage.Input, result.Usage.Output, result.Usage.CacheRead, result.Usage.TotalTokens, result.Usage.TotalCost())
	} else {
		log("runtime usage: unavailable")
	}
	validating := runProgressForIssue(w.candidate, w.workspace, "validating", w.progressStarted)
	validating.Branch = w.branch
	validating.PRURL = result.PRURL
	writeRunProgress(w.config.WorkspaceRoot, validating)
	log("runtime run duration: %s", piEnded.Sub(piStart).Round(time.Second))
	if exceeded := budgetExceeded(w.config.Budget, piStart, result.Usage); exceeded != "" {
		decision := budgetLifecycleDecision(attemptLifecyclePhaseImplementation, result.PRURL, exceeded)
		if err := linearStatus.CommentContext(ctx, renderBudgetFailureComment(exceeded)); err != nil {
			log("failed to comment on %s: %v", w.candidate.Identifier, err)
		}
		writeRunRecordWithCommandStateContext(ctx, w.stateStore, w.workspace, runRecordFor(w.candidate, w.workspace, configuredRuntimeCommand(w.config), githubAuth, piStart, time.Now(), result.Usage, nil, result.PRURL, decision.Status, exceeded, w.config.Budget.Active(), exceeded))
		result.Terminal = true
		return result, fmt.Errorf("%s", exceeded)
	}
	if len(piEnvelope.NeedsInfoQuestions) > 0 {
		moved, err := linearStatus.MoveToContext(ctx, w.config.NeedsInfoState)
		if err != nil {
			decision := needsInfoLifecycleDecision(piEnvelope.NeedsInfoQuestions, runAttemptStatusNeedsInfoFail, err.Error())
			writeRunRecordWithCommandStateContext(ctx, w.stateStore, w.workspace, runRecordFor(w.candidate, w.workspace, configuredRuntimeCommand(w.config), githubAuth, piStart, time.Now(), result.Usage, nil, result.PRURL, decision.Status, err.Error(), w.config.Budget.Active(), ""))
			result.Terminal = true
			return result, err
		}
		if !moved {
			log("needs-info state %q was not found for %s", w.config.NeedsInfoState, w.candidate.Identifier)
		}
		comment := renderNeedsInfoComment(piEnvelope.NeedsInfoQuestions)
		if err := linearStatus.CommentContext(ctx, comment); err != nil {
			log("failed to comment on %s: %v", w.candidate.Identifier, err)
		}
		decision := needsInfoLifecycleDecision(piEnvelope.NeedsInfoQuestions, "", "")
		if err := writeRunRecordWithCommandStateContext(ctx, w.stateStore, w.workspace, runRecordFor(w.candidate, w.workspace, configuredRuntimeCommand(w.config), githubAuth, piStart, time.Now(), result.Usage, nil, "", decision.Status, strings.Join(piEnvelope.NeedsInfoQuestions, "\n"), w.config.Budget.Active(), "")); err != nil {
			result.Terminal = true
			return result, err
		}
		log("%s needs additional information; stopped without PR handoff", w.candidate.Identifier)
		result.Terminal = true
		return result, nil
	}
	if w.config.AfterRun != "" {
		if err := sh.RunWithContextTimeout(ctx, w.config.AfterRun, w.workspace, w.config.Budget.CommandTimeout); err != nil {
			timeout := errors.Is(err, sh.ErrCommandTimeout)
			decision := commandFailureLifecycleDecision(attemptLifecyclePhaseValidation, err, timeout)
			if timeout {
				if commentErr := linearStatus.CommentContext(ctx, renderBudgetFailureComment(err.Error())); commentErr != nil {
					log("failed to comment on %s: %v", w.candidate.Identifier, commentErr)
				}
			}
			writeRunRecordWithCommandStateContext(ctx, w.stateStore, w.workspace, runRecordFor(w.candidate, w.workspace, configuredRuntimeCommand(w.config), githubAuth, piStart, time.Now(), result.Usage, nil, result.PRURL, decision.Status, err.Error(), w.config.Budget.Active(), err.Error()))
			result.Terminal = true
			return result, err
		}
	}
	return result, nil
}

func implementationPrompt(projectPrompt string, candidate *issue, feedback string, config runnerConfig) string {
	feedbackBlock := ""
	if strings.TrimSpace(feedback) != "" {
		feedbackBlock = fmt.Sprintf("\n\nCode-host PR/MR feedback to address before handoff:\n%s\n", feedback)
	}
	return renderPrompt(projectPrompt, *candidate, 1) + fmt.Sprintf("\n\nLinear issue description:\n%s%s\n\n%s\n\n%s\n\nAgent Machine runner constraints:\n- Follow the Linear issue description exactly; do not infer broader implementation work from the title alone.\n- If code-host PR/MR feedback is present, address that feedback in the existing PR branch rather than starting unrelated work.\n- If required information is missing or the ticket is ambiguous/unsafe to implement, output NEEDS_INFO followed by numbered questions instead of guessing.\n- Run exactly once; do not ask for continuation.\n- Keep context usage minimal.\n- Leave scoped code/test/doc changes in this workspace and include validation notes.\n- Do not create, update, push, or comment on a code-host PR/MR; the Agent Machine runner will commit, push, create or update exactly one PR/MR from branch %s into base branch %s, and post deterministic handoff comments.\n- Before finishing, perform a focused self-review of the final diff for scope, secrets, validation, tenant/security risk, unrelated files, and behavior-contract evidence; fix any clear findings.\n- Stop after the scoped diff and validation notes.\n- The runner will move the Linear issue to %s after runner PR/MR handoff, or to %s when NEEDS_INFO is detected.\n", candidate.Description, feedbackBlock, ticketContractPrompt(), behaviorContractPreflightPrompt(), expectedWorkspaceBranch(candidate.Identifier), config.BaseBranch, config.HandoffState, config.NeedsInfoState)
}
