package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/weskor/pi-symphony/internal/state"
)

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
		out.ActiveLocks = append(out.ActiveLocks, surfaceLock{Issue: lock.Issue, Workspace: lock.Workspace, Owner: lock.Owner, Active: lock.Active, Stale: lock.Stale, RenewedAt: lock.RenewedAt})
	}
	for _, lane := range orch.ActiveLanes {
		out.ActiveLanes = append(out.ActiveLanes, surfaceLane{Name: lane.Name, ProcessID: lane.ProcessID, CycleNumber: lane.CycleNumber, LastSuccessAt: lane.LastSuccessAt, LastError: lane.LastError, RecoveryRequired: lane.RecoveryRequired, ActiveTaskKey: lane.ActiveTaskKey, ActiveTaskRole: lane.ActiveTaskRole, ActiveLeaseName: lane.ActiveLeaseName, ActiveTaskStartedAt: lane.ActiveTaskStartedAt, UpdatedAt: lane.UpdatedAt, Source: lane.Source})
	}
	for _, task := range orch.WorkerTasks {
		out.WorkerTasks = append(out.WorkerTasks, surfaceWorkerTask{TaskKey: task.TaskKey, Role: task.Role, IssueKey: task.IssueKey, Attempt: task.Attempt, Status: task.Status, Priority: task.Priority, LeaseName: task.LeaseName, AvailableAt: task.AvailableAt, UpdatedAt: task.UpdatedAt})
	}
	for _, result := range orch.WorkerResults {
		out.WorkerResults = append(out.WorkerResults, surfaceWorkerResult{TaskKey: result.TaskKey, Role: result.Role, LaneName: result.LaneName, IssueKey: result.IssueKey, Attempt: result.Attempt, Status: result.Status, DidWork: result.DidWork, Reason: result.Reason, Error: result.Error, StartedAt: result.StartedAt, FinishedAt: result.FinishedAt, UpdatedAt: result.UpdatedAt})
	}
	for _, event := range orch.RecentEvents {
		out.RecentEvents = append(out.RecentEvents, surfaceRecentEvent{Sequence: event.Sequence, OccurredAt: event.OccurredAt, IssueKey: event.IssueKey, Source: event.Source, Type: event.Type})
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
