package main

import (
	"github.com/weskor/agent-machine/internal/attemptlifecycle"
	"github.com/weskor/agent-machine/internal/domain"
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
