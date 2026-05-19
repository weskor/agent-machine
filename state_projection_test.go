package main

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestStateProjectionRunArtifactMatchesMirroringContract(t *testing.T) {
	workspace := t.TempDir()
	record := runRecord{
		IssueIdentifier:      "CAG-67",
		IssueID:              "issue-id",
		Workspace:            workspace,
		Branch:               "symphony/CAG-67-workspace",
		Status:               "success",
		PRURL:                "https://github.com/weskor/pi-symphony/pull/67",
		ReviewStatus:         "passed",
		ReviewClassification: "clean",
		ReviewFindings:       "ship it",
		FeedbackHash:         "feedback-hash",
		StartedAt:            time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC),
		EndedAt:              time.Date(2026, 5, 19, 10, 30, 0, 0, time.UTC),
	}
	evaluation := evaluationArtifact{Outcome: "handoff_ready", RootCause: "none", MergeEligible: true, NextAction: "handoff", FeedbackRetryCount: 2}

	snapshot := stateProjection{}.RunArtifact(workspace, record, evaluation)

	if snapshot.IssueKey != "CAG-67" || snapshot.Attempt != 1 || snapshot.Repository != "weskor/pi-symphony" || snapshot.PRNumber != 67 {
		t.Fatalf("unexpected run artifact identity projection: %+v", snapshot)
	}
	if !snapshot.ReviewPassed || !snapshot.MergeEligible || snapshot.TerminalOutcome != "handoff_ready" || snapshot.TerminalReason != "none" {
		t.Fatalf("unexpected run artifact decision projection: %+v", snapshot)
	}
	if snapshot.RunArtifactRef != filepath.Join(workspace, ".pi-symphony-run.json") || snapshot.EvaluationRef != filepath.Join(workspace, evaluationArtifactName) {
		t.Fatalf("unexpected artifact refs: %+v", snapshot)
	}
}

func TestStateProjectionCleanupLeaseAndHeartbeat(t *testing.T) {
	now := time.Date(2026, 5, 19, 11, 0, 0, 0, time.UTC)
	projection := stateProjection{}

	cleanup := projection.Cleanup(cleanupResult{IssueIdentifier: "CAG-67", Category: "not-done", Reason: "still active", ArtifactRef: "artifact.json"}, false, "kept", true, now)
	if cleanup.BlockedReason != "still active" || cleanup.DeletionResult != "kept" || cleanup.UpdatedAt != now {
		t.Fatalf("unexpected cleanup projection: %+v", cleanup)
	}

	lock := runLock{IssueIdentifier: "CAG-67", Owner: "agent", Workspace: filepath.Join(t.TempDir(), "CAG-67"), StartedAt: now, HeartbeatAt: now.Add(time.Minute)}
	lease := projection.RunLockLease(lock, now)
	if lease.Name != "run:CAG-67" || lease.Scope != filepath.Dir(lock.Workspace) || lease.Owner != "agent" || !lease.ExpiresAt.Equal(lock.HeartbeatAt.Add(runLockStaleAfter)) {
		t.Fatalf("unexpected lease projection: %+v", lease)
	}

	heartbeat := projection.DaemonHeartbeat("host:123", runnerConfig{WorkflowPath: "/repo/WORKFLOW.md"}, continuousHeartbeat{LaneName: "merge", CycleNumber: 3, Err: errors.New("boom"), At: now})
	if heartbeat.ProcessID != "host:123" || heartbeat.LaneName != "merge" || !heartbeat.RecoveryRequired || heartbeat.LastError != "boom" || heartbeat.UpdatedAt != now {
		t.Fatalf("unexpected heartbeat projection: %+v", heartbeat)
	}
}
