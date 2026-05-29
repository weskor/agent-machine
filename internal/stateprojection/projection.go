package stateprojection

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/weskor/agent-machine/internal/artifacts"
	"github.com/weskor/agent-machine/internal/codehost"
	"github.com/weskor/agent-machine/internal/domain"
	"github.com/weskor/agent-machine/internal/state"
)

type Projection struct {
	BaseBranch        func(workspace string) string
	TerminalStatus    func(status string) bool
	RunLockStaleAfter time.Duration
}

type CleanupDecision struct {
	Reason          string
	Category        string
	IssueIdentifier string
	ArtifactRef     string
}

type DaemonHeartbeatInput struct {
	LaneName            string
	CycleNumber         int
	Success             bool
	Err                 error
	ActiveTaskKey       string
	ActiveTaskRole      string
	ActiveLeaseName     string
	ActiveTaskStartedAt time.Time
	At                  time.Time
}

func (p Projection) RunArtifact(workspace string, record domain.RunRecord, evaluation artifacts.EvaluationArtifact) state.RunArtifactSnapshot {
	result := p.AttemptResult(workspace, record, evaluation)
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
		RunArtifactRef:        filepath.Join(workspace, artifacts.RunRecordName),
		EvaluationRef:         filepath.Join(workspace, artifacts.EvaluationName),
	}
}

func (p Projection) AttemptResult(workspace string, record domain.RunRecord, evaluation artifacts.EvaluationArtifact) state.AttemptResult {
	repo, prNumber := ParseGitHubPR(record.PRURL)
	reviewHash := ""
	if strings.TrimSpace(record.ReviewFindings) != "" {
		sum := sha256.Sum256([]byte(record.ReviewFindings))
		reviewHash = fmt.Sprintf("%x", sum[:])
	}
	snapshot := artifacts.RunArtifactSnapshot(workspace, record, evaluation, artifacts.SnapshotOptions{
		BranchName:       firstNonEmpty(record.Branch, record.ExpectedBranch),
		BaseBranch:       p.baseBranch(workspace),
		Repository:       repo,
		PRNumber:         prNumber,
		ReviewOutputHash: reviewHash,
		TerminalStatus:   p.terminalStatus(record.Status),
	})
	if RetryableRunStatus(record.Status) {
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

func (p Projection) Cleanup(decision CleanupDecision, eligible bool, deletionResult string, workspaceExists bool, updatedAt time.Time) state.CleanupState {
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

func (p Projection) RunLockLease(lock domain.RunLock, observedAt time.Time) state.Lease {
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
		Name:       p.RunLockLeaseName(lock),
		Scope:      p.RunLockLeaseScope(lock),
		Owner:      owner,
		AcquiredAt: acquiredAt,
		RenewedAt:  renewedAt,
	}
	if !renewedAt.IsZero() {
		lease.ExpiresAt = renewedAt.Add(p.runLockStaleAfter())
	}
	return lease
}

func (Projection) RunLockLeaseName(lock domain.RunLock) string {
	name := strings.TrimSpace(lock.IssueIdentifier)
	if name == "" {
		name = filepath.Base(lock.Workspace)
	}
	return "run:" + name
}

func (Projection) RunLockLeaseScope(lock domain.RunLock) string {
	return filepath.Dir(lock.Workspace)
}

func (Projection) DaemonHeartbeat(processID string, config domain.RunnerConfig, heartbeat DaemonHeartbeatInput) state.DaemonHeartbeat {
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

func OpenStore(ctx context.Context, workspaceRoot string) (*state.Store, string, error) {
	dbPath := state.DefaultDBPath(workspaceRoot)
	if dbPath == "" {
		return nil, "", fmt.Errorf("state db path is empty")
	}
	store, err := state.Open(ctx, dbPath)
	return store, dbPath, err
}

func (p Projection) baseBranch(workspace string) string {
	if p.BaseBranch == nil {
		return ""
	}
	return p.BaseBranch(workspace)
}

func (p Projection) terminalStatus(status string) bool {
	if p.TerminalStatus == nil {
		return false
	}
	return p.TerminalStatus(status)
}

func (p Projection) runLockStaleAfter() time.Duration {
	if p.RunLockStaleAfter > 0 {
		return p.RunLockStaleAfter
	}
	return time.Hour
}

func evaluationArtifactSchemaVersion(evaluation artifacts.EvaluationArtifact) int {
	if evaluation.SchemaVersion != 0 {
		return evaluation.SchemaVersion
	}
	return artifacts.CurrentArtifactSchemaVersion
}

func evaluationArtifactSchemaSource(evaluation artifacts.EvaluationArtifact) string {
	if evaluation.SchemaSource != "" {
		return evaluation.SchemaSource
	}
	return artifacts.ArtifactSchemaSourceCurrent
}

func RetryableRunStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "failed", "failure", "blocked", "timeout", "budget_exceeded":
		return true
	default:
		return false
	}
}

func ParseGitHubPR(prURL string) (string, int) {
	parsed, ok := codehost.ParsePullRequestURL(strings.TrimSpace(prURL))
	if !ok || parsed.Provider != codehost.ProviderGitHub {
		return "", 0
	}
	return parsed.Owner + "/" + parsed.Repo, parsed.Number
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
