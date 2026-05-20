package state

import (
	"context"
	"database/sql"
	"errors"
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

	expectedTables := []string{"cleanup_states", "daemon_heartbeats", "external_fact_snapshots", "feedback_states", "issue_attempts", "leases", "merge_blockers", "pr_mappings", "retry_decisions", "review_states", "schema_migrations", "terminal_outcomes"}
	if !reflect.DeepEqual(firstTables, expectedTables) {
		t.Fatalf("tables = %v; want %v", firstTables, expectedTables)
	}
	expectedIndexes := []string{"idx_daemon_heartbeats_lane", "idx_issue_attempts_status", "idx_leases_expires_at", "idx_merge_blockers_active", "idx_pr_mappings_pr_number"}
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

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
