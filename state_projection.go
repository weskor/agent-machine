package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	artifactio "github.com/weskor/pi-symphony/internal/artifacts"
	"github.com/weskor/pi-symphony/internal/state"
	ws "github.com/weskor/pi-symphony/internal/workspace"
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
	return artifactio.RunArtifactSnapshot(workspace, record, evaluation, artifactio.SnapshotOptions{
		BranchName:       firstNonEmpty(record.Branch, record.ExpectedBranch),
		BaseBranch:       baseBranchForWorkspace(workspace),
		Repository:       repo,
		PRNumber:         prNumber,
		ReviewOutputHash: reviewHash,
		TerminalStatus:   terminalRunStatus(record.Status),
	})
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
	return ws.RunLockLease(lock, observedAt)
}

func (stateProjection) RunLockLeaseName(lock runLock) string {
	return ws.RunLockLeaseName(lock)
}

func (stateProjection) RunLockLeaseScope(lock runLock) string {
	return ws.RunLockLeaseScope(lock)
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
