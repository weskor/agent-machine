package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

const (
	CurrentSchemaVersion = 1
	busyTimeoutMS        = 5000
)

var ErrUnsupportedSchema = errors.New("unsupported sqlite orchestration schema")

type Store struct {
	db *sql.DB
}

type Health struct {
	OK            bool
	SchemaVersion int
	JournalMode   string
	BusyTimeoutMS int
}

func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("open state store: path is required")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open state store: %w", err)
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.init(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var version int
	if err := s.db.QueryRowContext(ctx, `SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1`).Scan(&version); err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	if version > CurrentSchemaVersion {
		return version, fmt.Errorf("%w: database version %d is newer than supported version %d", ErrUnsupportedSchema, version, CurrentSchemaVersion)
	}
	return version, nil
}

func (s *Store) Health(ctx context.Context) (Health, error) {
	if err := s.db.PingContext(ctx); err != nil {
		return Health{}, fmt.Errorf("state store health: ping: %w", err)
	}
	version, err := s.SchemaVersion(ctx)
	if err != nil {
		return Health{}, fmt.Errorf("state store health: %w", err)
	}
	var journal string
	if err := s.db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journal); err != nil {
		return Health{}, fmt.Errorf("state store health: journal_mode: %w", err)
	}
	var timeout int
	if err := s.db.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&timeout); err != nil {
		return Health{}, fmt.Errorf("state store health: busy_timeout: %w", err)
	}
	return Health{OK: true, SchemaVersion: version, JournalMode: journal, BusyTimeoutMS: timeout}, nil
}

func (s *Store) init(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `PRAGMA busy_timeout = 5000`); err != nil {
		return fmt.Errorf("configure busy_timeout: %w", err)
	}
	var journal string
	if err := s.db.QueryRowContext(ctx, `PRAGMA journal_mode = WAL`).Scan(&journal); err != nil {
		return fmt.Errorf("configure WAL: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin state migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureMigrationTable(ctx, tx); err != nil {
		return err
	}
	version, err := currentVersion(ctx, tx)
	if err != nil {
		return err
	}
	if version > CurrentSchemaVersion {
		return fmt.Errorf("%w: database version %d is newer than supported version %d", ErrUnsupportedSchema, version, CurrentSchemaVersion)
	}
	if version < 1 {
		if err := migrateV1(ctx, tx); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit state migration: %w", err)
	}
	return nil
}

func ensureMigrationTable(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS schema_migrations (
  version INTEGER PRIMARY KEY,
  name TEXT NOT NULL,
  checksum TEXT NOT NULL,
  applied_at TEXT NOT NULL,
  success INTEGER NOT NULL CHECK (success IN (0, 1)),
  error TEXT NOT NULL DEFAULT ''
)`)
	if err != nil {
		return fmt.Errorf("ensure schema_migrations: %w", err)
	}
	var failed int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE success = 0`).Scan(&failed); err != nil {
		return fmt.Errorf("check failed migrations: %w", err)
	}
	if failed > 0 {
		return fmt.Errorf("%w: database contains %d failed migration record(s)", ErrUnsupportedSchema, failed)
	}
	return nil
}

func currentVersion(ctx context.Context, tx *sql.Tx) (int, error) {
	var version sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations WHERE success = 1`).Scan(&version); err != nil {
		return 0, fmt.Errorf("read current schema version: %w", err)
	}
	if !version.Valid {
		return 0, nil
	}
	return int(version.Int64), nil
}

func migrateV1(ctx context.Context, tx *sql.Tx) error {
	for _, stmt := range v1Schema {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("apply migration v1: %w", err)
		}
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, name, checksum, applied_at, success) VALUES (?, ?, ?, ?, 1)`, CurrentSchemaVersion, "initial orchestration state", "v1", time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("record migration v1: %w", err)
	}
	return nil
}

var v1Schema = []string{
	`CREATE TABLE issue_attempts (id INTEGER PRIMARY KEY, issue_key TEXT NOT NULL, issue_id TEXT NOT NULL DEFAULT '', attempt INTEGER NOT NULL, workspace_path TEXT NOT NULL DEFAULT '', branch_name TEXT NOT NULL, base_branch TEXT NOT NULL, prompt_hash TEXT NOT NULL DEFAULT '', validation_summary TEXT NOT NULL DEFAULT '', status TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, UNIQUE(issue_key, attempt))`,
	`CREATE TABLE pr_mappings (id INTEGER PRIMARY KEY, attempt_id INTEGER NOT NULL REFERENCES issue_attempts(id) ON DELETE CASCADE, repository TEXT NOT NULL, branch_name TEXT NOT NULL, base_branch TEXT NOT NULL, pr_number INTEGER, pr_url TEXT NOT NULL DEFAULT '', head_sha TEXT NOT NULL DEFAULT '', base_sha TEXT NOT NULL DEFAULT '', symphony_owned INTEGER NOT NULL CHECK (symphony_owned IN (0, 1)), updated_at TEXT NOT NULL, UNIQUE(repository, branch_name))`,
	`CREATE TABLE review_states (attempt_id INTEGER PRIMARY KEY REFERENCES issue_attempts(id) ON DELETE CASCADE, command_status TEXT NOT NULL DEFAULT '', passed INTEGER NOT NULL DEFAULT 0 CHECK (passed IN (0, 1)), classification TEXT NOT NULL DEFAULT '', output_ref TEXT NOT NULL DEFAULT '', output_hash TEXT NOT NULL DEFAULT '', merge_eligible INTEGER NOT NULL DEFAULT 0 CHECK (merge_eligible IN (0, 1)), updated_at TEXT NOT NULL)`,
	`CREATE TABLE feedback_states (attempt_id INTEGER PRIMARY KEY REFERENCES issue_attempts(id) ON DELETE CASCADE, feedback_hash TEXT NOT NULL DEFAULT '', incorporated INTEGER NOT NULL DEFAULT 0 CHECK (incorporated IN (0, 1)), stale INTEGER NOT NULL DEFAULT 0 CHECK (stale IN (0, 1)), next_action TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL)`,
	`CREATE TABLE merge_blockers (id INTEGER PRIMARY KEY, attempt_id INTEGER NOT NULL REFERENCES issue_attempts(id) ON DELETE CASCADE, code TEXT NOT NULL, reason TEXT NOT NULL, external_state_hash TEXT NOT NULL DEFAULT '', active INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0, 1)), updated_at TEXT NOT NULL, UNIQUE(attempt_id, code))`,
	`CREATE TABLE cleanup_states (attempt_id INTEGER PRIMARY KEY REFERENCES issue_attempts(id) ON DELETE CASCADE, workspace_exists INTEGER NOT NULL DEFAULT 0 CHECK (workspace_exists IN (0, 1)), eligible INTEGER NOT NULL DEFAULT 0 CHECK (eligible IN (0, 1)), decision TEXT NOT NULL DEFAULT '', deletion_result TEXT NOT NULL DEFAULT '', artifact_ref TEXT NOT NULL DEFAULT '', blocked_reason TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL)`,
	`CREATE TABLE leases (name TEXT PRIMARY KEY, scope TEXT NOT NULL, owner TEXT NOT NULL, acquired_at TEXT NOT NULL, expires_at TEXT NOT NULL, renewed_at TEXT NOT NULL DEFAULT '', released_at TEXT NOT NULL DEFAULT '', release_reason TEXT NOT NULL DEFAULT '')`,
	`CREATE TABLE daemon_heartbeats (id INTEGER PRIMARY KEY, process_id TEXT NOT NULL, lane_name TEXT NOT NULL, workflow_path TEXT NOT NULL, cycle_number INTEGER NOT NULL, last_success_at TEXT NOT NULL DEFAULT '', last_error TEXT NOT NULL DEFAULT '', recovery_required INTEGER NOT NULL DEFAULT 0 CHECK (recovery_required IN (0, 1)), updated_at TEXT NOT NULL)`,
	`CREATE TABLE retry_decisions (id INTEGER PRIMARY KEY, attempt_id INTEGER NOT NULL REFERENCES issue_attempts(id) ON DELETE CASCADE, retry_count INTEGER NOT NULL, budget_state TEXT NOT NULL, reason TEXT NOT NULL, input_hash TEXT NOT NULL DEFAULT '', next_state TEXT NOT NULL, decided_at TEXT NOT NULL)`,
	`CREATE TABLE terminal_outcomes (attempt_id INTEGER PRIMARY KEY REFERENCES issue_attempts(id) ON DELETE CASCADE, outcome TEXT NOT NULL, reason TEXT NOT NULL DEFAULT '', cleaned INTEGER NOT NULL DEFAULT 0 CHECK (cleaned IN (0, 1)), recorded_at TEXT NOT NULL)`,
	`CREATE TABLE external_fact_snapshots (id INTEGER PRIMARY KEY, attempt_id INTEGER REFERENCES issue_attempts(id) ON DELETE CASCADE, source TEXT NOT NULL, fact_key TEXT NOT NULL, fact_json TEXT NOT NULL, fact_hash TEXT NOT NULL, captured_at TEXT NOT NULL, UNIQUE(source, fact_key, fact_hash))`,
	`CREATE INDEX idx_issue_attempts_status ON issue_attempts(status)`,
	`CREATE INDEX idx_pr_mappings_pr_number ON pr_mappings(repository, pr_number)`,
	`CREATE INDEX idx_merge_blockers_active ON merge_blockers(active, code)`,
	`CREATE INDEX idx_leases_expires_at ON leases(expires_at)`,
	`CREATE INDEX idx_daemon_heartbeats_lane ON daemon_heartbeats(lane_name, updated_at)`,
}
