package stateprojection

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weskor/agent-machine/internal/artifacts"
	"github.com/weskor/agent-machine/internal/domain"
)

func TestRunArtifactMatchesAttemptResultProjection(t *testing.T) {
	workspace := t.TempDir()
	record := domain.RunRecord{
		IssueIdentifier:      "CAG-67",
		IssueID:              "issue-id",
		Workspace:            workspace,
		Branch:               "am/CAG-67-workspace",
		Status:               "success",
		PRURL:                "https://github.com/weskor/agent-machine/pull/67",
		ReviewStatus:         "passed",
		ReviewClassification: "clean",
		ReviewFindings:       "ship it",
		FeedbackHash:         "feedback-hash",
		StartedAt:            time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC),
		EndedAt:              time.Date(2026, 5, 19, 10, 30, 0, 0, time.UTC),
	}
	evaluation := artifacts.EvaluationArtifact{Outcome: "handoff_ready", RootCause: "none", MergeEligible: true, NextAction: "handoff", FeedbackRetryCount: 2}
	projection := Projection{
		BaseBranch:     func(string) string { return "main" },
		TerminalStatus: func(status string) bool { return status == "success" },
	}

	snapshot := projection.RunArtifact(workspace, record, evaluation)

	if snapshot.IssueKey != "CAG-67" || snapshot.Attempt != 1 || snapshot.Repository != "weskor/agent-machine" || snapshot.PRNumber != 67 {
		t.Fatalf("unexpected run artifact identity projection: %+v", snapshot)
	}
	if !snapshot.ReviewPassed || !snapshot.MergeEligible || snapshot.TerminalOutcome != "handoff_ready" || snapshot.TerminalReason != "none" {
		t.Fatalf("unexpected run artifact decision projection: %+v", snapshot)
	}
	if snapshot.RunArtifactRef != filepath.Join(workspace, artifacts.RunRecordName) || snapshot.EvaluationRef != filepath.Join(workspace, artifacts.EvaluationName) {
		t.Fatalf("unexpected artifact refs: %+v", snapshot)
	}

	result := projection.AttemptResult(workspace, record, evaluation)
	if result.IssueKey != snapshot.IssueKey || result.PRURL != snapshot.PRURL || result.TerminalOutcome != snapshot.TerminalOutcome || result.RetryCount != snapshot.RetryCount {
		t.Fatalf("attempt result diverged from artifact projection: result=%+v snapshot=%+v", result, snapshot)
	}
}

func TestCleanupLeaseAndHeartbeatProjection(t *testing.T) {
	now := time.Date(2026, 5, 19, 11, 0, 0, 0, time.UTC)
	staleAfter := 30 * time.Minute
	projection := Projection{RunLockStaleAfter: staleAfter}

	cleanup := projection.Cleanup(CleanupDecision{IssueIdentifier: "CAG-67", Category: "not-done", Reason: "still active", ArtifactRef: "artifact.json"}, false, "kept", true, now)
	if cleanup.BlockedReason != "still active" || cleanup.DeletionResult != "kept" || cleanup.UpdatedAt != now {
		t.Fatalf("unexpected cleanup projection: %+v", cleanup)
	}

	lock := domain.RunLock{IssueIdentifier: "CAG-67", Owner: "agent", Workspace: filepath.Join(t.TempDir(), "CAG-67"), StartedAt: now, HeartbeatAt: now.Add(time.Minute)}
	lease := projection.RunLockLease(lock, now)
	if lease.Name != "run:CAG-67" || lease.Scope != filepath.Dir(lock.Workspace) || lease.Owner != "agent" || !lease.ExpiresAt.Equal(lock.HeartbeatAt.Add(staleAfter)) {
		t.Fatalf("unexpected lease projection: %+v", lease)
	}

	heartbeat := projection.DaemonHeartbeat("host:123", domain.RunnerConfig{ConfigPath: "/repo/am.yaml"}, DaemonHeartbeatInput{LaneName: "merge", CycleNumber: 3, Err: errors.New("boom"), ActiveTaskKey: "continuous:merge", ActiveTaskRole: "merge", ActiveLeaseName: "lane:merge", ActiveTaskStartedAt: now.Add(-time.Minute), At: now})
	if heartbeat.ProcessID != "host:123" || heartbeat.LaneName != "merge" || !heartbeat.RecoveryRequired || heartbeat.LastError != "boom" || heartbeat.UpdatedAt != now || heartbeat.ActiveTaskKey != "continuous:merge" || heartbeat.ActiveLeaseName != "lane:merge" {
		t.Fatalf("unexpected heartbeat projection: %+v", heartbeat)
	}
}

func TestParseGitHubPR(t *testing.T) {
	repo, number := ParseGitHubPR("https://github.com/weskor/agent-machine/pull/67")
	if repo != "weskor/agent-machine" || number != 67 {
		t.Fatalf("repo/number = %q/%d", repo, number)
	}
	if repo, number := ParseGitHubPR("https://gitlab.com/weskor/agent-machine/-/merge_requests/67"); repo != "" || number != 0 {
		t.Fatalf("expected non-GitHub PR to be ignored, got %q/%d", repo, number)
	}
}

func TestRetryableRunStatus(t *testing.T) {
	for _, status := range []string{"failed", "failure", "blocked", "timeout", "budget_exceeded"} {
		if !RetryableRunStatus(strings.ToUpper(status)) {
			t.Fatalf("%q should be retryable", status)
		}
	}
	if RetryableRunStatus("success") {
		t.Fatal("success should not be retryable")
	}
}
