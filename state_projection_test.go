package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weskor/agent-machine/internal/state"
)

func TestStateProjectionRunArtifactMatchesMirroringContract(t *testing.T) {
	workspace := t.TempDir()
	record := runRecord{
		IssueIdentifier:      "CAG-67",
		IssueID:              "issue-id",
		Workspace:            workspace,
		Branch:               "symphony/CAG-67-workspace",
		Status:               "success",
		PRURL:                "https://github.com/weskor/agent-machine/pull/67",
		ReviewStatus:         "passed",
		ReviewClassification: "clean",
		ReviewFindings:       "ship it",
		FeedbackHash:         "feedback-hash",
		StartedAt:            time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC),
		EndedAt:              time.Date(2026, 5, 19, 10, 30, 0, 0, time.UTC),
	}
	evaluation := evaluationArtifact{Outcome: "handoff_ready", RootCause: "none", MergeEligible: true, NextAction: "handoff", FeedbackRetryCount: 2}

	snapshot := stateProjection{}.RunArtifact(workspace, record, evaluation)

	if snapshot.IssueKey != "CAG-67" || snapshot.Attempt != 1 || snapshot.Repository != "weskor/agent-machine" || snapshot.PRNumber != 67 {
		t.Fatalf("unexpected run artifact identity projection: %+v", snapshot)
	}
	if !snapshot.ReviewPassed || !snapshot.MergeEligible || snapshot.TerminalOutcome != "handoff_ready" || snapshot.TerminalReason != "none" {
		t.Fatalf("unexpected run artifact decision projection: %+v", snapshot)
	}
	if snapshot.RunArtifactRef != filepath.Join(workspace, ".pi-symphony-run.json") || snapshot.EvaluationRef != filepath.Join(workspace, evaluationArtifactName) {
		t.Fatalf("unexpected artifact refs: %+v", snapshot)
	}

	result := stateProjection{}.AttemptResult(workspace, record, evaluation)
	if result.IssueKey != snapshot.IssueKey || result.PRURL != snapshot.PRURL || result.TerminalOutcome != snapshot.TerminalOutcome || result.RetryCount != snapshot.RetryCount {
		t.Fatalf("attempt result diverged from artifact projection: result=%+v snapshot=%+v", result, snapshot)
	}
}

func TestRunArtifactProjectionPersistsFinishedEventWithAttemptState(t *testing.T) {
	ctx := context.Background()
	store, err := state.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	workspace := t.TempDir()
	record := runRecord{IssueIdentifier: "CAG-87", IssueID: "issue-id", Workspace: workspace, Status: runAttemptStatusSuccess, PRURL: "https://github.com/weskor/agent-machine/pull/87", ReviewStatus: "passed", EndedAt: time.Date(2026, 5, 20, 11, 0, 0, 0, time.UTC)}
	evaluation := evaluationArtifact{Outcome: "handoff_ready", MergeEligible: true}
	if err := store.UpsertRunArtifact(ctx, stateProjection{}.RunArtifact(workspace, record, evaluation)); err != nil {
		t.Fatalf("UpsertRunArtifact() error = %v", err)
	}
	events, err := store.Events(ctx, state.EventFilter{IssueKey: "CAG-87", Type: state.EventAttemptFinished, Limit: 10})
	if err != nil {
		t.Fatalf("RecentEvents() error = %v", err)
	}
	if len(events) != 1 || events[0].IssueKey != "CAG-87" || events[0].Type != state.EventAttemptFinished {
		t.Fatalf("events = %+v", events)
	}
	if !strings.Contains(string(events[0].Payload), `"pr_url"`) {
		t.Fatalf("payload missing pr_url: %s", events[0].Payload)
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

	heartbeat := projection.DaemonHeartbeat("host:123", runnerConfig{ConfigPath: "/repo/symphony.yaml"}, continuousHeartbeat{LaneName: "merge", CycleNumber: 3, Err: errors.New("boom"), ActiveTaskKey: "continuous:merge", ActiveTaskRole: "merge", ActiveLeaseName: "lane:merge", ActiveTaskStartedAt: now.Add(-time.Minute), At: now})
	if heartbeat.ProcessID != "host:123" || heartbeat.LaneName != "merge" || !heartbeat.RecoveryRequired || heartbeat.LastError != "boom" || heartbeat.UpdatedAt != now || heartbeat.ActiveTaskKey != "continuous:merge" || heartbeat.ActiveLeaseName != "lane:merge" {
		t.Fatalf("unexpected heartbeat projection: %+v", heartbeat)
	}
}
