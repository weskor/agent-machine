package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/weskor/agent-machine/internal/state"
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
	WorkItems        []surfaceWorkItem     `json:"work_items"`
	IssueQueue       []surfaceWorkItem     `json:"issue_queue"`
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

type surfaceWorkItem struct {
	IssueIdentifier string                 `json:"issue_identifier"`
	Title           string                 `json:"title,omitempty"`
	AMStatus        string                 `json:"am_status"`
	LaneRoleHint    string                 `json:"lane_role_hint"`
	Age             string                 `json:"age,omitempty"`
	Attention       string                 `json:"attention"`
	UpdatedAt       time.Time              `json:"updated_at,omitempty"`
	Source          string                 `json:"source"`
	Status          string                 `json:"status,omitempty"`
	Review          string                 `json:"review,omitempty"`
	PRURL           string                 `json:"pr_url,omitempty"`
	Outcome         string                 `json:"outcome,omitempty"`
	Attempt         int                    `json:"attempt,omitempty"`
	Workspace       string                 `json:"workspace,omitempty"`
	Branch          string                 `json:"branch,omitempty"`
	LinearState     string                 `json:"linear_state,omitempty"`
	NextAction      string                 `json:"next_action,omitempty"`
	BlockerReason   string                 `json:"blocker_reason,omitempty"`
	CurrentActivity surfaceCurrentActivity `json:"current_activity,omitempty"`
	ExternalState   surfaceExternalState   `json:"external_state,omitempty"`
	AgentEvidence   surfaceAgentEvidence   `json:"agent_evidence_summary,omitempty"`
	RecentEvents    []surfaceRecentEvent   `json:"recent_events,omitempty"`
	PriorityBucket  string                 `json:"priority_bucket"`
	priorityRank    int
	priority        int
}

type surfaceCurrentActivity struct {
	Lane      string `json:"lane,omitempty"`
	Task      string `json:"task,omitempty"`
	Lease     string `json:"lease,omitempty"`
	Attempt   int    `json:"attempt,omitempty"`
	Workspace string `json:"workspace,omitempty"`
	Branch    string `json:"branch,omitempty"`
	Timing    string `json:"timing,omitempty"`
}

type surfaceExternalState struct {
	Linear string `json:"linear,omitempty"`
	PR     string `json:"pr,omitempty"`
	Review string `json:"review,omitempty"`
	Checks string `json:"checks,omitempty"`
	Merge  string `json:"merge,omitempty"`
}

type surfaceAgentEvidence struct {
	Outcome      string   `json:"outcome,omitempty"`
	RootCause    string   `json:"root_cause,omitempty"`
	NextAction   string   `json:"next_action,omitempty"`
	BlockedBy    []string `json:"blocked_by,omitempty"`
	Review       string   `json:"review,omitempty"`
	WorkerStatus string   `json:"worker_status,omitempty"`
	WorkerReason string   `json:"worker_reason,omitempty"`
	WorkerError  string   `json:"worker_error,omitempty"`
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
		WorkItems:     []surfaceWorkItem{},
		IssueQueue:    []surfaceWorkItem{},
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
	out.IssueQueue = buildSurfaceIssueQueue(orch, observedAt)
	out.WorkItems = append([]surfaceWorkItem{}, out.IssueQueue...)
	return out, nil
}

func buildSurfaceIssueQueue(orch orchestrationSnapshot, observedAt time.Time) []surfaceWorkItem {
	items := map[string]*surfaceWorkItem{}
	for _, issue := range orch.Issues {
		key := issue.Issue
		if key == "" {
			continue
		}
		item := ensureSurfaceWorkItem(items, key)
		item.Source = issue.Source
		item.Status = issue.Status
		item.Review = issue.Review
		item.PRURL = issue.PRURL
		item.Outcome = issue.Outcome
		item.UpdatedAt = latestTime(item.UpdatedAt, issue.UpdatedAt)
		if issue.Artifact != nil {
			applySurfaceArtifactEvidence(item, *issue.Artifact)
		}
	}
	for _, task := range orch.WorkerTasks {
		if task.IssueKey == "" {
			continue
		}
		item := ensureSurfaceWorkItem(items, task.IssueKey)
		item.Attempt = firstPositive(item.Attempt, task.Attempt)
		item.LaneRoleHint = firstSurfaceNonEmpty(item.LaneRoleHint, task.Role)
		item.UpdatedAt = latestTime(item.UpdatedAt, task.UpdatedAt)
		item.priority = maxInt(item.priority, task.Priority)
		item.CurrentActivity.Task = firstSurfaceNonEmpty(item.CurrentActivity.Task, task.TaskKey)
		item.CurrentActivity.Lease = firstSurfaceNonEmpty(item.CurrentActivity.Lease, task.LeaseName)
		item.CurrentActivity.Attempt = firstPositive(item.CurrentActivity.Attempt, task.Attempt)
		if task.UpdatedAt.IsZero() && !task.AvailableAt.IsZero() {
			item.UpdatedAt = latestTime(item.UpdatedAt, task.AvailableAt)
		}
		if task.Status != "" {
			item.AgentEvidence.WorkerStatus = task.Status
		}
		if task.Status == "reconciliation_needed" {
			item.Attention = "reconciliation_needed"
		}
		if task.Status == "failed" {
			item.Attention = firstSurfaceNonEmpty(item.Attention, "failed")
		}
	}
	for _, result := range orch.WorkerResults {
		if result.IssueKey == "" {
			continue
		}
		item := ensureSurfaceWorkItem(items, result.IssueKey)
		item.Attempt = firstPositive(item.Attempt, result.Attempt)
		item.LaneRoleHint = firstSurfaceNonEmpty(item.LaneRoleHint, result.Role)
		item.UpdatedAt = latestTime(item.UpdatedAt, result.UpdatedAt)
		item.CurrentActivity.Lane = firstSurfaceNonEmpty(item.CurrentActivity.Lane, result.LaneName)
		item.CurrentActivity.Task = firstSurfaceNonEmpty(item.CurrentActivity.Task, result.TaskKey)
		item.CurrentActivity.Attempt = firstPositive(item.CurrentActivity.Attempt, result.Attempt)
		item.AgentEvidence.WorkerStatus = firstSurfaceNonEmpty(result.Status, item.AgentEvidence.WorkerStatus)
		item.AgentEvidence.WorkerReason = firstSurfaceNonEmpty(result.Reason, item.AgentEvidence.WorkerReason)
		item.AgentEvidence.WorkerError = firstSurfaceNonEmpty(result.Error, item.AgentEvidence.WorkerError)
		if result.Status == "failed" || result.Error != "" {
			item.Attention = firstSurfaceNonEmpty(item.Attention, "failed")
		}
	}
	for _, lock := range orch.ActiveLocks {
		if lock.Issue == "" {
			continue
		}
		item := ensureSurfaceWorkItem(items, lock.Issue)
		item.Workspace = firstSurfaceNonEmpty(item.Workspace, lock.Workspace)
		item.CurrentActivity.Workspace = firstSurfaceNonEmpty(item.CurrentActivity.Workspace, lock.Workspace)
		item.CurrentActivity.Lease = firstSurfaceNonEmpty(item.CurrentActivity.Lease, lock.Owner)
		item.UpdatedAt = latestTime(item.UpdatedAt, lock.RenewedAt)
		if lock.Active {
			item.LaneRoleHint = firstSurfaceNonEmpty(item.LaneRoleHint, "work-lane")
		}
		if lock.Stale {
			item.Attention = firstSurfaceNonEmpty(item.Attention, "stale")
			item.BlockerReason = firstSurfaceNonEmpty(item.BlockerReason, "stale run lock")
		}
	}
	for _, lane := range orch.ActiveLanes {
		if lane.ActiveTaskKey == "" && lane.ActiveTaskRole == "" {
			continue
		}
		for _, item := range items {
			if item.CurrentActivity.Task == lane.ActiveTaskKey || item.LaneRoleHint == lane.ActiveTaskRole {
				item.CurrentActivity.Lane = firstSurfaceNonEmpty(item.CurrentActivity.Lane, lane.Name)
				item.CurrentActivity.Lease = firstSurfaceNonEmpty(item.CurrentActivity.Lease, lane.ActiveLeaseName)
				item.CurrentActivity.Timing = firstSurfaceNonEmpty(item.CurrentActivity.Timing, formatSurfaceTime(lane.ActiveTaskStartedAt))
				item.UpdatedAt = latestTime(item.UpdatedAt, lane.UpdatedAt)
			}
		}
	}
	for _, event := range orch.RecentEvents {
		if event.IssueKey == "" {
			continue
		}
		item := ensureSurfaceWorkItem(items, event.IssueKey)
		item.RecentEvents = append(item.RecentEvents, surfaceRecentEvent(event))
		item.UpdatedAt = latestTime(item.UpdatedAt, event.OccurredAt)
	}

	queue := make([]surfaceWorkItem, 0, len(items))
	for _, item := range items {
		finalizeSurfaceWorkItem(item, observedAt)
		queue = append(queue, *item)
	}
	sortSurfaceIssueQueue(queue)
	return queue
}

func ensureSurfaceWorkItem(items map[string]*surfaceWorkItem, key string) *surfaceWorkItem {
	if item := items[key]; item != nil {
		return item
	}
	items[key] = &surfaceWorkItem{IssueIdentifier: key, Source: "snapshot", Attention: "none", LaneRoleHint: "n/a", AMStatus: "n/a"}
	return items[key]
}

func applySurfaceArtifactEvidence(item *surfaceWorkItem, artifact artifactSummary) {
	item.AgentEvidence.Outcome = firstSurfaceNonEmpty(item.AgentEvidence.Outcome, artifact.Outcome)
	item.AgentEvidence.RootCause = firstSurfaceNonEmpty(item.AgentEvidence.RootCause, artifact.RootCause)
	item.AgentEvidence.NextAction = firstSurfaceNonEmpty(item.AgentEvidence.NextAction, artifact.NextAction)
	item.AgentEvidence.BlockedBy = append([]string{}, artifact.BlockedBy...)
	item.AgentEvidence.Review = firstSurfaceNonEmpty(item.AgentEvidence.Review, artifact.Review)
	item.NextAction = firstSurfaceNonEmpty(item.NextAction, artifact.NextAction)
	item.PRURL = firstSurfaceNonEmpty(item.PRURL, artifact.PRURL)
	item.Outcome = firstSurfaceNonEmpty(item.Outcome, artifact.Outcome)
	item.Review = firstSurfaceNonEmpty(item.Review, artifact.Review)
	item.ExternalState.Checks = firstSurfaceNonEmpty(item.ExternalState.Checks, artifact.ChecksStatus)
	if artifact.MergeEligible {
		item.ExternalState.Merge = "mergeable"
	}
	if artifact.MergeBlockReason != "" {
		item.BlockerReason = artifact.MergeBlockReason
	}
	if item.BlockerReason == "" && len(artifact.MergeBlockerCodes) > 0 {
		item.BlockerReason = strings.Join(artifact.MergeBlockerCodes, ",")
	}
	if item.BlockerReason == "" && artifact.RootCause != "" && artifact.RootCause != "none" {
		item.BlockerReason = artifact.RootCause
	}
	if artifact.OperatorAttention {
		item.Attention = "operator_review"
	}
	if artifact.ShouldRetry {
		item.Attention = firstSurfaceNonEmpty(item.Attention, "failed")
	}
}

func finalizeSurfaceWorkItem(item *surfaceWorkItem, observedAt time.Time) {
	item.AMStatus = surfaceAMStatus(*item)
	item.PriorityBucket, item.priorityRank = surfacePriorityBucket(*item)
	item.Attention = surfaceAttention(*item)
	item.LaneRoleHint = surfaceLaneRoleHint(*item)
	item.NextAction = firstSurfaceNonEmpty(item.NextAction, surfaceNextAction(*item))
	item.BlockerReason = firstSurfaceNonEmpty(item.BlockerReason, surfaceBlockerReason(*item))
	item.Age = surfaceAge(item.UpdatedAt, observedAt)
	item.ExternalState.Linear = firstSurfaceNonEmpty(item.ExternalState.Linear, item.LinearState)
	item.ExternalState.PR = firstSurfaceNonEmpty(item.ExternalState.PR, item.PRURL)
	item.ExternalState.Review = firstSurfaceNonEmpty(item.ExternalState.Review, item.Review)
	item.CurrentActivity.Attempt = firstPositive(item.CurrentActivity.Attempt, item.Attempt)
	item.CurrentActivity.Workspace = firstSurfaceNonEmpty(item.CurrentActivity.Workspace, item.Workspace)
	item.CurrentActivity.Branch = firstSurfaceNonEmpty(item.CurrentActivity.Branch, item.Branch)
	item.CurrentActivity.Timing = firstSurfaceNonEmpty(item.CurrentActivity.Timing, formatSurfaceTime(item.UpdatedAt))
}

func surfaceAMStatus(item surfaceWorkItem) string {
	switch {
	case item.Attention == "reconciliation_needed" || item.Status == "reconciliation_needed":
		return "reconciliation_needed"
	case item.Status == "active" || item.Status == "running" || item.AgentEvidence.WorkerStatus == "claimed" || item.AgentEvidence.WorkerStatus == "running":
		return "running"
	case item.Review == "failed" || item.Status == "review_failed":
		return "review_blocked"
	case item.ExternalState.Merge == "mergeable":
		return "mergeable"
	case item.Outcome == "needs_info":
		return "needs_info"
	case item.Outcome == "handoff_ready" || item.Status == "success":
		return "done"
	case item.AgentEvidence.WorkerStatus == "queued" || item.Status == "queued":
		return "queued"
	case item.Status != "":
		return item.Status
	default:
		return "n/a"
	}
}

func surfacePriorityBucket(item surfaceWorkItem) (string, int) {
	switch {
	case item.AMStatus == "reconciliation_needed" || item.Attention == "reconciliation_needed":
		return "reconciliation_needed", 0
	case item.Attention == "failed" || item.Attention == "stale" || strings.Contains(item.AMStatus, "failed"):
		return "failed_or_blocked_active_work", 1
	case item.AMStatus == "running":
		return "running_work", 2
	case item.AMStatus == "review_blocked" || item.Attention == "operator_review":
		return "review_or_merge_blockers", 3
	case item.AMStatus == "mergeable":
		return "mergeable", 4
	case item.AMStatus == "queued":
		return "queued_runnable", 5
	default:
		return "cleanup_only_or_done", 6
	}
}

func surfaceAttention(item surfaceWorkItem) string {
	if item.Attention != "" && item.Attention != "none" {
		return item.Attention
	}
	switch {
	case item.AMStatus == "reconciliation_needed":
		return "reconciliation_needed"
	case item.AMStatus == "review_blocked":
		return "operator_review"
	case item.AgentEvidence.WorkerStatus == "failed" || item.AgentEvidence.WorkerError != "":
		return "failed"
	default:
		return "none"
	}
}

func surfaceLaneRoleHint(item surfaceWorkItem) string {
	if item.LaneRoleHint != "" && item.LaneRoleHint != "n/a" {
		return item.LaneRoleHint
	}
	switch item.AMStatus {
	case "mergeable":
		return "merge-lane"
	case "review_blocked":
		return "review"
	case "reconciliation_needed":
		return "reconciliation"
	case "done":
		return "cleanup"
	case "needs_info":
		return "operator"
	default:
		return "n/a"
	}
}

func surfaceNextAction(item surfaceWorkItem) string {
	switch item.AMStatus {
	case "running":
		return "wait_for_active_agent"
	case "review_blocked":
		return "operator_review"
	case "mergeable":
		return "merge_pr"
	case "queued":
		return "run_agent"
	case "done":
		return "cleanup_workspace"
	case "reconciliation_needed":
		return "repair_state"
	default:
		return ""
	}
}

func surfaceBlockerReason(item surfaceWorkItem) string {
	if item.Attention == "none" {
		return ""
	}
	if item.AgentEvidence.WorkerError != "" {
		return item.AgentEvidence.WorkerError
	}
	if item.AgentEvidence.WorkerReason != "" {
		return item.AgentEvidence.WorkerReason
	}
	return item.Attention
}

func sortSurfaceIssueQueue(queue []surfaceWorkItem) {
	sort.Slice(queue, func(i, j int) bool {
		if queue[i].priorityRank != queue[j].priorityRank {
			return queue[i].priorityRank < queue[j].priorityRank
		}
		if queue[i].priority != queue[j].priority {
			return queue[i].priority > queue[j].priority
		}
		if !queue[i].UpdatedAt.Equal(queue[j].UpdatedAt) {
			return queue[i].UpdatedAt.Before(queue[j].UpdatedAt)
		}
		return queue[i].IssueIdentifier < queue[j].IssueIdentifier
	})
}

func surfaceAge(value time.Time, observedAt time.Time) string {
	if value.IsZero() || observedAt.Before(value) {
		return ""
	}
	minutes := int(observedAt.Sub(value).Minutes())
	if minutes < 60 {
		return fmt.Sprintf("%dm", minutes)
	}
	hours := minutes / 60
	if hours < 48 {
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dd", hours/24)
}

func formatSurfaceTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func firstSurfaceNonEmpty(values ...string) string {
	for _, value := range values {
		cleaned := strings.TrimSpace(value)
		if cleaned != "" && cleaned != "n/a" {
			return cleaned
		}
	}
	return ""
}

func firstPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func latestTime(a, b time.Time) time.Time {
	if b.IsZero() || a.After(b) {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
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
