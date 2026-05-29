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
	IssueIdentifier      string                 `json:"issue_identifier"`
	Title                string                 `json:"title,omitempty"`
	AMStatus             string                 `json:"am_status"`
	LaneRoleHint         string                 `json:"lane_role_hint,omitempty"`
	Age                  string                 `json:"age,omitempty"`
	Attention            string                 `json:"attention"`
	UpdatedAt            time.Time              `json:"updated_at,omitempty"`
	Source               string                 `json:"source"`
	LinearState          string                 `json:"linear_state,omitempty"`
	Attempt              int                    `json:"attempt,omitempty"`
	Workspace            string                 `json:"workspace,omitempty"`
	Branch               string                 `json:"branch,omitempty"`
	PRURL                string                 `json:"pr_url,omitempty"`
	Review               string                 `json:"review,omitempty"`
	Outcome              string                 `json:"outcome,omitempty"`
	PriorityBucket       string                 `json:"priority_bucket"`
	NextAction           surfaceTextEvidence    `json:"next_action"`
	BlockerReason        []string               `json:"blocker_reason,omitempty"`
	CurrentActivity      surfaceCurrentActivity `json:"current_activity"`
	ExternalState        surfaceExternalState   `json:"external_state"`
	AgentEvidenceSummary []string               `json:"agent_evidence_summary,omitempty"`
	RecentEvents         []surfaceRecentEvent   `json:"recent_events,omitempty"`
}

type surfaceTextEvidence struct {
	Code string `json:"code"`
	Text string `json:"text"`
}

type surfaceCurrentActivity struct {
	Lane               string    `json:"lane,omitempty"`
	TaskKey            string    `json:"task_key,omitempty"`
	TaskStatus         string    `json:"task_status,omitempty"`
	Lease              string    `json:"lease,omitempty"`
	Attempt            int       `json:"attempt,omitempty"`
	Workspace          string    `json:"workspace,omitempty"`
	Branch             string    `json:"branch,omitempty"`
	HeartbeatFreshness string    `json:"heartbeat_freshness,omitempty"`
	UpdatedAt          time.Time `json:"updated_at,omitempty"`
}

type surfaceExternalState struct {
	LinearState string `json:"linear_state,omitempty"`
	PRURL       string `json:"pr_url,omitempty"`
	Review      string `json:"review,omitempty"`
	Checks      string `json:"checks,omitempty"`
	Merge       string `json:"merge,omitempty"`
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
		ActiveLocks:   []surfaceLock{},
		ActiveLanes:   []surfaceLane{},
		WorkerTasks:   []surfaceWorkerTask{},
		WorkerResults: []surfaceWorkerResult{},
		RecentEvents:  []surfaceRecentEvent{},
	}
	for _, issue := range orch.Issues {
		out.Issues = append(out.Issues, surfaceIssue{Issue: issue.Issue, Status: issue.Status, Review: issue.Review, PRURL: issue.PRURL, Outcome: issue.Outcome, Source: issue.Source, UpdatedAt: issue.UpdatedAt})
	}
	out.WorkItems = buildSurfaceWorkItems(orch, observedAt)
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

func buildSurfaceWorkItems(orch orchestrationSnapshot, observedAt time.Time) []surfaceWorkItem {
	byIssue := map[string]*surfaceWorkItem{}
	for _, issue := range orch.Issues {
		if issue.Issue == "" {
			continue
		}
		item := surfaceWorkItem{
			IssueIdentifier: issue.Issue,
			AMStatus:        issue.Status,
			Attention:       "none",
			UpdatedAt:       issue.UpdatedAt,
			Source:          issue.Source,
			PRURL:           issue.PRURL,
			Review:          issue.Review,
			Outcome:         issue.Outcome,
			ExternalState:   surfaceExternalState{PRURL: issue.PRURL, Review: issue.Review, Merge: issue.Outcome},
		}
		if issue.Artifact != nil {
			item.NextAction = surfaceNextAction(issue.Artifact.NextAction, issue.Status)
			item.BlockerReason = surfaceBlockers(*issue.Artifact)
			item.AgentEvidenceSummary = surfaceAgentEvidence(*issue.Artifact)
			item.ExternalState.Checks = issue.Artifact.ChecksStatus
		} else {
			item.NextAction = surfaceNextAction("", issue.Status)
		}
		if item.UpdatedAt.IsZero() && issue.Artifact != nil && issue.Artifact.HasArtifact {
			item.UpdatedAt = observedAt
		}
		byIssue[issue.Issue] = &item
	}
	for _, task := range orch.WorkerTasks {
		if task.IssueKey == "" {
			continue
		}
		item := byIssue[task.IssueKey]
		if item == nil {
			item = &surfaceWorkItem{IssueIdentifier: task.IssueKey, AMStatus: task.Status, Attention: "none", Source: "worker_task"}
			byIssue[task.IssueKey] = item
		}
		if item.AMStatus == "" || surfacePriorityRank(item.PriorityBucket) > surfacePriorityRank(surfaceBucketForTask(task)) {
			item.AMStatus = task.Status
		}
		if item.LaneRoleHint == "" {
			item.LaneRoleHint = task.Role
		}
		if item.Attempt == 0 {
			item.Attempt = task.Attempt
		}
		if item.UpdatedAt.IsZero() || task.UpdatedAt.After(item.UpdatedAt) {
			item.UpdatedAt = task.UpdatedAt
		}
		item.CurrentActivity.TaskKey = task.TaskKey
		item.CurrentActivity.TaskStatus = task.Status
		item.CurrentActivity.Attempt = task.Attempt
		item.CurrentActivity.Lease = task.LeaseName
		item.CurrentActivity.UpdatedAt = task.UpdatedAt
		if item.NextAction.Code == "" {
			item.NextAction = surfaceNextAction("", task.Status)
		}
	}
	for _, lock := range orch.ActiveLocks {
		if lock.Issue == "" {
			continue
		}
		item := byIssue[lock.Issue]
		if item == nil {
			item = &surfaceWorkItem{IssueIdentifier: lock.Issue, AMStatus: "active", Attention: "none", Source: "active_lock"}
			byIssue[lock.Issue] = item
		}
		item.Workspace = lock.Workspace
		item.CurrentActivity.Workspace = lock.Workspace
		item.CurrentActivity.Lease = surfaceFirstNonEmpty(item.CurrentActivity.Lease, lock.Owner)
		item.CurrentActivity.HeartbeatFreshness = map[bool]string{true: "stale", false: "fresh"}[lock.Stale]
		if lock.Stale {
			item.Attention = "stale"
			item.BlockerReason = appendUnique(item.BlockerReason, "stale active run lock")
		}
		if item.UpdatedAt.IsZero() || lock.RenewedAt.After(item.UpdatedAt) {
			item.UpdatedAt = lock.RenewedAt
		}
	}
	for _, lane := range orch.ActiveLanes {
		if lane.ActiveTaskKey == "" && lane.ActiveTaskRole == "" {
			continue
		}
		for _, item := range byIssue {
			if item.CurrentActivity.TaskKey != "" && item.CurrentActivity.TaskKey != lane.ActiveTaskKey {
				continue
			}
			if item.CurrentActivity.TaskKey == "" && item.LaneRoleHint != lane.ActiveTaskRole {
				continue
			}
			item.CurrentActivity.Lane = lane.Name
			item.CurrentActivity.Lease = surfaceFirstNonEmpty(item.CurrentActivity.Lease, lane.ActiveLeaseName)
			if item.LaneRoleHint == "" {
				item.LaneRoleHint = lane.ActiveTaskRole
			}
		}
	}
	eventsByIssue := map[string][]surfaceRecentEvent{}
	for _, event := range orch.RecentEvents {
		if event.IssueKey == "" {
			continue
		}
		eventsByIssue[event.IssueKey] = append(eventsByIssue[event.IssueKey], surfaceRecentEvent(event))
	}
	items := make([]surfaceWorkItem, 0, len(byIssue))
	for _, item := range byIssue {
		if item.IssueIdentifier == "" {
			continue
		}
		if item.AMStatus == "" {
			item.AMStatus = "n/a"
		}
		if item.NextAction.Code == "" {
			item.NextAction = surfaceNextAction("", item.AMStatus)
		}
		item.PriorityBucket = surfaceBucket(*item)
		if item.LaneRoleHint == "" {
			item.LaneRoleHint = surfaceLaneHint(*item)
		}
		if item.Age == "" {
			item.Age = surfaceAge(item.UpdatedAt, observedAt)
		}
		if item.Attention == "" {
			item.Attention = "none"
		}
		item.RecentEvents = eventsByIssue[item.IssueIdentifier]
		items = append(items, *item)
	}
	sort.Slice(items, func(i, j int) bool {
		if surfacePriorityRank(items[i].PriorityBucket) != surfacePriorityRank(items[j].PriorityBucket) {
			return surfacePriorityRank(items[i].PriorityBucket) < surfacePriorityRank(items[j].PriorityBucket)
		}
		if !items[i].UpdatedAt.Equal(items[j].UpdatedAt) {
			return items[i].UpdatedAt.Before(items[j].UpdatedAt)
		}
		return items[i].IssueIdentifier < items[j].IssueIdentifier
	})
	return items
}

func surfaceBucketForTask(task snapshotWorkerTask) string {
	switch task.Status {
	case "reconciliation_needed":
		return "reconciliation_needed"
	case "failed":
		return "failed_or_blocked_active_work"
	case "claimed", "running":
		return "running_work"
	case "queued":
		return "queued_runnable"
	default:
		return "cleanup_only_or_done"
	}
}

func surfaceBucket(item surfaceWorkItem) string {
	status := strings.ToLower(surfaceFirstNonEmpty(item.CurrentActivity.TaskStatus, item.AMStatus, item.Outcome))
	if status == "reconciliation_needed" {
		return "reconciliation_needed"
	}
	if item.Attention == "stale" || strings.Contains(status, "failed") || strings.Contains(status, "blocked") || status == "timeout" || status == "budget_exceeded" {
		return "failed_or_blocked_active_work"
	}
	if status == "active" || status == "claimed" || status == "running" {
		return "running_work"
	}
	if strings.Contains(status, "review") || strings.Contains(status, "handoff") || strings.Contains(status, "merge") {
		return "review_or_merge_blockers"
	}
	if item.ExternalState.Merge == "mergeable" {
		return "mergeable"
	}
	if status == "queued" || status == "ready" {
		return "queued_runnable"
	}
	return "cleanup_only_or_done"
}

func surfacePriorityRank(bucket string) int {
	switch bucket {
	case "reconciliation_needed":
		return 1
	case "failed_or_blocked_active_work":
		return 2
	case "running_work":
		return 3
	case "review_or_merge_blockers":
		return 4
	case "mergeable":
		return 5
	case "queued_runnable":
		return 6
	case "cleanup_only_or_done":
		return 7
	default:
		return 8
	}
}

func surfaceLaneHint(item surfaceWorkItem) string {
	status := strings.ToLower(item.AMStatus)
	switch {
	case item.PriorityBucket == "reconciliation_needed":
		return "reconciliation"
	case strings.Contains(status, "review"):
		return "review"
	case strings.Contains(status, "handoff"):
		return "handoff"
	case strings.Contains(status, "merge"):
		return "merge-lane"
	case strings.Contains(status, "cleanup") || item.PriorityBucket == "cleanup_only_or_done":
		return "cleanup"
	default:
		return "operator"
	}
}

func surfaceNextAction(explicit, status string) surfaceTextEvidence {
	code := strings.TrimSpace(explicit)
	if code == "" {
		switch strings.ToLower(strings.TrimSpace(status)) {
		case "queued", "ready":
			code = "run_agent"
		case "active", "claimed", "running":
			code = "wait_for_active_work"
		case "review_not_ready":
			code = "wait_for_checks"
		case "review_failed", "failed", "timeout", "budget_exceeded":
			code = "repair_or_retry"
		case "reconciliation_needed":
			code = "repair_state"
		case "success":
			code = "operator_review"
		default:
			code = "operator_review"
		}
	}
	return surfaceTextEvidence{Code: code, Text: strings.ReplaceAll(code, "_", " ")}
}

func surfaceBlockers(artifact artifactSummary) []string {
	var out []string
	out = append(out, artifact.BlockedBy...)
	out = append(out, artifact.MergeBlockerCodes...)
	if artifact.MergeBlockReason != "" {
		out = append(out, artifact.MergeBlockReason)
	}
	if artifact.RootCause != "" && artifact.RootCause != "none" {
		out = append(out, artifact.RootCause)
	}
	if artifact.OperatorAttention {
		out = append(out, "operator_attention")
	}
	return uniqueNonEmpty(out)
}

func surfaceAgentEvidence(artifact artifactSummary) []string {
	var out []string
	if artifact.Outcome != "" {
		out = append(out, "outcome="+artifact.Outcome)
	}
	if artifact.Review != "" {
		out = append(out, "review="+artifact.Review)
	}
	if artifact.ChecksStatus != "" {
		out = append(out, "checks="+artifact.ChecksStatus)
	}
	if artifact.NextAction != "" {
		out = append(out, "next="+artifact.NextAction)
	}
	return uniqueNonEmpty(out)
}

func surfaceAge(value, observedAt time.Time) string {
	if value.IsZero() || observedAt.IsZero() || observedAt.Before(value) {
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

func surfaceFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func appendUnique(values []string, value string) []string {
	if strings.TrimSpace(value) == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func uniqueNonEmpty(values []string) []string {
	var out []string
	for _, value := range values {
		out = appendUnique(out, value)
	}
	return out
}
