package main

import (
	"time"

	"github.com/weskor/agent-machine/internal/attemptlifecycle"
	"github.com/weskor/agent-machine/internal/attemptoutcome"
	"github.com/weskor/agent-machine/internal/domain"
	"github.com/weskor/agent-machine/internal/runtimeadapter"
	"github.com/weskor/agent-machine/internal/scopeguard"
)

type runBudget = domain.Budget

type project = domain.Project

type runRecord = domain.RunRecord

type runLock = domain.RunLock

type reviewResult = domain.ReviewResult

type usage = domain.Usage

type usageCost = domain.UsageCost

type issue = domain.Issue

type workflowState = domain.WorkflowState

type runnerConfig = domain.RunnerConfig

type scopeGuardResult = scopeguard.Result

type runAttemptOutcome = attemptoutcome.Outcome

const (
	runAttemptStatusSuccess        = attemptoutcome.StatusSuccess
	runAttemptStatusFailed         = attemptoutcome.StatusFailed
	runAttemptStatusReviewFailed   = attemptoutcome.StatusReviewFailed
	runAttemptStatusReviewNotReady = attemptoutcome.StatusReviewNotReady
	runAttemptStatusGitHubAppError = attemptoutcome.StatusGitHubAppError
	runAttemptStatusNeedsInfo      = attemptoutcome.StatusNeedsInfo
	runAttemptStatusNeedsInfoFail  = attemptoutcome.StatusNeedsInfoFail
	runAttemptStatusTimeout        = attemptoutcome.StatusTimeout
	runAttemptStatusBudgetExceeded = attemptoutcome.StatusBudgetExceeded
)

const (
	runtimeProviderPiCLI     = runtimeadapter.ProviderPiCLI
	runtimeProviderCodexCLI  = runtimeadapter.ProviderCodexCLI
	runtimeProviderClaudeCLI = runtimeadapter.ProviderClaudeCLI
)

type attemptLifecyclePhase = attemptlifecycle.Phase

const (
	attemptLifecyclePhasePreflight       = attemptlifecycle.PhasePreflight
	attemptLifecyclePhaseWorkspace       = attemptlifecycle.PhaseWorkspace
	attemptLifecyclePhaseImplementation  = attemptlifecycle.PhaseImplementation
	attemptLifecyclePhaseNeedsInfo       = attemptlifecycle.PhaseNeedsInfo
	attemptLifecyclePhaseValidation      = attemptlifecycle.PhaseValidation
	attemptLifecyclePhaseScopeGuard      = attemptlifecycle.PhaseScopeGuard
	attemptLifecyclePhaseHandoff         = attemptlifecycle.PhaseHandoff
	attemptLifecyclePhaseReviewReadiness = attemptlifecycle.PhaseReviewReadiness
	attemptLifecyclePhaseReview          = attemptlifecycle.PhaseReview
	attemptLifecyclePhaseSuccess         = attemptlifecycle.PhaseSuccess
)

type attemptLifecycleInput = attemptlifecycle.Input

type attemptLifecycleDecision = attemptlifecycle.Decision

func decideAttemptLifecycle(input attemptLifecycleInput) attemptLifecycleDecision {
	return attemptlifecycle.Decide(input)
}

func runRecordFor(candidate *issue, workspace, runtimeCommand, githubAuth string, startedAt, endedAt time.Time, runtimeUsage *usage, review *reviewResult, prURL, status, errorMessage string, budget *runBudget, budgetExceeded string) runRecord {
	return attemptoutcome.RecordFor(candidate, workspace, runtimeCommand, githubAuth, startedAt, endedAt, runtimeUsage, review, prURL, status, errorMessage, budget, budgetExceeded)
}
