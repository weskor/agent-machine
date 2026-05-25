package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	artifactio "github.com/weskor/agent-machine/internal/artifacts"
	"github.com/weskor/agent-machine/internal/state"
)

// stateProjection owns domain object to SQLite row projection. Attempt writers
// persist this projection before exporting JSON evidence artifacts; compatibility
// and non-authoritative mirrors may still call it best-effort.
type stateProjection struct{}

func (stateProjection) RunArtifact(workspace string, record runRecord, evaluation evaluationArtifact) state.RunArtifactSnapshot {
	result := stateProjection{}.AttemptResult(workspace, record, evaluation)
	return state.RunArtifactSnapshot{
		SchemaVersion:         state.CurrentSchemaVersion,
		ArtifactSchemaVersion: evaluationArtifactSchemaVersion(evaluation),
		ArtifactSchemaSource:  evaluationArtifactSchemaSource(evaluation),
		IssueKey:              result.IssueKey,
		IssueID:               result.IssueID,
		Attempt:               result.Attempt,
		WorkspacePath:         result.WorkspacePath,
		BranchName:            result.BranchName,
		BaseBranch:            result.BaseBranch,
		Status:                result.Status,
		StartedAt:             result.StartedAt,
		UpdatedAt:             result.UpdatedAt,
		Repository:            result.Repository,
		PRNumber:              result.PRNumber,
		PRURL:                 result.PRURL,
		ReviewStatus:          result.ReviewStatus,
		ReviewPassed:          result.ReviewPassed,
		ReviewClassification:  result.ReviewClassification,
		ReviewOutputRef:       result.ReviewOutputRef,
		ReviewOutputHash:      result.ReviewOutputHash,
		MergeEligible:         result.MergeEligible,
		FeedbackHash:          result.FeedbackHash,
		FeedbackNextAction:    result.FeedbackNextAction,
		RetryCount:            result.RetryCount,
		RetryBudgetState:      result.RetryBudgetState,
		RetryReason:           result.RetryReason,
		RetryInputHash:        result.RetryInputHash,
		RetryNextState:        result.RetryNextState,
		TerminalOutcome:       result.TerminalOutcome,
		TerminalReason:        result.TerminalReason,
		RunArtifactRef:        filepath.Join(workspace, ".pi-symphony-run.json"),
		EvaluationRef:         filepath.Join(workspace, evaluationArtifactName),
	}
}

func (stateProjection) AttemptResult(workspace string, record runRecord, evaluation evaluationArtifact) state.AttemptResult {
	repo, prNumber := parseGitHubPR(record.PRURL)
	reviewHash := ""
	if strings.TrimSpace(record.ReviewFindings) != "" {
		sum := sha256.Sum256([]byte(record.ReviewFindings))
		reviewHash = fmt.Sprintf("%x", sum[:])
	}
	snapshot := artifactio.RunArtifactSnapshot(workspace, record, evaluation, artifactio.SnapshotOptions{
		BranchName:       firstNonEmpty(record.Branch, record.ExpectedBranch),
		BaseBranch:       baseBranchForWorkspace(workspace),
		Repository:       repo,
		PRNumber:         prNumber,
		ReviewOutputHash: reviewHash,
		TerminalStatus:   terminalRunStatus(record.Status),
	})
	if retryableRunStatus(record.Status) {
		snapshot.RetryBudgetState = "available"
		snapshot.RetryReason = firstNonEmpty(record.Error, record.BudgetExceeded, record.Status)
		snapshot.RetryNextState = "retry_after_backoff"
	}
	return state.AttemptResult{
		IssueKey:             snapshot.IssueKey,
		IssueID:              snapshot.IssueID,
		Attempt:              snapshot.Attempt,
		WorkspacePath:        snapshot.WorkspacePath,
		BranchName:           snapshot.BranchName,
		BaseBranch:           snapshot.BaseBranch,
		Status:               snapshot.Status,
		StartedAt:            snapshot.StartedAt,
		UpdatedAt:            snapshot.UpdatedAt,
		Repository:           snapshot.Repository,
		PRNumber:             snapshot.PRNumber,
		PRURL:                snapshot.PRURL,
		ReviewStatus:         snapshot.ReviewStatus,
		ReviewPassed:         snapshot.ReviewPassed,
		ReviewClassification: snapshot.ReviewClassification,
		ReviewOutputRef:      snapshot.ReviewOutputRef,
		ReviewOutputHash:     snapshot.ReviewOutputHash,
		MergeEligible:        snapshot.MergeEligible,
		FeedbackHash:         snapshot.FeedbackHash,
		FeedbackNextAction:   snapshot.FeedbackNextAction,
		RetryCount:           snapshot.RetryCount,
		RetryBudgetState:     snapshot.RetryBudgetState,
		RetryReason:          snapshot.RetryReason,
		RetryInputHash:       snapshot.RetryInputHash,
		RetryNextState:       snapshot.RetryNextState,
		TerminalOutcome:      snapshot.TerminalOutcome,
		TerminalReason:       snapshot.TerminalReason,
	}
}

func evaluationArtifactSchemaVersion(evaluation evaluationArtifact) int {
	if evaluation.SchemaVersion != 0 {
		return evaluation.SchemaVersion
	}
	return artifactio.CurrentArtifactSchemaVersion
}

func evaluationArtifactSchemaSource(evaluation evaluationArtifact) string {
	if evaluation.SchemaSource != "" {
		return evaluation.SchemaSource
	}
	return artifactio.ArtifactSchemaSourceCurrent
}

func retryableRunStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "failed", "failure", "blocked", "timeout", "budget_exceeded":
		return true
	default:
		return false
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
		ProcessID:           processID,
		LaneName:            heartbeat.LaneName,
		WorkflowPath:        config.ConfigPath,
		CycleNumber:         heartbeat.CycleNumber,
		LastSuccessAt:       lastSuccessAt,
		LastError:           lastError,
		RecoveryRequired:    heartbeat.Err != nil,
		ActiveTaskKey:       heartbeat.ActiveTaskKey,
		ActiveTaskRole:      heartbeat.ActiveTaskRole,
		ActiveLeaseName:     heartbeat.ActiveLeaseName,
		ActiveTaskStartedAt: heartbeat.ActiveTaskStartedAt,
		UpdatedAt:           heartbeat.At,
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

func commandScopedStateStore(ctx context.Context, workspaceRoot, commandName string) (*state.Store, string) {
	store, dbPath, err := openStateProjectionStore(ctx, workspaceRoot)
	if err != nil {
		if dbPath != "" {
			log("SQLite %s mirror degraded: open path=%s error=%q", commandName, dbPath, err.Error())
		} else {
			log("SQLite %s mirror degraded: %v", commandName, err)
		}
		return nil, dbPath
	}
	return store, dbPath
}
