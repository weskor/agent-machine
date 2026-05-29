package main

import (
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
