package main

import (
	"time"

	artifactio "github.com/weskor/agent-machine/internal/artifacts"
	"github.com/weskor/agent-machine/internal/attemptlifecycle"
	"github.com/weskor/agent-machine/internal/attemptoutcome"
	cleanuppolicy "github.com/weskor/agent-machine/internal/cleanup"
	"github.com/weskor/agent-machine/internal/domain"
	gatepkg "github.com/weskor/agent-machine/internal/gate"
	"github.com/weskor/agent-machine/internal/runprogress"
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

type evaluationArtifact = artifactio.EvaluationArtifact

type runProgressSnapshot = runprogress.Snapshot

type cleanupResult = cleanuppolicy.Decision

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

const evaluationArtifactName = artifactio.EvaluationName

const (
	runProgressArtifactName             = runprogress.ArtifactName
	prHandoffPendingPayloadArtifactName = runprogress.PRHandoffPendingPayloadName
	reviewPendingPayloadArtifactName    = runprogress.ReviewPendingPayloadName
	handoffPendingPayloadArtifactName   = runprogress.HandoffPendingPayloadName
	runProgressPhasePRHandoffPending    = runprogress.PhasePRHandoffPending
	runProgressPhaseReviewPending       = runprogress.PhaseReviewPending
	runProgressPhaseHandoffPending      = runprogress.PhaseHandoffPending
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

func cleanupGateResult(decision cleanupResult) gatepkg.Result {
	return cleanuppolicy.GateResult(decision)
}

func cleanupDeletionResult(decision cleanupResult, fallback string) string {
	return cleanuppolicy.DeletionResult(decision, fallback)
}

func cleanupCategoryForTerminalStatus(status string) string {
	return cleanuppolicy.CategoryForTerminalStatus(status)
}

func terminalRunStatus(status string) bool {
	return cleanuppolicy.TerminalRunStatus(status)
}

func runRecordFor(candidate *issue, workspace, runtimeCommand, githubAuth string, startedAt, endedAt time.Time, runtimeUsage *usage, review *reviewResult, prURL, status, errorMessage string, budget *runBudget, budgetExceeded string) runRecord {
	return attemptoutcome.RecordFor(candidate, workspace, runtimeCommand, githubAuth, startedAt, endedAt, runtimeUsage, review, prURL, status, errorMessage, budget, budgetExceeded)
}

func hasString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}
