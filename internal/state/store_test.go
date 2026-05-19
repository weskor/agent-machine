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
