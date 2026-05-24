package main

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/weskor/pi-symphony/internal/state"
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

type eventSummary struct {
	Sequence   int64
	OccurredAt time.Time
	IssueKey   string
	Source     string
	Type       string
}

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

type snapshotLane struct {
	Name                string
	ProcessID           string
	CycleNumber         int
	LastSuccessAt       time.Time
	LastError           string
	RecoveryRequired    bool
	ActiveTaskKey       string
	ActiveTaskRole      string
	ActiveLeaseName     string
	ActiveTaskStartedAt time.Time
	UpdatedAt           time.Time
	Source              string
}

type snapshotWorkerTask struct {
	TaskKey     string
	Role        string
	IssueKey    string
	Attempt     int
	Status      string
	Priority    int
	LeaseName   string
	AvailableAt time.Time
	UpdatedAt   time.Time
}

type snapshotWorkerResult struct {
	TaskKey    string
	Role       string
	LaneName   string
	IssueKey   string
	Attempt    int
	Status     string
	DidWork    bool
	Reason     string
	Error      string
	StartedAt  time.Time
	FinishedAt time.Time
	UpdatedAt  time.Time
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

type snapshotStateRow struct {
	IssueKey, Status, ReviewStatus, PRURL, TerminalOutcome string
	UpdatedAt                                              time.Time
}

func loadSnapshotState(ctx context.Context, workspaceRoot string) ([]snapshotStateRow, []snapshotLane, []eventSummary, []snapshotWorkerTask, []snapshotWorkerResult, state.Health, error) {
	path := state.DefaultDBPath(workspaceRoot)
	health, err := state.InspectHealth(ctx, path)
	if err != nil || !health.OK {
		return nil, nil, nil, nil, nil, health, err
	}
	store, err := state.Open(ctx, path)
	if err != nil {
		return nil, nil, nil, nil, nil, health, err
	}
	defer store.Close()
	rows, err := store.SnapshotAttempts(ctx)
	if err != nil {
		return nil, nil, nil, nil, nil, health, err
	}
	heartbeats, err := store.SnapshotHeartbeats(ctx)
	if err != nil {
		return nil, nil, nil, nil, nil, health, err
	}
	recent, err := store.RecentEvents(ctx, 5)
	if err != nil {
		return nil, nil, nil, nil, nil, health, err
	}
	workerTasks, err := store.WorkerTasks(ctx, "")
	if err != nil {
		return nil, nil, nil, nil, nil, health, err
	}
	workerResults, err := store.WorkerResults(ctx, "")
	if err != nil {
		return nil, nil, nil, nil, nil, health, err
	}
	out := make([]snapshotStateRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, snapshotStateRow{IssueKey: row.IssueKey, Status: row.Status, ReviewStatus: row.ReviewStatus, PRURL: row.PRURL, TerminalOutcome: row.TerminalOutcome, UpdatedAt: row.UpdatedAt})
	}
	lanes := make([]snapshotLane, 0, len(heartbeats))
	for _, heartbeat := range heartbeats {
		lanes = append(lanes, snapshotLane{Name: heartbeat.LaneName, ProcessID: heartbeat.ProcessID, CycleNumber: heartbeat.CycleNumber, LastSuccessAt: heartbeat.LastSuccessAt, LastError: heartbeat.LastError, RecoveryRequired: heartbeat.RecoveryRequired, ActiveTaskKey: heartbeat.ActiveTaskKey, ActiveTaskRole: heartbeat.ActiveTaskRole, ActiveLeaseName: heartbeat.ActiveLeaseName, ActiveTaskStartedAt: heartbeat.ActiveTaskStartedAt, UpdatedAt: heartbeat.UpdatedAt, Source: "sqlite"})
	}
	events := make([]eventSummary, 0, len(recent))
	for _, event := range recent {
		events = append(events, eventSummary{Sequence: event.Sequence, OccurredAt: event.OccurredAt, IssueKey: event.IssueKey, Source: event.Source, Type: event.Type})
	}
	tasks := make([]snapshotWorkerTask, 0, len(workerTasks))
	for _, task := range workerTasks {
		tasks = append(tasks, snapshotWorkerTask{TaskKey: task.TaskKey, Role: task.Role, IssueKey: task.IssueKey, Attempt: task.Attempt, Status: task.Status, Priority: task.Priority, LeaseName: task.LeaseName, AvailableAt: task.AvailableAt, UpdatedAt: task.UpdatedAt})
	}
	results := make([]snapshotWorkerResult, 0, len(workerResults))
	for _, result := range workerResults {
		results = append(results, snapshotWorkerResult{TaskKey: result.TaskKey, Role: result.Role, LaneName: result.LaneName, IssueKey: result.IssueKey, Attempt: result.Attempt, Status: result.Status, DidWork: result.DidWork, Reason: result.Reason, Error: result.Error, StartedAt: result.StartedAt, FinishedAt: result.FinishedAt, UpdatedAt: result.UpdatedAt})
	}
	return out, lanes, events, tasks, results, health, nil
}
