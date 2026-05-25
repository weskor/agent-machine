package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestOpenInitializesDeterministicSchemaAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	firstTables := tableNames(t, ctx, s.db)
	firstIndexes := indexNames(t, ctx, s.db)
	if err := s.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	s, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("second Open() error = %v", err)
	}
	defer s.Close()
	if got := tableNames(t, ctx, s.db); !reflect.DeepEqual(got, firstTables) {
		t.Fatalf("tables changed after re-open\nfirst=%v\nsecond=%v", firstTables, got)
	}
	if got := indexNames(t, ctx, s.db); !reflect.DeepEqual(got, firstIndexes) {
		t.Fatalf("indexes changed after re-open\nfirst=%v\nsecond=%v", firstIndexes, got)
	}
	if version, err := s.SchemaVersion(ctx); err != nil || version != CurrentSchemaVersion {
		t.Fatalf("SchemaVersion() = %d, %v; want %d, nil", version, err, CurrentSchemaVersion)
	}

	expectedTables := []string{"cleanup_states", "daemon_heartbeats", "external_fact_snapshots", "feedback_states", "issue_attempts", "leases", "merge_blockers", "orchestration_events", "pr_handoff_intents", "pr_mappings", "retry_decisions", "review_states", "schema_migrations", "terminal_outcomes", "worker_payload_refs", "worker_results", "worker_tasks"}
	if !reflect.DeepEqual(firstTables, expectedTables) {
		t.Fatalf("tables = %v; want %v", firstTables, expectedTables)
	}
	expectedIndexes := []string{"idx_daemon_heartbeats_lane", "idx_issue_attempts_status", "idx_leases_expires_at", "idx_merge_blockers_active", "idx_orchestration_events_issue", "idx_orchestration_events_type", "idx_pr_handoff_intents_issue", "idx_pr_handoff_intents_status", "idx_pr_mappings_pr_number", "idx_worker_payload_refs_issue", "idx_worker_payload_refs_role_phase_status", "idx_worker_results_lane_status", "idx_worker_results_role_status", "idx_worker_tasks_issue", "idx_worker_tasks_role_status"}
	for _, name := range expectedIndexes {
		if !contains(firstIndexes, name) {
			t.Fatalf("missing expected index %q in %v", name, firstIndexes)
		}
	}
}

func TestAcquireLeaseAllowsOnlyOneActiveOwner(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.Close()
	now := time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)
	lease := Lease{Name: "run:CAG-109", Scope: "root", Owner: "owner-a", AcquiredAt: now, RenewedAt: now, ExpiresAt: now.Add(time.Hour)}
	acquired, err := s.AcquireLease(ctx, lease, now)
	if err != nil || !acquired {
		t.Fatalf("first AcquireLease acquired=%v err=%v", acquired, err)
	}
	second := lease
	second.Owner = "owner-b"
	acquired, err = s.AcquireLease(ctx, second, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("second AcquireLease error = %v", err)
	}
	if acquired {
		t.Fatal("second AcquireLease acquired active lease; want blocked")
	}
}

func TestAppendEventOrdersAndRoundTripsPayload(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.Close()
	now := time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)
	first, err := s.AppendEvent(ctx, EventInput{OccurredAt: now, IssueKey: "CAG-87", IssueID: "issue-id", Attempt: 1, RunID: "run-1", Source: "test", Type: EventAttemptStarted, Payload: map[string]any{"status": "running"}})
	if err != nil {
		t.Fatalf("AppendEvent(first) error = %v", err)
	}
	second, err := s.AppendEvent(ctx, EventInput{OccurredAt: now.Add(time.Second), IssueKey: "CAG-87", Attempt: 1, Source: "test", Type: EventAttemptFinished, Payload: json.RawMessage(`{"status":"success","tokens":10}`)})
	if err != nil {
		t.Fatalf("AppendEvent(second) error = %v", err)
	}
	if first.ID == "" || second.ID == "" || first.ID == second.ID {
		t.Fatalf("event IDs not stable unique values: first=%q second=%q", first.ID, second.ID)
	}
	events, err := s.RecentEvents(ctx, 10)
	if err != nil {
		t.Fatalf("RecentEvents() error = %v", err)
	}
	if len(events) != 2 || events[0].Type != EventAttemptStarted || events[1].Type != EventAttemptFinished {
		t.Fatalf("events = %+v; want append order", events)
	}
	var payload struct {
		Status string `json:"status"`
		Tokens int    `json:"tokens"`
	}
	if err := json.Unmarshal(events[1].Payload, &payload); err != nil {
		t.Fatalf("payload unmarshal error = %v", err)
	}
	if payload.Status != "success" || payload.Tokens != 10 {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestEventsFiltersByIssueAttemptTypeAndGlobalRecentOrder(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.Close()
	now := time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)
	inputs := []EventInput{
		{OccurredAt: now, IssueKey: "CAG-1", IssueID: "issue-1", Attempt: 1, Source: "test", Type: EventAttemptStarted, Payload: map[string]any{"n": 1}},
		{OccurredAt: now.Add(time.Second), IssueKey: "CAG-1", IssueID: "issue-1", Attempt: 2, Source: "test", Type: EventAttemptStarted, Payload: map[string]any{"n": 2}},
		{OccurredAt: now.Add(2 * time.Second), IssueKey: "CAG-2", IssueID: "issue-2", Attempt: 1, Source: "test", Type: EventAttemptFinished, Payload: map[string]any{"n": 3}},
	}
	for _, input := range inputs {
		if _, err := s.AppendEvent(ctx, input); err != nil {
			t.Fatalf("AppendEvent(%+v) error = %v", input, err)
		}
	}

	recent, err := s.RecentEvents(ctx, 2)
	if err != nil {
		t.Fatalf("RecentEvents() error = %v", err)
	}
	if got := eventIssueAttempts(recent); !reflect.DeepEqual(got, []string{"CAG-1#2", "CAG-2#1"}) {
		t.Fatalf("recent issue attempts = %v; want latest two in append order", got)
	}

	filtered, err := s.Events(ctx, EventFilter{IssueKey: "CAG-1", Attempt: 2, Type: EventAttemptStarted, Limit: 10})
	if err != nil {
		t.Fatalf("Events(filter) error = %v", err)
	}
	if got := eventIssueAttempts(filtered); !reflect.DeepEqual(got, []string{"CAG-1#2"}) {
		t.Fatalf("filtered issue attempts = %v; want CAG-1#2", got)
	}

	byIssueID, err := s.Events(ctx, EventFilter{IssueID: "issue-2", Limit: 10})
	if err != nil {
		t.Fatalf("Events(issue id) error = %v", err)
	}
	if got := eventIssueAttempts(byIssueID); !reflect.DeepEqual(got, []string{"CAG-2#1"}) {
		t.Fatalf("issue-id filtered events = %v; want CAG-2#1", got)
	}
}

func TestAppendEventRejectsInvalidPayloadWithoutMutatingLog(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.Close()
	if _, err := s.AppendEvent(ctx, EventInput{Source: "test", Type: EventErrorRecorded, Payload: json.RawMessage(`{"unterminated"`)}); err == nil {
		t.Fatal("AppendEvent() error = nil; want invalid payload error")
	}
	counts, err := s.Counts(ctx)
	if err != nil {
		t.Fatalf("Counts() error = %v", err)
	}
	if counts.Events != 0 {
		t.Fatalf("events count = %d; want 0", counts.Events)
	}
}

func TestWorkerTasksUpsertAndFilterByRole(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.Close()
	now := time.Date(2026, 5, 23, 10, 0, 0, 0, time.UTC)
	tasks := []WorkerTask{
		{TaskKey: "implementation:CAG-200:1", Role: "implementation", IssueKey: "CAG-200", IssueID: "issue-200", Attempt: 1, Priority: 10, AvailableAt: now.Add(time.Minute), LeaseName: "run:CAG-200", Payload: json.RawMessage(`{"workspace":"workspaces/CAG-200"}`), CreatedAt: now, UpdatedAt: now},
		{TaskKey: "review:CAG-200:1", Role: "review", IssueKey: "CAG-200", IssueID: "issue-200", Attempt: 1, Priority: 5, AvailableAt: now, LeaseName: "review:CAG-200", Payload: json.RawMessage(`{"pr_url":"https://github.com/acme/repo/pull/200"}`), CreatedAt: now, UpdatedAt: now},
	}
	for _, task := range tasks {
		if err := s.UpsertWorkerTask(ctx, task); err != nil {
			t.Fatalf("UpsertWorkerTask(%s) error = %v", task.TaskKey, err)
		}
	}
	if err := s.UpsertWorkerTask(ctx, WorkerTask{TaskKey: "review:CAG-200:1", Role: "review", IssueKey: "CAG-200", Attempt: 1, Status: "claimed", Priority: 7, AvailableAt: now.Add(2 * time.Minute), LeaseName: "review:CAG-200", Payload: json.RawMessage(`{"pr_url":"https://github.com/acme/repo/pull/200","retry":true}`), CreatedAt: now, UpdatedAt: now.Add(time.Minute)}); err != nil {
		t.Fatalf("UpsertWorkerTask(update) error = %v", err)
	}

	reviewTasks, err := s.WorkerTasks(ctx, "review")
	if err != nil {
		t.Fatalf("WorkerTasks(review) error = %v", err)
	}
	if len(reviewTasks) != 1 {
		t.Fatalf("review tasks = %+v; want one", reviewTasks)
	}
	task := reviewTasks[0]
	if task.TaskKey != "review:CAG-200:1" || task.Status != "claimed" || task.Priority != 7 || task.LeaseName != "review:CAG-200" {
		t.Fatalf("updated review task = %+v", task)
	}
	var payload struct {
		Retry bool `json:"retry"`
	}
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		t.Fatalf("payload unmarshal error = %v", err)
	}
	if !payload.Retry {
		t.Fatalf("payload = %s; want retry=true", string(task.Payload))
	}
	counts, err := s.Counts(ctx)
	if err != nil {
		t.Fatalf("Counts() error = %v", err)
	}
	if counts.WorkerTasks != 2 {
		t.Fatalf("WorkerTasks count = %d; want 2", counts.WorkerTasks)
	}
}

func TestWorkerTaskClaimAndCompletionAreAtomic(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.Close()
	now := time.Date(2026, 5, 23, 10, 0, 0, 0, time.UTC)
	if err := s.UpsertWorkerTask(ctx, WorkerTask{TaskKey: "continuous:merge", Role: "merge", Status: "queued", AvailableAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("UpsertWorkerTask() error = %v", err)
	}

	task, claimed, err := s.ClaimWorkerTask(ctx, "continuous:merge", now.Add(time.Second))
	if err != nil || !claimed {
		t.Fatalf("ClaimWorkerTask() claimed=%v err=%v", claimed, err)
	}
	if task.Status != "claimed" {
		t.Fatalf("claimed task status = %q, want claimed", task.Status)
	}
	if _, claimed, err := s.ClaimWorkerTask(ctx, "continuous:merge", now.Add(2*time.Second)); err != nil || claimed {
		t.Fatalf("second ClaimWorkerTask() claimed=%v err=%v, want false nil", claimed, err)
	}
	if err := s.CompleteWorkerTask(ctx, "continuous:merge", "completed", now.Add(3*time.Second)); err != nil {
		t.Fatalf("CompleteWorkerTask() error = %v", err)
	}
	tasks, err := s.WorkerTasks(ctx, "merge")
	if err != nil {
		t.Fatalf("WorkerTasks() error = %v", err)
	}
	if len(tasks) != 1 || tasks[0].Status != "completed" {
		t.Fatalf("tasks = %+v; want completed merge task", tasks)
	}
}

func TestMarkStaleClaimedWorkerTasksReconciliationNeeded(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.Close()
	now := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)
	old := now.Add(-30 * time.Minute)
	if err := s.UpsertWorkerTask(ctx, WorkerTask{TaskKey: "implementation:CAG-201:1", Role: "implementation", IssueKey: "CAG-201", IssueID: "issue-201", Attempt: 1, Status: WorkerTaskStatusClaimed, AvailableAt: old, UpdatedAt: old}); err != nil {
		t.Fatalf("UpsertWorkerTask(stale) error = %v", err)
	}
	if err := s.UpsertWorkerTask(ctx, WorkerTask{TaskKey: "implementation:CAG-202:1", Role: "implementation", IssueKey: "CAG-202", IssueID: "issue-202", Attempt: 1, Status: WorkerTaskStatusClaimed, AvailableAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("UpsertWorkerTask(fresh) error = %v", err)
	}

	recovered, err := s.MarkStaleClaimedWorkerTasksReconciliationNeeded(ctx, now, 15*time.Minute)
	if err != nil {
		t.Fatalf("MarkStaleClaimedWorkerTasksReconciliationNeeded() error = %v", err)
	}
	if len(recovered) != 1 || recovered[0].TaskKey != "implementation:CAG-201:1" || recovered[0].Status != WorkerTaskStatusReconciliationNeeded {
		t.Fatalf("recovered = %+v; want stale implementation task marked reconciliation_needed", recovered)
	}
	stale, ok, err := s.WorkerTask(ctx, "implementation:CAG-201:1")
	if err != nil || !ok {
		t.Fatalf("WorkerTask(stale) ok=%v err=%v", ok, err)
	}
	if stale.Status != WorkerTaskStatusReconciliationNeeded {
		t.Fatalf("stale status = %q, want reconciliation_needed", stale.Status)
	}
	fresh, ok, err := s.WorkerTask(ctx, "implementation:CAG-202:1")
	if err != nil || !ok {
		t.Fatalf("WorkerTask(fresh) ok=%v err=%v", ok, err)
	}
	if fresh.Status != WorkerTaskStatusClaimed {
		t.Fatalf("fresh status = %q, want claimed", fresh.Status)
	}
	results, err := s.WorkerResults(ctx, "implementation")
	if err != nil {
		t.Fatalf("WorkerResults() error = %v", err)
	}
	if len(results) != 1 || results[0].TaskKey != "implementation:CAG-201:1" || results[0].Status != WorkerTaskStatusReconciliationNeeded || results[0].Reason != "stale_claim_reconciliation_required" {
		t.Fatalf("worker results = %+v; want stale claim reconciliation result", results)
	}
	events, err := s.Events(ctx, EventFilter{Type: EventReconciliationNeeded, Limit: 10})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if len(events) != 1 || events[0].IssueKey != "CAG-201" {
		t.Fatalf("reconciliation events = %+v; want CAG-201 event", events)
	}
}

func TestMarkStaleClaimedWorkerTasksRequiresExpiredLeaseAndStaleHeartbeat(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.Close()
	now := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)
	old := now.Add(-30 * time.Minute)
	task := WorkerTask{TaskKey: "merge:CAG-203:1", Role: "merge", IssueKey: "CAG-203", IssueID: "issue-203", Attempt: 1, Status: WorkerTaskStatusClaimed, AvailableAt: old, LeaseName: "worker:merge:CAG-203", UpdatedAt: old}
	if err := s.UpsertWorkerTask(ctx, task); err != nil {
		t.Fatalf("UpsertWorkerTask() error = %v", err)
	}
	if err := s.UpsertLease(ctx, Lease{Name: task.LeaseName, Scope: task.TaskKey, Owner: "host:123", AcquiredAt: old, RenewedAt: old, ExpiresAt: now.Add(time.Minute)}); err != nil {
		t.Fatalf("UpsertLease(active) error = %v", err)
	}
	if recovered, err := s.MarkStaleClaimedWorkerTasksReconciliationNeeded(ctx, now, 15*time.Minute); err != nil || len(recovered) != 0 {
		t.Fatalf("active lease recovery = %+v err=%v; want no recovery", recovered, err)
	}
	if err := s.UpsertLease(ctx, Lease{Name: task.LeaseName, Scope: task.TaskKey, Owner: "host:123", AcquiredAt: old, RenewedAt: old, ExpiresAt: old.Add(time.Minute)}); err != nil {
		t.Fatalf("UpsertLease(expired) error = %v", err)
	}
	if err := s.UpsertDaemonHeartbeat(ctx, DaemonHeartbeat{ProcessID: "host:123", LaneName: "merge", WorkflowPath: "/repo/am.yaml", CycleNumber: 1, LastSuccessAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("UpsertDaemonHeartbeat() error = %v", err)
	}
	if recovered, err := s.MarkStaleClaimedWorkerTasksReconciliationNeeded(ctx, now, 15*time.Minute); err != nil || len(recovered) != 0 {
		t.Fatalf("fresh heartbeat recovery = %+v err=%v; want no recovery", recovered, err)
	}
	if err := s.UpsertDaemonHeartbeat(ctx, DaemonHeartbeat{ProcessID: "host:123", LaneName: "merge", WorkflowPath: "/repo/am.yaml", CycleNumber: 1, LastSuccessAt: old, UpdatedAt: old}); err != nil {
		t.Fatalf("UpsertDaemonHeartbeat(stale) error = %v", err)
	}
	recovered, err := s.MarkStaleClaimedWorkerTasksReconciliationNeeded(ctx, now, 15*time.Minute)
	if err != nil {
		t.Fatalf("MarkStaleClaimedWorkerTasksReconciliationNeeded() error = %v", err)
	}
	if len(recovered) != 1 || recovered[0].TaskKey != task.TaskKey {
		t.Fatalf("recovered = %+v; want expired lease with stale heartbeat recovered", recovered)
	}
}

func TestClaimNextWorkerTaskClaimsAvailableRoleByPriority(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.Close()
	now := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)
	tasks := []WorkerTask{
		{TaskKey: "implementation:CAG-low:1", Role: "implementation", IssueKey: "CAG-low", Attempt: 1, Status: "queued", Priority: 1, AvailableAt: now, UpdatedAt: now},
		{TaskKey: "implementation:CAG-high:1", Role: "implementation", IssueKey: "CAG-high", Attempt: 1, Status: "queued", Priority: 10, AvailableAt: now, UpdatedAt: now},
		{TaskKey: "implementation:CAG-later:1", Role: "implementation", IssueKey: "CAG-later", Attempt: 1, Status: "queued", Priority: 99, AvailableAt: now.Add(time.Hour), UpdatedAt: now},
		{TaskKey: "review:CAG-review:1", Role: "review", IssueKey: "CAG-review", Attempt: 1, Status: "queued", Priority: 99, AvailableAt: now, UpdatedAt: now},
	}
	for _, task := range tasks {
		if err := s.UpsertWorkerTask(ctx, task); err != nil {
			t.Fatalf("UpsertWorkerTask(%s) error = %v", task.TaskKey, err)
		}
	}

	claimed, ok, err := s.ClaimNextWorkerTask(ctx, "implementation", now)
	if err != nil || !ok {
		t.Fatalf("ClaimNextWorkerTask() ok=%v err=%v", ok, err)
	}
	if claimed.TaskKey != "implementation:CAG-high:1" || claimed.Status != "claimed" {
		t.Fatalf("claimed task = %+v; want high priority implementation task", claimed)
	}
	next, ok, err := s.ClaimNextWorkerTask(ctx, "implementation", now)
	if err != nil || !ok {
		t.Fatalf("second ClaimNextWorkerTask() ok=%v err=%v", ok, err)
	}
	if next.TaskKey != "implementation:CAG-low:1" {
		t.Fatalf("second claimed task = %+v; want low priority implementation task", next)
	}
	if _, ok, err := s.ClaimNextWorkerTask(ctx, "implementation", now); err != nil || ok {
		t.Fatalf("third ClaimNextWorkerTask() ok=%v err=%v; want no available implementation task", ok, err)
	}
	review, ok, err := s.ClaimNextWorkerTask(ctx, "review", now)
	if err != nil || !ok || review.TaskKey != "review:CAG-review:1" {
		t.Fatalf("review ClaimNextWorkerTask() task=%+v ok=%v err=%v", review, ok, err)
	}
}

func TestWorkerResultsUpsertAndFilterByRole(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.Close()
	now := time.Date(2026, 5, 24, 10, 0, 0, 0, time.UTC)
	if err := s.UpsertWorkerTask(ctx, WorkerTask{TaskKey: "continuous:merge", Role: "merge", Status: "claimed", AvailableAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("UpsertWorkerTask() error = %v", err)
	}
	if err := s.UpsertWorkerResult(ctx, WorkerResult{TaskKey: "continuous:merge", Role: "merge", LaneName: "merge", Status: "completed", DidWork: true, Reason: "work_completed", Payload: json.RawMessage(`{"did_work":true}`), StartedAt: now, FinishedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second)}); err != nil {
		t.Fatalf("UpsertWorkerResult() insert error = %v", err)
	}
	if err := s.UpsertWorkerResult(ctx, WorkerResult{TaskKey: "continuous:merge", Role: "merge", LaneName: "merge", Status: "failed", DidWork: false, Reason: "worker_error", Error: "boom", Payload: json.RawMessage(`{"error":"boom"}`), StartedAt: now, FinishedAt: now.Add(2 * time.Second), UpdatedAt: now.Add(2 * time.Second)}); err != nil {
		t.Fatalf("UpsertWorkerResult() update error = %v", err)
	}

	results, err := s.WorkerResults(ctx, "merge")
	if err != nil {
		t.Fatalf("WorkerResults() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %+v; want one", results)
	}
	result := results[0]
	if result.TaskKey != "continuous:merge" || result.Status != "failed" || result.Reason != "worker_error" || result.Error != "boom" || result.DidWork {
		t.Fatalf("result = %+v; want latest failed result", result)
	}
	counts, err := s.Counts(ctx)
	if err != nil {
		t.Fatalf("Counts() error = %v", err)
	}
	if counts.WorkerResults != 1 {
		t.Fatalf("WorkerResults count = %d; want 1", counts.WorkerResults)
	}
}

func TestWorkerPayloadRefsTrackPendingPayloads(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.Close()
	now := time.Date(2026, 5, 24, 11, 0, 0, 0, time.UTC)
	ref := WorkerPayloadRef{
		Role:          "review",
		Phase:         "review_pending",
		IssueKey:      "CAG-184",
		IssueID:       "issue-184",
		Attempt:       1,
		WorkspacePath: "/tmp/CAG-184",
		BranchName:    "am/CAG-184-workspace",
		PRURL:         "https://github.com/acme/repo/pull/184",
		PayloadPath:   "/tmp/CAG-184/review-pending.json",
		Status:        "pending",
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := s.UpsertWorkerPayloadRef(ctx, ref); err != nil {
		t.Fatalf("UpsertWorkerPayloadRef() error = %v", err)
	}
	refs, err := s.PendingWorkerPayloadRefs(ctx, "review", "review_pending")
	if err != nil {
		t.Fatalf("PendingWorkerPayloadRefs() error = %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("refs = %+v; want one pending ref", refs)
	}
	if refs[0].IssueKey != ref.IssueKey || refs[0].PayloadPath != ref.PayloadPath || refs[0].PRURL != ref.PRURL {
		t.Fatalf("ref = %+v; want persisted review payload ref", refs[0])
	}
	if err := s.CompleteWorkerPayloadRef(ctx, refs[0], "completed", now.Add(time.Minute)); err != nil {
		t.Fatalf("CompleteWorkerPayloadRef() error = %v", err)
	}
	refs, err = s.PendingWorkerPayloadRefs(ctx, "review", "review_pending")
	if err != nil {
		t.Fatalf("PendingWorkerPayloadRefs(after complete) error = %v", err)
	}
	if len(refs) != 0 {
		t.Fatalf("refs after complete = %+v; want no pending refs", refs)
	}
	counts, err := s.Counts(ctx)
	if err != nil {
		t.Fatalf("Counts() error = %v", err)
	}
	if counts.WorkerPayloadRefs != 1 {
		t.Fatalf("WorkerPayloadRefs count = %d; want 1", counts.WorkerPayloadRefs)
	}
}

func TestPRHandoffIntentsTrackPendingAndResultIdempotently(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.Close()
	now := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)
	intent := PRHandoffIntent{
		IssueKey:      "CAG-185",
		IssueID:       "issue-185",
		Attempt:       1,
		WorkspacePath: "/tmp/CAG-185",
		BranchName:    "am/CAG-185-workspace",
		AgentPRURL:    "https://github.com/acme/repo/pull/old",
		PayloadPath:   "/tmp/CAG-185/pr-handoff-pending.json",
		Status:        PRHandoffIntentStatusPending,
		UpdatedAt:     now,
	}
	if err := s.UpsertPRHandoffIntent(ctx, intent); err != nil {
		t.Fatalf("UpsertPRHandoffIntent() error = %v", err)
	}
	if err := s.CompletePRHandoffIntent(ctx, intent.IssueKey, intent.Attempt, PRHandoffIntentStatusCompleted, "https://github.com/acme/repo/pull/185", "", now.Add(time.Minute)); err != nil {
		t.Fatalf("CompletePRHandoffIntent() error = %v", err)
	}
	intent.AgentPRURL = "https://github.com/acme/repo/pull/new"
	intent.UpdatedAt = now.Add(2 * time.Minute)
	if err := s.UpsertPRHandoffIntent(ctx, intent); err != nil {
		t.Fatalf("UpsertPRHandoffIntent(second) error = %v", err)
	}
	got, ok, err := s.PRHandoffIntent(ctx, intent.IssueKey, intent.Attempt)
	if err != nil || !ok {
		t.Fatalf("PRHandoffIntent() got ok=%t err=%v", ok, err)
	}
	if got.Status != PRHandoffIntentStatusCompleted || got.PRURL != "https://github.com/acme/repo/pull/185" || got.Result != "success" {
		t.Fatalf("intent = %+v; want completed result preserved", got)
	}
	if got.AgentPRURL != intent.AgentPRURL {
		t.Fatalf("intent AgentPRURL = %q, want latest pending evidence %q", got.AgentPRURL, intent.AgentPRURL)
	}
	counts, err := s.Counts(ctx)
	if err != nil {
		t.Fatalf("Counts() error = %v", err)
	}
	if counts.PRHandoffIntents != 1 {
		t.Fatalf("PRHandoffIntents count = %d; want 1", counts.PRHandoffIntents)
	}
}

func TestWorkerTaskClaimWaitsUntilAvailable(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.Close()
	now := time.Date(2026, 5, 23, 10, 0, 0, 0, time.UTC)
	if err := s.UpsertWorkerTask(ctx, WorkerTask{TaskKey: "continuous:work", Role: "scheduler", Status: "queued", AvailableAt: now.Add(time.Hour)}); err != nil {
		t.Fatalf("UpsertWorkerTask() error = %v", err)
	}
	if _, claimed, err := s.ClaimWorkerTask(ctx, "continuous:work", now); err != nil || claimed {
		t.Fatalf("ClaimWorkerTask() claimed=%v err=%v, want unavailable", claimed, err)
	}
}

func TestAppendEventWriteFailureIsIsolatedFromPrimaryError(t *testing.T) {
	ctx := context.Background()
	primaryErr := errors.New("primary runner error")
	_, eventErr := appendEvent(ctx, failingEventExecer{err: errors.New("disk full")}, EventInput{Source: "test", Type: EventErrorRecorded})
	if eventErr == nil {
		t.Fatal("appendEvent() error = nil; want write error")
	}
	if !errors.Is(primaryErr, primaryErr) {
		t.Fatalf("primary error was not preserved: %v", primaryErr)
	}
	combined := errors.Join(primaryErr, eventErr)
	if !errors.Is(combined, primaryErr) || !errors.Is(combined, eventErr) {
		t.Fatalf("combined error does not preserve primary and event failures: %v", combined)
	}
}

func TestOpenMigratesV1DatabaseToEventLogWithoutRewritingExistingState(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx() error = %v", err)
	}
	if err := ensureMigrationTable(ctx, tx); err != nil {
		t.Fatalf("ensureMigrationTable() error = %v", err)
	}
	if err := migrateV1(ctx, tx); err != nil {
		t.Fatalf("migrateV1() error = %v", err)
	}
	seededAt := time.Date(2026, 5, 20, 9, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO issue_attempts(issue_key, attempt, branch_name, base_branch, status, created_at, updated_at) VALUES ('CAG-OLD', 1, 'am/CAG-OLD', 'main', 'running', ?, ?)`, seededAt, seededAt); err != nil {
		t.Fatalf("seed v1 attempt: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit seed v1 db: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close seed db: %v", err)
	}

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open(v1) error = %v", err)
	}
	defer s.Close()
	if version, err := s.SchemaVersion(ctx); err != nil || version != CurrentSchemaVersion {
		t.Fatalf("SchemaVersion() = %d, %v; want %d, nil", version, err, CurrentSchemaVersion)
	}
	var status string
	if err := s.db.QueryRowContext(ctx, `SELECT status FROM issue_attempts WHERE issue_key = 'CAG-OLD' AND attempt = 1`).Scan(&status); err != nil {
		t.Fatalf("read migrated attempt: %v", err)
	}
	if status != "running" {
		t.Fatalf("status = %q; want running", status)
	}
	if _, err := s.AppendEvent(ctx, EventInput{IssueKey: "CAG-OLD", Attempt: 1, Source: "test", Type: EventAttemptStarted}); err != nil {
		t.Fatalf("AppendEvent() on migrated db error = %v", err)
	}
}

func TestMigrationV8AddsAgentMachineOwnershipColumn(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx() error = %v", err)
	}
	if err := ensureMigrationTable(ctx, tx); err != nil {
		t.Fatalf("ensureMigrationTable() error = %v", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE pr_mappings (id INTEGER PRIMARY KEY, repository TEXT NOT NULL, branch_name TEXT NOT NULL, am_previous_owned INTEGER NOT NULL DEFAULT 1)`); err != nil {
		t.Fatalf("create pre-v8 pr_mappings: %v", err)
	}
	if err := migrateV8(ctx, tx); err != nil {
		t.Fatalf("migrateV8() error = %v", err)
	}
	exists, err := tableColumnExists(ctx, tx, "pr_mappings", "am_owned")
	if err != nil {
		t.Fatalf("tableColumnExists() error = %v", err)
	}
	if !exists {
		t.Fatal("migrateV8() did not add pr_mappings.am_owned")
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit migrated db: %v", err)
	}
}

func TestInspectHealthToleratesPreWorkerTaskSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx() error = %v", err)
	}
	if err := ensureMigrationTable(ctx, tx); err != nil {
		t.Fatalf("ensureMigrationTable() error = %v", err)
	}
	if err := migrateV1(ctx, tx); err != nil {
		t.Fatalf("migrateV1() error = %v", err)
	}
	if err := migrateV2(ctx, tx); err != nil {
		t.Fatalf("migrateV2() error = %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit v2 db: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close seed db: %v", err)
	}

	health, err := InspectHealth(ctx, path)
	if err != nil {
		t.Fatalf("InspectHealth(v2) error = %v", err)
	}
	if !health.OK || health.SchemaVersion != 2 || health.Counts.WorkerTasks != 0 {
		t.Fatalf("health = %+v; want ok v2 with zero worker_tasks", health)
	}
}

func TestInspectHealthToleratesPreWorkerResultSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx() error = %v", err)
	}
	if err := ensureMigrationTable(ctx, tx); err != nil {
		t.Fatalf("ensureMigrationTable() error = %v", err)
	}
	if err := migrateV1(ctx, tx); err != nil {
		t.Fatalf("migrateV1() error = %v", err)
	}
	if err := migrateV2(ctx, tx); err != nil {
		t.Fatalf("migrateV2() error = %v", err)
	}
	if err := migrateV3(ctx, tx); err != nil {
		t.Fatalf("migrateV3() error = %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit v3 db: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close seed db: %v", err)
	}

	health, err := InspectHealth(ctx, path)
	if err != nil {
		t.Fatalf("InspectHealth(v3) error = %v", err)
	}
	if !health.OK || health.SchemaVersion != 3 || health.Counts.WorkerResults != 0 {
		t.Fatalf("health = %+v; want ok v3 with zero worker_results", health)
	}
}

func TestAcquireLeaseComparesFractionalExpiryAsTime(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.Close()
	now := time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)
	active := Lease{Name: "run:CAG-109", Scope: "root", Owner: "owner-a", AcquiredAt: now.Add(-time.Minute), RenewedAt: now.Add(-time.Minute), ExpiresAt: now.Add(100 * time.Millisecond)}
	acquired, err := s.AcquireLease(ctx, active, now.Add(-time.Minute))
	if err != nil || !acquired {
		t.Fatalf("seed AcquireLease acquired=%v err=%v", acquired, err)
	}
	contender := Lease{Name: "run:CAG-109", Scope: "root", Owner: "owner-b", AcquiredAt: now, RenewedAt: now, ExpiresAt: now.Add(time.Hour)}
	acquired, err = s.AcquireLease(ctx, contender, now)
	if err != nil {
		t.Fatalf("contender AcquireLease error = %v", err)
	}
	if acquired {
		t.Fatal("AcquireLease reclaimed lease whose fractional expiry is still in the future")
	}
}

func TestAcquireLeaseReclaimsExpiredLeaseAndRecordsEvidence(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.Close()
	now := time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)
	oldLease := Lease{Name: "run:CAG-109", Scope: "root", Owner: "old-owner", AcquiredAt: now.Add(-2 * time.Hour), RenewedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour)}
	if acquired, err := s.AcquireLease(ctx, oldLease, oldLease.AcquiredAt); err != nil || !acquired {
		t.Fatalf("seed AcquireLease acquired=%v err=%v", acquired, err)
	}
	newLease := Lease{Name: "run:CAG-109", Scope: "root", Owner: "new-owner", AcquiredAt: now, RenewedAt: now, ExpiresAt: now.Add(time.Hour)}
	acquired, err := s.AcquireLease(ctx, newLease, now)
	if err != nil || !acquired {
		t.Fatalf("reclaim AcquireLease acquired=%v err=%v", acquired, err)
	}
	var owner string
	if err := s.DB().QueryRowContext(ctx, `SELECT owner FROM leases WHERE name = ?`, "run:CAG-109").Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner != "new-owner" {
		t.Fatalf("owner=%q, want new-owner", owner)
	}
	var facts int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM external_fact_snapshots WHERE source = 'lease' AND fact_key = 'lease_reclaim:run:CAG-109' AND fact_json LIKE '%old-owner%' AND fact_json LIKE '%new-owner%' AND fact_json LIKE '%stale%'`).Scan(&facts); err != nil {
		t.Fatal(err)
	}
	if facts != 1 {
		t.Fatalf("reclaim facts=%d, want 1", facts)
	}
}

func TestReclaimLeaseRecordsDeadOwnerEvidence(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.Close()
	now := time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)
	oldLease := Lease{Name: "run:CAG-113", Scope: "root", Owner: "old-owner", AcquiredAt: now.Add(-time.Hour), RenewedAt: now.Add(-time.Hour), ExpiresAt: now.Add(3 * time.Hour)}
	if acquired, err := s.AcquireLease(ctx, oldLease, oldLease.AcquiredAt); err != nil || !acquired {
		t.Fatalf("seed AcquireLease acquired=%v err=%v", acquired, err)
	}
	newLease := Lease{Name: "run:CAG-113", Scope: "root", Owner: "new-owner", AcquiredAt: now, RenewedAt: now, ExpiresAt: now.Add(4 * time.Hour)}
	reclaimed, err := s.ReclaimLease(ctx, newLease, now, "dead_owner")
	if err != nil || !reclaimed {
		t.Fatalf("ReclaimLease reclaimed=%v err=%v", reclaimed, err)
	}
	lease, ok, err := s.Lease(ctx, "run:CAG-113")
	if err != nil || !ok {
		t.Fatalf("Lease ok=%v err=%v", ok, err)
	}
	if lease.Owner != "new-owner" || !lease.ReleasedAt.IsZero() || lease.ReleaseReason != "" {
		t.Fatalf("lease after reclaim = %+v", lease)
	}
	var facts int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM external_fact_snapshots WHERE source = 'lease' AND fact_key = 'lease_reclaim:run:CAG-113' AND fact_json LIKE '%old-owner%' AND fact_json LIKE '%new-owner%' AND fact_json LIKE '%dead_owner%'`).Scan(&facts); err != nil {
		t.Fatal(err)
	}
	if facts != 1 {
		t.Fatalf("reclaim facts=%d, want 1", facts)
	}
}

func TestUpsertDaemonHeartbeatInsertsAndUpdatesLaneProcessRow(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.Close()

	firstSuccess := time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC)
	activeStartedAt := firstSuccess.Add(-time.Minute)
	if err := s.UpsertDaemonHeartbeat(ctx, DaemonHeartbeat{ProcessID: "host:123", LaneName: "work", WorkflowPath: "/repo/am.yaml", CycleNumber: 1, LastSuccessAt: firstSuccess, ActiveTaskKey: "continuous:work", ActiveTaskRole: "implementation", ActiveLeaseName: "lane:work", ActiveTaskStartedAt: activeStartedAt, UpdatedAt: firstSuccess}); err != nil {
		t.Fatalf("UpsertDaemonHeartbeat() insert error = %v", err)
	}
	heartbeats, err := s.SnapshotHeartbeats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(heartbeats) != 1 || heartbeats[0].ActiveTaskKey != "continuous:work" || heartbeats[0].ActiveTaskRole != "implementation" || heartbeats[0].ActiveLeaseName != "lane:work" || !heartbeats[0].ActiveTaskStartedAt.Equal(activeStartedAt) {
		t.Fatalf("active heartbeat fields = %+v", heartbeats)
	}
	failedAt := firstSuccess.Add(time.Minute)
	if err := s.UpsertDaemonHeartbeat(ctx, DaemonHeartbeat{ProcessID: "host:123", LaneName: "work", WorkflowPath: "/repo/am.yaml", CycleNumber: 2, LastError: "boom", RecoveryRequired: true, UpdatedAt: failedAt}); err != nil {
		t.Fatalf("UpsertDaemonHeartbeat() update error = %v", err)
	}
	heartbeats, err = s.SnapshotHeartbeats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(heartbeats) != 1 || heartbeats[0].ActiveTaskKey != "" || !heartbeats[0].ActiveTaskStartedAt.IsZero() {
		t.Fatalf("active heartbeat fields after clear = %+v", heartbeats)
	}

	var count, cycle, recovery int
	var lastSuccess, lastError, updatedAt string
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), MAX(cycle_number), MAX(last_success_at), MAX(last_error), MAX(recovery_required), MAX(updated_at) FROM daemon_heartbeats WHERE process_id = 'host:123' AND lane_name = 'work'`).Scan(&count, &cycle, &lastSuccess, &lastError, &recovery, &updatedAt); err != nil {
		t.Fatal(err)
	}
	if count != 1 || cycle != 2 || lastSuccess != firstSuccess.Format(time.RFC3339Nano) || lastError != "boom" || recovery != 1 || updatedAt != failedAt.Format(time.RFC3339Nano) {
		t.Fatalf("heartbeat row = count=%d cycle=%d last_success=%q last_error=%q recovery=%d updated_at=%q", count, cycle, lastSuccess, lastError, recovery, updatedAt)
	}
}

func TestRequeueReconciliationNeededWorkerTaskRecordsRepairEvent(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.Close()
	now := time.Date(2026, 5, 24, 15, 0, 0, 0, time.UTC)
	task := WorkerTask{
		TaskKey:     "merge:CAG-203:91",
		Role:        "merge",
		IssueKey:    "CAG-203",
		IssueID:     "issue-203",
		Attempt:     1,
		Status:      WorkerTaskStatusReconciliationNeeded,
		AvailableAt: now.Add(-time.Hour),
		Payload:     json.RawMessage(`{"pr_number":91}`),
		UpdatedAt:   now.Add(-time.Hour),
	}
	if err := s.UpsertWorkerTask(ctx, task); err != nil {
		t.Fatalf("UpsertWorkerTask() error = %v", err)
	}

	requeued, err := s.RequeueReconciliationNeededWorkerTask(ctx, task.TaskKey, "operator verified task can retry", now)
	if err != nil {
		t.Fatalf("RequeueReconciliationNeededWorkerTask() error = %v", err)
	}
	if requeued.Status != WorkerTaskStatusQueued || !requeued.AvailableAt.Equal(now) {
		t.Fatalf("requeued = %+v; want queued at repair time", requeued)
	}
	events, err := s.Events(ctx, EventFilter{IssueKey: task.IssueKey, Type: EventWorkerTaskRepaired})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if len(events) != 1 || events[0].IssueKey != task.IssueKey {
		t.Fatalf("repair events = %+v; want one repair event", events)
	}
}

func TestLeaseLifecycleUpsertRenewRelease(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.Close()

	acquired := time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC)
	if err := s.UpsertLease(ctx, Lease{Name: "run:CAG-64", Scope: "/repo/.am/workspaces", Owner: "agent", AcquiredAt: acquired, RenewedAt: acquired, ExpiresAt: acquired.Add(time.Hour)}); err != nil {
		t.Fatalf("UpsertLease() error = %v", err)
	}
	renewed := acquired.Add(10 * time.Minute)
	if err := s.RenewLease(ctx, "run:CAG-64", renewed, renewed.Add(time.Hour)); err != nil {
		t.Fatalf("RenewLease() error = %v", err)
	}
	released := acquired.Add(20 * time.Minute)
	if err := s.ReleaseLease(ctx, "run:CAG-64", released, "released"); err != nil {
		t.Fatalf("ReleaseLease() error = %v", err)
	}

	var expiresAt, renewedAt, releasedAt, reason string
	if err := s.db.QueryRowContext(ctx, `SELECT expires_at, renewed_at, released_at, release_reason FROM leases WHERE name = 'run:CAG-64'`).Scan(&expiresAt, &renewedAt, &releasedAt, &reason); err != nil {
		t.Fatal(err)
	}
	if expiresAt != renewed.Add(time.Hour).Format(time.RFC3339Nano) || renewedAt != renewed.Format(time.RFC3339Nano) || releasedAt != released.Format(time.RFC3339Nano) || reason != "released" {
		t.Fatalf("lease lifecycle row = expires_at=%q renewed_at=%q released_at=%q reason=%q", expiresAt, renewedAt, releasedAt, reason)
	}
}

func TestUpsertCleanupStateRequiresExistingAttemptAndUpdates(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.Close()
	if err := s.UpsertCleanupState(ctx, CleanupState{IssueKey: "CAG-missing", Decision: "completed"}); err == nil {
		t.Fatal("UpsertCleanupState() succeeded without mirrored attempt; want error")
	}
	now := time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC)
	if err := s.UpsertRunArtifact(ctx, RunArtifactSnapshot{IssueKey: "CAG-65", Attempt: 1, BranchName: "am/CAG-65-workspace", BaseBranch: "main", Status: "success", StartedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("UpsertRunArtifact() error = %v", err)
	}
	if err := s.UpsertCleanupState(ctx, CleanupState{IssueKey: "CAG-65", Attempt: 1, WorkspaceExists: true, Eligible: true, Decision: "completed", DeletionResult: "dry_run", ArtifactRef: "/repo/.am-run.json", UpdatedAt: now}); err != nil {
		t.Fatalf("UpsertCleanupState() insert error = %v", err)
	}
	updated := now.Add(time.Minute)
	if err := s.UpsertCleanupState(ctx, CleanupState{IssueKey: "CAG-65", Attempt: 1, WorkspaceExists: false, Eligible: true, Decision: "completed", DeletionResult: "deleted", ArtifactRef: "/repo/.am-run.json", UpdatedAt: updated}); err != nil {
		t.Fatalf("UpsertCleanupState() update error = %v", err)
	}

	var workspaceExists, eligible int
	var decision, deletionResult, artifactRef, updatedAt string
	if err := s.db.QueryRowContext(ctx, `SELECT workspace_exists, eligible, decision, deletion_result, artifact_ref, updated_at FROM cleanup_states`).Scan(&workspaceExists, &eligible, &decision, &deletionResult, &artifactRef, &updatedAt); err != nil {
		t.Fatal(err)
	}
	if workspaceExists != 0 || eligible != 1 || decision != "completed" || deletionResult != "deleted" || artifactRef != "/repo/.am-run.json" || updatedAt != updated.Format(time.RFC3339Nano) {
		t.Fatalf("cleanup state = exists=%d eligible=%d decision=%q result=%q artifact=%q updated=%q", workspaceExists, eligible, decision, deletionResult, artifactRef, updatedAt)
	}
}

func TestHealthReportsWALAndBusyTimeout(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.Close()

	health, err := s.Health(ctx)
	if err != nil {
		t.Fatalf("Health() error = %v", err)
	}
	if !health.OK || health.SchemaVersion != CurrentSchemaVersion || health.JournalMode != "wal" || health.BusyTimeoutMS != busyTimeoutMS {
		t.Fatalf("Health() = %+v", health)
	}
}

func TestDefaultDBPathUsesAgentMachineStateSibling(t *testing.T) {
	root := t.TempDir()
	workspaceRoot := filepath.Join(root, ".am", "workspaces")
	want := filepath.Join(root, ".am", "state", "am.db")
	if got := DefaultDBPath(workspaceRoot); got != want {
		t.Fatalf("DefaultDBPath() = %q, want %q", got, want)
	}
	if got := DefaultDBPath(""); got != "" {
		t.Fatalf("DefaultDBPath(empty) = %q, want empty", got)
	}
}

func TestUpsertRunArtifactReplacesRetryDecision(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.Close()
	snap := RunArtifactSnapshot{IssueKey: "CAG-61", Attempt: 1, Status: "review_failed", RetryReason: "review", RetryNextState: "repair"}
	if err := s.UpsertRunArtifact(ctx, snap); err != nil {
		t.Fatalf("UpsertRunArtifact() error = %v", err)
	}
	snap.RetryReason = "updated"
	if err := s.UpsertRunArtifact(ctx, snap); err != nil {
		t.Fatalf("second UpsertRunArtifact() error = %v", err)
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM retry_decisions`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("retry_decisions count = %d, want 1", count)
	}
}

func TestUpsertAttemptResultPersistsDecisionWithoutArtifactRefs(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.Close()
	now := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)
	result := AttemptResult{
		IssueKey:             "CAG-190",
		IssueID:              "issue-190",
		Attempt:              1,
		WorkspacePath:        "/repo/.am/workspaces/CAG-190",
		BranchName:           "am/CAG-190-workspace",
		BaseBranch:           "main",
		Status:               "review_not_ready",
		StartedAt:            now.Add(-time.Minute),
		UpdatedAt:            now,
		Repository:           "weskor/agent-machine",
		PRNumber:             190,
		PRURL:                "https://github.com/weskor/agent-machine/pull/190",
		ReviewStatus:         "failed",
		ReviewClassification: "behavior_spec_blocker",
		ReviewOutputRef:      "/tmp/review-output.txt",
		ReviewOutputHash:     "review-output-hash",
		FeedbackHash:         "feedback-hash",
		FeedbackNextAction:   "retry_with_unresolved_pr_feedback",
		RetryReason:          "waiting_for_checks",
		RetryNextState:       "retry_after_backoff",
		TerminalOutcome:      "waiting_for_checks",
		TerminalReason:       "review not ready",
	}
	if err := s.UpsertAttemptResult(ctx, result); err != nil {
		t.Fatalf("UpsertAttemptResult() error = %v", err)
	}
	facts, ok, err := s.ReconciliationFacts(ctx, "CAG-190")
	if err != nil || !ok {
		t.Fatalf("ReconciliationFacts() ok=%v err=%v", ok, err)
	}
	if facts.Status != "review_not_ready" || facts.PRURL != result.PRURL || facts.ReviewStatus != "failed" || facts.ReviewClassification != "behavior_spec_blocker" || facts.ReviewOutputRef != "/tmp/review-output.txt" || facts.ReviewOutputHash != "review-output-hash" || facts.FeedbackHash != "feedback-hash" || facts.FeedbackNextAction != "retry_with_unresolved_pr_feedback" || facts.RetryNextState != "retry_after_backoff" || facts.TerminalOutcome != "waiting_for_checks" {
		t.Fatalf("facts = %+v; want typed attempt result facts", facts)
	}
	var artifactRefs int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM external_fact_snapshots WHERE source = 'artifact'`).Scan(&artifactRefs); err != nil {
		t.Fatal(err)
	}
	if artifactRefs != 0 {
		t.Fatalf("artifact refs = %d; want 0 for direct attempt result", artifactRefs)
	}
	events, err := s.Events(ctx, EventFilter{IssueKey: "CAG-190", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := eventTypes(events); !reflect.DeepEqual(got, []string{EventAttemptFinished, EventPRDetected, EventReviewCompleted, EventErrorRecorded}) {
		t.Fatalf("events = %v; want typed attempt result events", got)
	}
}

func TestOpenFailsClosedForFutureSchemaVersion(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	_, err = db.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, checksum TEXT NOT NULL, applied_at TEXT NOT NULL, success INTEGER NOT NULL, error TEXT NOT NULL DEFAULT ''); INSERT INTO schema_migrations(version, name, checksum, applied_at, success) VALUES (99, 'future', 'future', 'now', 1)`)
	if err != nil {
		t.Fatalf("seed future schema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close seed db: %v", err)
	}

	s, err := Open(ctx, path)
	if err == nil {
		_ = s.Close()
		t.Fatal("Open() succeeded for future schema; want error")
	}
	if !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("Open() error = %v; want ErrUnsupportedSchema", err)
	}
}

func TestOpenFailsClosedForFailedMigrationRecord(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	_, err = db.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, checksum TEXT NOT NULL, applied_at TEXT NOT NULL, success INTEGER NOT NULL, error TEXT NOT NULL DEFAULT ''); INSERT INTO schema_migrations(version, name, checksum, applied_at, success, error) VALUES (1, 'failed', 'failed', 'now', 0, 'boom')`)
	if err != nil {
		t.Fatalf("seed failed schema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close seed db: %v", err)
	}

	s, err := Open(ctx, path)
	if err == nil {
		_ = s.Close()
		t.Fatal("Open() succeeded with failed migration; want error")
	}
	if !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("Open() error = %v; want ErrUnsupportedSchema", err)
	}
}

func tableNames(t *testing.T, ctx context.Context, db *sql.DB) []string {
	t.Helper()
	return names(t, ctx, db, `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
}

func indexNames(t *testing.T, ctx context.Context, db *sql.DB) []string {
	t.Helper()
	return names(t, ctx, db, `SELECT name FROM sqlite_master WHERE type = 'index' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
}

func names(t *testing.T, ctx context.Context, db *sql.DB, query string) []string {
	t.Helper()
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		t.Fatalf("query names: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan name: %v", err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	sort.Strings(out)
	return out
}

func eventIssueAttempts(events []Event) []string {
	values := make([]string, 0, len(events))
	for _, event := range events {
		values = append(values, fmt.Sprintf("%s#%d", event.IssueKey, event.Attempt))
	}
	return values
}

func eventTypes(events []Event) []string {
	values := make([]string, 0, len(events))
	for _, event := range events {
		values = append(values, event.Type)
	}
	return values
}

type failingEventExecer struct {
	err error
}

func (e failingEventExecer) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	return nil, e.err
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
