package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	artifactio "github.com/weskor/pi-symphony/internal/artifacts"
	"github.com/weskor/pi-symphony/internal/state"
)

// stateProjection owns domain object to SQLite row projection. Callers keep
// JSON/Markdown artifact writes and lifecycle decisions first, then delegate
// best-effort SQLite mirroring here.
type stateProjection struct{}

func (stateProjection) RunArtifact(workspace string, record runRecord, evaluation evaluationArtifact) state.RunArtifactSnapshot {
	repo, prNumber := parseGitHubPR(record.PRURL)
	reviewHash := ""
	if strings.TrimSpace(record.ReviewFindings) != "" {
		sum := sha256.Sum256([]byte(record.ReviewFindings))
		reviewHash = fmt.Sprintf("%x", sum[:])
	}
	retryReason := ""
	retryNextState := ""
	if evaluation.ShouldRetry {
		retryReason = evaluation.RootCause
		if retryReason == "" {
			retryReason = evaluation.Outcome
		}
		retryNextState = evaluation.NextAction
	}
	terminalOutcome := ""
	if terminalRunStatus(record.Status) {
		terminalOutcome = evaluation.Outcome
	}
	return state.RunArtifactSnapshot{
		IssueKey:             record.IssueIdentifier,
		IssueID:              record.IssueID,
		Attempt:              1,
		WorkspacePath:        record.Workspace,
		BranchName:           firstNonEmpty(record.Branch, record.ExpectedBranch),
		BaseBranch:           baseBranchForWorkspace(workspace),
		Status:               record.Status,
		StartedAt:            record.StartedAt,
		UpdatedAt:            record.EndedAt,
		Repository:           repo,
		PRNumber:             prNumber,
		PRURL:                record.PRURL,
		ReviewStatus:         record.ReviewStatus,
		ReviewPassed:         record.ReviewStatus == "passed",
		ReviewClassification: record.ReviewClassification,
		ReviewOutputRef:      filepath.Join(workspace, evaluationArtifactName),
		ReviewOutputHash:     reviewHash,
		MergeEligible:        evaluation.MergeEligible,
		FeedbackHash:         record.FeedbackHash,
		FeedbackNextAction:   evaluation.NextAction,
		RetryCount:           evaluation.FeedbackRetryCount,
		RetryBudgetState:     record.BudgetExceeded,
		RetryReason:          retryReason,
		RetryInputHash:       record.FeedbackHash,
		RetryNextState:       retryNextState,
		TerminalOutcome:      terminalOutcome,
		TerminalReason:       evaluation.RootCause,
		RunArtifactRef:       artifactio.RunRecordPath(workspace),
		EvaluationRef:        artifactio.EvaluationPath(workspace),
	}
}

func (stateProjection) Cleanup(decision cleanupResult, eligible bool, deletionResult string, workspaceExists bool, updatedAt time.Time) state.CleanupState {
	blockedReason := ""
	if !eligible || deletionResult == "failed" {
		blockedReason = decision.Reason
	}
	return state.CleanupState{
		IssueKey:        decision.IssueIdentifier,
		Attempt:         1,
		WorkspaceExists: workspaceExists,
		Eligible:        eligible,
		Decision:        decision.Category,
		DeletionResult:  deletionResult,
		ArtifactRef:     decision.ArtifactRef,
		BlockedReason:   blockedReason,
		UpdatedAt:       updatedAt,
	}
}

func (stateProjection) RunLockLease(lock runLock, observedAt time.Time) state.Lease {
	owner := strings.TrimSpace(lock.Owner)
	if owner == "" {
		owner = "unknown"
	}
	acquiredAt := lock.StartedAt
	if acquiredAt.IsZero() {
		acquiredAt = lock.HeartbeatAt
	}
	if acquiredAt.IsZero() {
		acquiredAt = observedAt
	}
	renewedAt := lock.HeartbeatAt
	if renewedAt.IsZero() {
		renewedAt = acquiredAt
	}
	lease := state.Lease{
		Name:       stateProjection{}.RunLockLeaseName(lock),
		Scope:      stateProjection{}.RunLockLeaseScope(lock),
		Owner:      owner,
		AcquiredAt: acquiredAt,
		RenewedAt:  renewedAt,
	}
	if !renewedAt.IsZero() {
		lease.ExpiresAt = renewedAt.Add(runLockStaleAfter)
	}
	return lease
}

func (stateProjection) RunLockLeaseName(lock runLock) string {
	name := strings.TrimSpace(lock.IssueIdentifier)
	if name == "" {
		name = filepath.Base(lock.Workspace)
	}
	return "run:" + name
}

func (stateProjection) RunLockLeaseScope(lock runLock) string {
	return filepath.Dir(lock.Workspace)
}

func (stateProjection) DaemonHeartbeat(processID string, config runnerConfig, heartbeat continuousHeartbeat) state.DaemonHeartbeat {
	lastError := ""
	if heartbeat.Err != nil {
		lastError = heartbeat.Err.Error()
	}
	var lastSuccessAt time.Time
	if heartbeat.Success {
		lastSuccessAt = heartbeat.At
	}
	return state.DaemonHeartbeat{
		ProcessID:        processID,
		LaneName:         heartbeat.LaneName,
		WorkflowPath:     config.WorkflowPath,
		CycleNumber:      heartbeat.CycleNumber,
		LastSuccessAt:    lastSuccessAt,
		LastError:        lastError,
		RecoveryRequired: heartbeat.Err != nil,
		UpdatedAt:        heartbeat.At,
	}
}

func openStateProjectionStore(ctx context.Context, workspaceRoot string) (*state.Store, string, error) {
	dbPath := state.DefaultDBPath(workspaceRoot)
	if dbPath == "" {
		return nil, "", fmt.Errorf("state db path is empty")
	}
	store, err := state.Open(ctx, dbPath)
	return store, dbPath, err
}
