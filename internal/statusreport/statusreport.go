package statusreport

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/weskor/agent-machine/internal/state"
)

type Snapshot struct {
	SQLiteHealth      state.Health
	SQLiteHealthError string
	ActiveLanes       []Lane
	RecentEvents      []EventSummary
	WorkerTasks       []WorkerTask
	WorkerResults     []WorkerResult
}

type EventSummary struct {
	Sequence   int64
	OccurredAt time.Time
	IssueKey   string
	Source     string
	Type       string
}

type Lane struct {
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

type WorkerTask struct {
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

type WorkerResult struct {
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

type StateRow struct {
	IssueKey, Status, ReviewStatus, PRURL, TerminalOutcome string
	UpdatedAt                                              time.Time
}

func SummarizeSnapshotStateStore(workspaceRoot string, snapshot Snapshot) []string {
	path := state.DefaultDBPath(workspaceRoot)
	lines := []string{fmt.Sprintf("SQLite state path: %s", emptyAsNA(path))}
	if path == "" {
		return append(lines, "SQLite state health: degraded path=unconfigured action=set workspace.root")
	}
	if snapshot.SQLiteHealthError != "" {
		return append(lines, fmt.Sprintf("SQLite state health: degraded error=%q action=check state DB path and permissions", snapshot.SQLiteHealthError))
	}
	health := snapshot.SQLiteHealth
	if !health.Exists {
		return append(lines, "SQLite state health: missing action=run am start to initialize durable state")
	}
	status := "degraded"
	if health.OK {
		status = "healthy"
	}
	lines = append(lines,
		fmt.Sprintf("SQLite state health: %s schema_version=%d journal_mode=%s busy_timeout_ms=%d", status, health.SchemaVersion, emptyAsNA(health.JournalMode), health.BusyTimeoutMS),
		FormatStateCounts(health.Counts),
	)
	lines = append(lines, SummarizeActiveLanes(snapshot.ActiveLanes)...)
	lines = append(lines, SummarizeWorkerTasks(snapshot.WorkerTasks)...)
	lines = append(lines, SummarizeWorkerResults(snapshot.WorkerResults)...)
	if len(snapshot.RecentEvents) > 0 {
		lines = append(lines, "SQLite recent events:")
		for _, event := range snapshot.RecentEvents {
			lines = append(lines, FormatEventSummary(event))
		}
	}
	return lines
}

func SummarizeStateStore(workspaceRoot string) []string {
	path := state.DefaultDBPath(workspaceRoot)
	lines := []string{fmt.Sprintf("SQLite state path: %s", emptyAsNA(path))}
	if path == "" {
		return append(lines, "SQLite state health: degraded path=unconfigured action=set workspace.root")
	}
	health, err := state.InspectHealth(context.Background(), path)
	if err != nil {
		return append(lines, fmt.Sprintf("SQLite state health: degraded error=%q action=check state DB path and permissions", err.Error()))
	}
	if !health.Exists {
		return append(lines, "SQLite state health: missing action=run am start to initialize durable state")
	}
	status := "degraded"
	if health.OK {
		status = "healthy"
	}
	lines = append(lines,
		fmt.Sprintf("SQLite state health: %s schema_version=%d journal_mode=%s busy_timeout_ms=%d", status, health.SchemaVersion, emptyAsNA(health.JournalMode), health.BusyTimeoutMS),
		FormatStateCounts(health.Counts),
	)
	store, err := state.Open(context.Background(), path)
	if err != nil {
		return lines
	}
	defer store.Close()
	heartbeats, err := store.SnapshotHeartbeats(context.Background())
	if err == nil {
		lines = append(lines, SummarizeActiveLanes(LanesFromHeartbeats(heartbeats))...)
	}
	tasks, err := store.WorkerTasks(context.Background(), "")
	if err == nil {
		lines = append(lines, SummarizeWorkerTasks(WorkerTasksFromState(tasks))...)
	}
	results, err := store.WorkerResults(context.Background(), "")
	if err == nil {
		lines = append(lines, SummarizeWorkerResults(WorkerResultsFromState(results))...)
	}
	events, err := store.RecentEvents(context.Background(), 5)
	if err != nil || len(events) == 0 {
		return lines
	}
	lines = append(lines, "SQLite recent events:")
	for _, event := range events {
		lines = append(lines, FormatEventSummary(EventSummary{Sequence: event.Sequence, OccurredAt: event.OccurredAt, IssueKey: event.IssueKey, Source: event.Source, Type: event.Type}))
	}
	return lines
}

func LoadSnapshotState(ctx context.Context, workspaceRoot string) ([]StateRow, []Lane, []EventSummary, []WorkerTask, []WorkerResult, state.Health, error) {
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
	out := make([]StateRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, StateRow{IssueKey: row.IssueKey, Status: row.Status, ReviewStatus: row.ReviewStatus, PRURL: row.PRURL, TerminalOutcome: row.TerminalOutcome, UpdatedAt: row.UpdatedAt})
	}
	events := make([]EventSummary, 0, len(recent))
	for _, event := range recent {
		events = append(events, EventSummary{Sequence: event.Sequence, OccurredAt: event.OccurredAt, IssueKey: event.IssueKey, Source: event.Source, Type: event.Type})
	}
	return out, LanesFromHeartbeats(heartbeats), events, WorkerTasksFromState(workerTasks), WorkerResultsFromState(workerResults), health, nil
}

func FormatStateCounts(counts state.Counts) string {
	return fmt.Sprintf("SQLite state counts: issue_attempts=%d pr_mappings=%d review_states=%d terminal_outcomes=%d daemon_heartbeats=%d cleanup_states=%d worker_tasks=%d worker_results=%d worker_payload_refs=%d pr_handoff_intents=%d events=%d", counts.IssueAttempts, counts.PRMappings, counts.ReviewStates, counts.TerminalOutcomes, counts.DaemonHeartbeats, counts.CleanupStates, counts.WorkerTasks, counts.WorkerResults, counts.WorkerPayloadRefs, counts.PRHandoffIntents, counts.Events)
}

func LanesFromHeartbeats(heartbeats []state.SnapshotHeartbeat) []Lane {
	lanes := make([]Lane, 0, len(heartbeats))
	for _, heartbeat := range heartbeats {
		lanes = append(lanes, Lane{Name: heartbeat.LaneName, ProcessID: heartbeat.ProcessID, CycleNumber: heartbeat.CycleNumber, LastSuccessAt: heartbeat.LastSuccessAt, LastError: heartbeat.LastError, RecoveryRequired: heartbeat.RecoveryRequired, ActiveTaskKey: heartbeat.ActiveTaskKey, ActiveTaskRole: heartbeat.ActiveTaskRole, ActiveLeaseName: heartbeat.ActiveLeaseName, ActiveTaskStartedAt: heartbeat.ActiveTaskStartedAt, UpdatedAt: heartbeat.UpdatedAt, Source: "sqlite"})
	}
	return lanes
}

func WorkerTasksFromState(tasks []state.WorkerTask) []WorkerTask {
	out := make([]WorkerTask, 0, len(tasks))
	for _, task := range tasks {
		out = append(out, WorkerTask{TaskKey: task.TaskKey, Role: task.Role, IssueKey: task.IssueKey, Attempt: task.Attempt, Status: task.Status, Priority: task.Priority, LeaseName: task.LeaseName, AvailableAt: task.AvailableAt, UpdatedAt: task.UpdatedAt})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		return out[i].TaskKey < out[j].TaskKey
	})
	return out
}

func WorkerResultsFromState(results []state.WorkerResult) []WorkerResult {
	out := make([]WorkerResult, 0, len(results))
	for _, result := range results {
		out = append(out, WorkerResult{TaskKey: result.TaskKey, Role: result.Role, LaneName: result.LaneName, IssueKey: result.IssueKey, Attempt: result.Attempt, Status: result.Status, DidWork: result.DidWork, Reason: result.Reason, Error: result.Error, StartedAt: result.StartedAt, FinishedAt: result.FinishedAt, UpdatedAt: result.UpdatedAt})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		return out[i].TaskKey < out[j].TaskKey
	})
	return out
}

func SummarizeActiveLanes(lanes []Lane) []string {
	if len(lanes) == 0 {
		return nil
	}
	sort.Slice(lanes, func(i, j int) bool {
		if !lanes[i].UpdatedAt.Equal(lanes[j].UpdatedAt) {
			return lanes[i].UpdatedAt.After(lanes[j].UpdatedAt)
		}
		return lanes[i].Name < lanes[j].Name
	})
	lines := []string{fmt.Sprintf("SQLite active lanes: total=%d", len(lanes))}
	for _, lane := range lanes {
		lines = append(lines, FormatLaneSummary(lane))
	}
	return lines
}

func FormatLaneSummary(lane Lane) string {
	active := ""
	if lane.ActiveTaskKey != "" {
		active = fmt.Sprintf(" active_task=%s active_role=%s active_lease=%s active_started_at=%s", emptyAsNA(lane.ActiveTaskKey), emptyAsNA(lane.ActiveTaskRole), emptyAsNA(lane.ActiveLeaseName), FormatOptionalTime(lane.ActiveTaskStartedAt))
	}
	errorText := ""
	if lane.LastError != "" {
		errorText = fmt.Sprintf(" error=%q", lane.LastError)
	}
	return fmt.Sprintf("- lane=%s process=%s cycle=%d recovery_required=%t updated_at=%s%s%s", emptyAsNA(lane.Name), emptyAsNA(lane.ProcessID), lane.CycleNumber, lane.RecoveryRequired, FormatOptionalTime(lane.UpdatedAt), active, errorText)
}

func SummarizeWorkerTasks(tasks []WorkerTask) []string {
	if len(tasks) == 0 {
		return nil
	}
	counts := map[string]int{}
	keys := make([]string, 0)
	for _, task := range tasks {
		key := fmt.Sprintf("%s:%s", emptyAsUnknown(task.Role), emptyAsUnknown(task.Status))
		if _, ok := counts[key]; !ok {
			keys = append(keys, key)
		}
		counts[key]++
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, counts[key]))
	}
	lines := []string{fmt.Sprintf("SQLite worker tasks: total=%d %s", len(tasks), strings.Join(parts, " "))}
	lines = append(lines, "SQLite recent worker tasks:")
	limit := len(tasks)
	if limit > 5 {
		limit = 5
	}
	for i := 0; i < limit; i++ {
		lines = append(lines, FormatWorkerTaskSummary(tasks[i]))
	}
	return lines
}

func FormatWorkerTaskSummary(task WorkerTask) string {
	return fmt.Sprintf("- task=%s role=%s status=%s issue=%s attempt=%d priority=%d lease=%s available_at=%s updated_at=%s", emptyAsNA(task.TaskKey), emptyAsUnknown(task.Role), emptyAsUnknown(task.Status), emptyAsNA(task.IssueKey), task.Attempt, task.Priority, emptyAsNA(task.LeaseName), FormatOptionalTime(task.AvailableAt), FormatOptionalTime(task.UpdatedAt))
}

func SummarizeWorkerResults(results []WorkerResult) []string {
	if len(results) == 0 {
		return nil
	}
	counts := map[string]int{}
	keys := make([]string, 0)
	for _, result := range results {
		key := fmt.Sprintf("%s:%s", emptyAsUnknown(result.Role), emptyAsUnknown(result.Status))
		if _, ok := counts[key]; !ok {
			keys = append(keys, key)
		}
		counts[key]++
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, counts[key]))
	}
	lines := []string{fmt.Sprintf("SQLite worker results: total=%d %s", len(results), strings.Join(parts, " "))}
	lines = append(lines, "SQLite recent worker results:")
	limit := len(results)
	if limit > 5 {
		limit = 5
	}
	for i := 0; i < limit; i++ {
		lines = append(lines, FormatWorkerResultSummary(results[i]))
	}
	return lines
}

func FormatWorkerResultSummary(result WorkerResult) string {
	errorText := ""
	if strings.TrimSpace(result.Error) != "" {
		errorText = fmt.Sprintf(" error=%q", result.Error)
	}
	return fmt.Sprintf("- task=%s role=%s lane=%s status=%s did_work=%t reason=%s issue=%s attempt=%d finished_at=%s%s", emptyAsNA(result.TaskKey), emptyAsUnknown(result.Role), emptyAsNA(result.LaneName), emptyAsUnknown(result.Status), result.DidWork, emptyAsNA(result.Reason), emptyAsNA(result.IssueKey), result.Attempt, FormatOptionalTime(result.FinishedAt), errorText)
}

func FormatOptionalTime(value time.Time) string {
	if value.IsZero() {
		return "UNKNOWN"
	}
	return value.UTC().Format(time.RFC3339)
}

func FormatEventSummary(event EventSummary) string {
	issue := emptyAsNA(event.IssueKey)
	return fmt.Sprintf("- #%d %s issue=%s source=%s at=%s", event.Sequence, event.Type, issue, event.Source, event.OccurredAt.UTC().Format(time.RFC3339))
}

func emptyAsUnknown(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return value
}

func emptyAsNA(value string) string {
	if strings.TrimSpace(value) == "" {
		return "n/a"
	}
	return value
}
