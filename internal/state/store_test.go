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

	expectedTables := []string{"cleanup_states", "daemon_heartbeats", "external_fact_snapshots", "feedback_states", "issue_attempts", "leases", "merge_blockers", "orchestration_events", "pr_mappings", "retry_decisions", "review_states", "schema_migrations", "terminal_outcomes", "worker_tasks"}
	if !reflect.DeepEqual(firstTables, expectedTables) {
		t.Fatalf("tables = %v; want %v", firstTables, expectedTables)
	}
	expectedIndexes := []string{"idx_daemon_heartbeats_lane", "idx_issue_attempts_status", "idx_leases_expires_at", "idx_merge_blockers_active", "idx_orchestration_events_issue", "idx_orchestration_events_type", "idx_pr_mappings_pr_number", "idx_worker_tasks_issue", "idx_worker_tasks_role_status"}
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO issue_attempts(issue_key, attempt, branch_name, base_branch, status, created_at, updated_at) VALUES ('CAG-OLD', 1, 'symphony/CAG-OLD', 'main', 'running', ?, ?)`, seededAt, seededAt); err != nil {
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
	if err := s.UpsertDaemonHeartbeat(ctx, DaemonHeartbeat{ProcessID: "host:123", LaneName: "work", WorkflowPath: "/repo/WORKFLOW.md", CycleNumber: 1, LastSuccessAt: firstSuccess, UpdatedAt: firstSuccess}); err != nil {
		t.Fatalf("UpsertDaemonHeartbeat() insert error = %v", err)
	}
	failedAt := firstSuccess.Add(time.Minute)
	if err := s.UpsertDaemonHeartbeat(ctx, DaemonHeartbeat{ProcessID: "host:123", LaneName: "work", WorkflowPath: "/repo/WORKFLOW.md", CycleNumber: 2, LastError: "boom", RecoveryRequired: true, UpdatedAt: failedAt}); err != nil {
		t.Fatalf("UpsertDaemonHeartbeat() update error = %v", err)
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

func TestLeaseLifecycleUpsertRenewRelease(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.Close()

	acquired := time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC)
	if err := s.UpsertLease(ctx, Lease{Name: "run:CAG-64", Scope: "/repo/.symphony/workspaces", Owner: "agent", AcquiredAt: acquired, RenewedAt: acquired, ExpiresAt: acquired.Add(time.Hour)}); err != nil {
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
	if err := s.UpsertRunArtifact(ctx, RunArtifactSnapshot{IssueKey: "CAG-65", Attempt: 1, BranchName: "symphony/CAG-65-workspace", BaseBranch: "main", Status: "success", StartedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("UpsertRunArtifact() error = %v", err)
	}
	if err := s.UpsertCleanupState(ctx, CleanupState{IssueKey: "CAG-65", Attempt: 1, WorkspaceExists: true, Eligible: true, Decision: "completed", DeletionResult: "dry_run", ArtifactRef: "/repo/.pi-symphony-run.json", UpdatedAt: now}); err != nil {
		t.Fatalf("UpsertCleanupState() insert error = %v", err)
	}
	updated := now.Add(time.Minute)
	if err := s.UpsertCleanupState(ctx, CleanupState{IssueKey: "CAG-65", Attempt: 1, WorkspaceExists: false, Eligible: true, Decision: "completed", DeletionResult: "deleted", ArtifactRef: "/repo/.pi-symphony-run.json", UpdatedAt: updated}); err != nil {
		t.Fatalf("UpsertCleanupState() update error = %v", err)
	}

	var workspaceExists, eligible int
	var decision, deletionResult, artifactRef, updatedAt string
	if err := s.db.QueryRowContext(ctx, `SELECT workspace_exists, eligible, decision, deletion_result, artifact_ref, updated_at FROM cleanup_states`).Scan(&workspaceExists, &eligible, &decision, &deletionResult, &artifactRef, &updatedAt); err != nil {
		t.Fatal(err)
	}
	if workspaceExists != 0 || eligible != 1 || decision != "completed" || deletionResult != "deleted" || artifactRef != "/repo/.pi-symphony-run.json" || updatedAt != updated.Format(time.RFC3339Nano) {
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

func TestDefaultDBPathUsesSymphonyStateSibling(t *testing.T) {
	root := t.TempDir()
	workspaceRoot := filepath.Join(root, ".symphony", "workspaces")
	want := filepath.Join(root, ".symphony", "state", "pi-symphony.db")
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
