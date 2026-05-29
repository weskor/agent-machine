package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/weskor/agent-machine/internal/state"
	"github.com/weskor/agent-machine/internal/statusreport"
)

type orchestrationSnapshot struct {
	SourcePrecedence  []string
	Issues            []snapshotIssue
	ActiveLocks       []snapshotLock
	ActiveLanes       []snapshotLane
	Artifacts         []artifactSummary
	RecentEvents      []eventSummary
	WorkerTasks       []snapshotWorkerTask
	WorkerResults     []snapshotWorkerResult
	SQLiteHealth      state.Health
	SQLiteHealthError string
}

type eventSummary = statusreport.EventSummary

type snapshotIssue struct {
	Issue     string
	Status    string
	Review    string
	PRURL     string
	Outcome   string
	Source    string
	UpdatedAt time.Time
	Artifact  *artifactSummary
}

type snapshotLock struct {
	Issue     string
	Workspace string
	Owner     string
	Active    bool
	Stale     bool
	RenewedAt time.Time
}

type snapshotLane = statusreport.Lane

type snapshotWorkerTask = statusreport.WorkerTask

type snapshotWorkerResult = statusreport.WorkerResult

const surfaceSnapshotSchemaVersion = 1

type surfaceSnapshot struct {
	SchemaVersion    int                   `json:"schema_version"`
	ObservedAt       time.Time             `json:"observed_at"`
	ConfigPath       string                `json:"config_path"`
	ProjectSlug      string                `json:"project_slug"`
	WorkspaceRoot    string                `json:"workspace_root"`
	SourcePrecedence []string              `json:"source_precedence"`
	SQLite           surfaceSQLiteHealth   `json:"sqlite"`
	Issues           []surfaceIssue        `json:"issues"`
	ActiveLocks      []surfaceLock         `json:"active_locks"`
	ActiveLanes      []surfaceLane         `json:"active_lanes"`
	WorkerTasks      []surfaceWorkerTask   `json:"worker_tasks"`
	WorkerResults    []surfaceWorkerResult `json:"worker_results"`
	RecentEvents     []surfaceRecentEvent  `json:"recent_events"`
}

type surfaceSQLiteHealth struct {
	OK            bool          `json:"ok"`
	Exists        bool          `json:"exists"`
	SchemaVersion int           `json:"schema_version"`
	JournalMode   string        `json:"journal_mode"`
	BusyTimeoutMS int           `json:"busy_timeout_ms"`
	Counts        surfaceCounts `json:"counts"`
	Error         string        `json:"error,omitempty"`
}

type surfaceCounts struct {
	IssueAttempts     int `json:"issue_attempts"`
	PRMappings        int `json:"pr_mappings"`
	ReviewStates      int `json:"review_states"`
	TerminalOutcomes  int `json:"terminal_outcomes"`
	DaemonHeartbeats  int `json:"daemon_heartbeats"`
	CleanupStates     int `json:"cleanup_states"`
	Events            int `json:"events"`
	WorkerTasks       int `json:"worker_tasks"`
	WorkerResults     int `json:"worker_results"`
	WorkerPayloadRefs int `json:"worker_payload_refs"`
	PRHandoffIntents  int `json:"pr_handoff_intents"`
}

type surfaceIssue struct {
	Issue     string    `json:"issue"`
	Status    string    `json:"status"`
	Review    string    `json:"review,omitempty"`
	PRURL     string    `json:"pr_url,omitempty"`
	Outcome   string    `json:"outcome,omitempty"`
	Source    string    `json:"source"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type surfaceLock struct {
	Issue     string    `json:"issue"`
	Workspace string    `json:"workspace"`
	Owner     string    `json:"owner"`
	Active    bool      `json:"active"`
	Stale     bool      `json:"stale"`
	RenewedAt time.Time `json:"renewed_at,omitempty"`
}

type surfaceLane struct {
	Name                string    `json:"name"`
	ProcessID           string    `json:"process_id"`
	CycleNumber         int       `json:"cycle_number"`
	LastSuccessAt       time.Time `json:"last_success_at,omitempty"`
	LastError           string    `json:"last_error,omitempty"`
	RecoveryRequired    bool      `json:"recovery_required"`
	ActiveTaskKey       string    `json:"active_task_key,omitempty"`
	ActiveTaskRole      string    `json:"active_task_role,omitempty"`
	ActiveLeaseName     string    `json:"active_lease_name,omitempty"`
	ActiveTaskStartedAt time.Time `json:"active_task_started_at,omitempty"`
	UpdatedAt           time.Time `json:"updated_at,omitempty"`
	Source              string    `json:"source"`
}

type surfaceWorkerTask struct {
	TaskKey     string    `json:"task_key"`
	Role        string    `json:"role"`
	IssueKey    string    `json:"issue_key,omitempty"`
	Attempt     int       `json:"attempt,omitempty"`
	Status      string    `json:"status"`
	Priority    int       `json:"priority"`
	LeaseName   string    `json:"lease_name,omitempty"`
	AvailableAt time.Time `json:"available_at,omitempty"`
	UpdatedAt   time.Time `json:"updated_at,omitempty"`
}

type surfaceWorkerResult struct {
	TaskKey    string    `json:"task_key"`
	Role       string    `json:"role"`
	LaneName   string    `json:"lane_name,omitempty"`
	IssueKey   string    `json:"issue_key,omitempty"`
	Attempt    int       `json:"attempt,omitempty"`
	Status     string    `json:"status"`
	DidWork    bool      `json:"did_work"`
	Reason     string    `json:"reason,omitempty"`
	Error      string    `json:"error,omitempty"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	UpdatedAt  time.Time `json:"updated_at,omitempty"`
}

type surfaceRecentEvent struct {
	Sequence   int64     `json:"sequence"`
	OccurredAt time.Time `json:"occurred_at,omitempty"`
	IssueKey   string    `json:"issue_key,omitempty"`
	Source     string    `json:"source"`
	Type       string    `json:"type"`
}

func printSurfaceSnapshot(config runnerConfig) error {
	snapshot, err := buildSurfaceSnapshot(context.Background(), config, time.Now().UTC())
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

func buildSurfaceSnapshot(ctx context.Context, config runnerConfig, observedAt time.Time) (surfaceSnapshot, error) {
	orch, err := buildOrchestrationSnapshot(ctx, config, observedAt)
	if err != nil {
		return surfaceSnapshot{}, err
	}
	out := surfaceSnapshot{
		SchemaVersion:    surfaceSnapshotSchemaVersion,
		ObservedAt:       observedAt,
		ConfigPath:       config.ConfigPath,
		ProjectSlug:      config.ProjectSlug,
		WorkspaceRoot:    config.WorkspaceRoot,
		SourcePrecedence: append([]string{}, orch.SourcePrecedence...),
		SQLite: surfaceSQLiteHealth{
			OK:            orch.SQLiteHealth.OK,
			Exists:        orch.SQLiteHealth.Exists,
			SchemaVersion: orch.SQLiteHealth.SchemaVersion,
			JournalMode:   orch.SQLiteHealth.JournalMode,
			BusyTimeoutMS: orch.SQLiteHealth.BusyTimeoutMS,
			Counts:        surfaceCountsFromState(orch.SQLiteHealth.Counts),
			Error:         orch.SQLiteHealthError,
		},
		Issues:        []surfaceIssue{},
		ActiveLocks:   []surfaceLock{},
		ActiveLanes:   []surfaceLane{},
		WorkerTasks:   []surfaceWorkerTask{},
		WorkerResults: []surfaceWorkerResult{},
		RecentEvents:  []surfaceRecentEvent{},
	}
	for _, issue := range orch.Issues {
		out.Issues = append(out.Issues, surfaceIssue{Issue: issue.Issue, Status: issue.Status, Review: issue.Review, PRURL: issue.PRURL, Outcome: issue.Outcome, Source: issue.Source, UpdatedAt: issue.UpdatedAt})
	}
	for _, lock := range orch.ActiveLocks {
		out.ActiveLocks = append(out.ActiveLocks, surfaceLock(lock))
	}
	for _, lane := range orch.ActiveLanes {
		out.ActiveLanes = append(out.ActiveLanes, surfaceLane(lane))
	}
	for _, task := range orch.WorkerTasks {
		out.WorkerTasks = append(out.WorkerTasks, surfaceWorkerTask(task))
	}
	for _, result := range orch.WorkerResults {
		out.WorkerResults = append(out.WorkerResults, surfaceWorkerResult(result))
	}
	for _, event := range orch.RecentEvents {
		out.RecentEvents = append(out.RecentEvents, surfaceRecentEvent(event))
	}
	return out, nil
}

func surfaceCountsFromState(counts state.Counts) surfaceCounts {
	return surfaceCounts{
		IssueAttempts:     counts.IssueAttempts,
		PRMappings:        counts.PRMappings,
		ReviewStates:      counts.ReviewStates,
		TerminalOutcomes:  counts.TerminalOutcomes,
		DaemonHeartbeats:  counts.DaemonHeartbeats,
		CleanupStates:     counts.CleanupStates,
		Events:            counts.Events,
		WorkerTasks:       counts.WorkerTasks,
		WorkerResults:     counts.WorkerResults,
		WorkerPayloadRefs: counts.WorkerPayloadRefs,
		PRHandoffIntents:  counts.PRHandoffIntents,
	}
}

func buildOrchestrationSnapshot(ctx context.Context, config runnerConfig, observedAt time.Time) (orchestrationSnapshot, error) {
	snap := orchestrationSnapshot{SourcePrecedence: []string{"active_locks_lanes", "sqlite", "artifacts_fallback"}}
	artifacts, err := workspaceArtifactSummaries(config.WorkspaceRoot)
	if err != nil {
		return snap, err
	}
	snap.Artifacts = artifacts
	byIssue := map[string]*snapshotIssue{}
	for _, artifact := range artifacts {
		a := artifact
		if a.Issue == "" {
			continue
		}
		byIssue[a.Issue] = &snapshotIssue{Issue: a.Issue, Status: a.Status, Review: a.Review, PRURL: a.PRURL, Outcome: a.Outcome, Source: "artifact", Artifact: &a}
	}
	rows, lanes, events, tasks, results, health, healthErr := loadSnapshotState(ctx, config.WorkspaceRoot)
	snap.SQLiteHealth = health
	snap.ActiveLanes = lanes
	snap.RecentEvents = events
	snap.WorkerTasks = tasks
	snap.WorkerResults = results
	if healthErr != nil {
		snap.SQLiteHealthError = healthErr.Error()
	}
	for _, row := range rows {
		issue := strings.TrimSpace(row.IssueKey)
		if issue == "" {
			continue
		}
		existing := byIssue[issue]
		var artifact *artifactSummary
		if existing != nil {
			artifact = existing.Artifact
		}
		byIssue[issue] = &snapshotIssue{Issue: issue, Status: row.Status, Review: row.ReviewStatus, PRURL: row.PRURL, Outcome: row.TerminalOutcome, Source: "sqlite", UpdatedAt: row.UpdatedAt, Artifact: artifact}
	}
	locks, err := snapshotRunLocks(config.WorkspaceRoot, observedAt)
	if err != nil {
		return snap, err
	}
	snap.ActiveLocks = locks
	for _, lock := range locks {
		if !lock.Active || lock.Issue == "" {
			continue
		}
		existing := byIssue[lock.Issue]
		var artifact *artifactSummary
		review, prURL, outcome := "", "", ""
		if existing != nil {
			artifact = existing.Artifact
			review = existing.Review
			prURL = existing.PRURL
			outcome = existing.Outcome
		}
		byIssue[lock.Issue] = &snapshotIssue{Issue: lock.Issue, Status: "active", Review: review, PRURL: prURL, Outcome: outcome, Source: "active_lock", UpdatedAt: lock.RenewedAt, Artifact: artifact}
	}
	for _, issue := range byIssue {
		snap.Issues = append(snap.Issues, *issue)
	}
	sort.Slice(snap.Issues, func(i, j int) bool { return snap.Issues[i].Issue < snap.Issues[j].Issue })
	sort.Slice(snap.ActiveLocks, func(i, j int) bool { return snap.ActiveLocks[i].Issue < snap.ActiveLocks[j].Issue })
	sort.Slice(snap.WorkerTasks, func(i, j int) bool {
		if !snap.WorkerTasks[i].UpdatedAt.Equal(snap.WorkerTasks[j].UpdatedAt) {
			return snap.WorkerTasks[i].UpdatedAt.After(snap.WorkerTasks[j].UpdatedAt)
		}
		return snap.WorkerTasks[i].TaskKey < snap.WorkerTasks[j].TaskKey
	})
	sort.Slice(snap.WorkerResults, func(i, j int) bool {
		if !snap.WorkerResults[i].UpdatedAt.Equal(snap.WorkerResults[j].UpdatedAt) {
			return snap.WorkerResults[i].UpdatedAt.After(snap.WorkerResults[j].UpdatedAt)
		}
		return snap.WorkerResults[i].TaskKey < snap.WorkerResults[j].TaskKey
	})
	return snap, nil
}

func snapshotRunLocks(workspaceRoot string, observedAt time.Time) ([]snapshotLock, error) {
	entries, err := os.ReadDir(workspaceRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var locks []snapshotLock
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		workspace := filepath.Join(workspaceRoot, entry.Name())
		lock, err := readRunLock(runLockPath(workspace))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		renewed := lock.HeartbeatAt
		if renewed.IsZero() {
			renewed = lock.StartedAt
		}
		issue := lock.IssueIdentifier
		if issue == "" {
			issue = entry.Name()
		}
		stale := !renewed.IsZero() && observedAt.Sub(renewed) > runLockStaleAfter
		locks = append(locks, snapshotLock{Issue: issue, Workspace: workspace, Owner: lock.Owner, Active: !stale, Stale: stale, RenewedAt: renewed})
	}
	return locks, nil
}

type snapshotStateRow = statusreport.StateRow

func loadSnapshotState(ctx context.Context, workspaceRoot string) ([]snapshotStateRow, []snapshotLane, []eventSummary, []snapshotWorkerTask, []snapshotWorkerResult, state.Health, error) {
	return statusreport.LoadSnapshotState(ctx, workspaceRoot)
}
