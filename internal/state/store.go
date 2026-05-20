package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
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
	Exists        bool
	SchemaVersion int
	JournalMode   string
	BusyTimeoutMS int
	Counts        Counts
}

type Counts struct {
	IssueAttempts    int
	PRMappings       int
	ReviewStates     int
	TerminalOutcomes int
	DaemonHeartbeats int
	CleanupStates    int
}

type DaemonHeartbeat struct {
	ProcessID        string
	LaneName         string
	WorkflowPath     string
	CycleNumber      int
	LastSuccessAt    time.Time
	LastError        string
	RecoveryRequired bool
	UpdatedAt        time.Time
}

type Lease struct {
	Name          string
	Scope         string
	Owner         string
	AcquiredAt    time.Time
	ExpiresAt     time.Time
	RenewedAt     time.Time
	ReleasedAt    time.Time
	ReleaseReason string
}

type CleanupState struct {
	IssueKey        string
	Attempt         int
	WorkspaceExists bool
	Eligible        bool
	Decision        string
	DeletionResult  string
	ArtifactRef     string
	BlockedReason   string
	UpdatedAt       time.Time
}

type RunArtifactSnapshot struct {
	IssueKey             string
	IssueID              string
	Attempt              int
	WorkspacePath        string
	BranchName           string
	BaseBranch           string
	Status               string
	StartedAt            time.Time
	UpdatedAt            time.Time
	Repository           string
	PRNumber             int
	PRURL                string
	ReviewStatus         string
	ReviewPassed         bool
	ReviewClassification string
	ReviewOutputRef      string
	ReviewOutputHash     string
	MergeEligible        bool
	FeedbackHash         string
	FeedbackNextAction   string
	RetryCount           int
	RetryBudgetState     string
	RetryReason          string
	RetryInputHash       string
	RetryNextState       string
	TerminalOutcome      string
	TerminalReason       string
	RunArtifactRef       string
	EvaluationRef        string
}

func DefaultDBPath(workspaceRoot string) string {
	if workspaceRoot == "" {
		return ""
	}
	clean := filepath.Clean(workspaceRoot)
	if filepath.Base(clean) == "workspaces" && filepath.Base(filepath.Dir(clean)) == ".symphony" {
		return filepath.Join(filepath.Dir(clean), "state", "pi-symphony.db")
	}
	return filepath.Join(clean, "state", "pi-symphony.db")
}

func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("open state store: path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create state store directory: %w", err)
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

func (s *Store) UpsertLease(ctx context.Context, lease Lease) error {
	if lease.Name == "" {
		return errors.New("upsert lease: name is required")
	}
	if lease.Scope == "" {
		return errors.New("upsert lease: scope is required")
	}
	if lease.Owner == "" {
		return errors.New("upsert lease: owner is required")
	}
	if lease.AcquiredAt.IsZero() {
		lease.AcquiredAt = time.Now().UTC()
	}
	if lease.RenewedAt.IsZero() {
		lease.RenewedAt = lease.AcquiredAt
	}
	if lease.ExpiresAt.IsZero() {
		lease.ExpiresAt = lease.RenewedAt
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO leases(name, scope, owner, acquired_at, expires_at, renewed_at, released_at, release_reason) VALUES (?, ?, ?, ?, ?, ?, '', '') ON CONFLICT(name) DO UPDATE SET scope = excluded.scope, owner = excluded.owner, acquired_at = excluded.acquired_at, expires_at = excluded.expires_at, renewed_at = excluded.renewed_at, released_at = '', release_reason = ''`, lease.Name, lease.Scope, lease.Owner, formatTime(lease.AcquiredAt), formatTime(lease.ExpiresAt), formatTime(lease.RenewedAt))
	if err != nil {
		return fmt.Errorf("upsert lease: %w", err)
	}
	return nil
}

func (s *Store) RenewLease(ctx context.Context, name string, renewedAt, expiresAt time.Time) error {
	if name == "" {
		return errors.New("renew lease: name is required")
	}
	if renewedAt.IsZero() {
		renewedAt = time.Now().UTC()
	}
	if expiresAt.IsZero() {
		expiresAt = renewedAt
	}
	_, err := s.db.ExecContext(ctx, `UPDATE leases SET expires_at = ?, renewed_at = ? WHERE name = ?`, formatTime(expiresAt), formatTime(renewedAt), name)
	if err != nil {
		return fmt.Errorf("renew lease: %w", err)
	}
	return nil
}

func (s *Store) ReleaseLease(ctx context.Context, name string, releasedAt time.Time, reason string) error {
	if name == "" {
		return errors.New("release lease: name is required")
	}
	if releasedAt.IsZero() {
		releasedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `UPDATE leases SET released_at = ?, release_reason = ? WHERE name = ?`, formatTime(releasedAt), reason, name)
	if err != nil {
		return fmt.Errorf("release lease: %w", err)
	}
	return nil
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func (s *Store) UpsertDaemonHeartbeat(ctx context.Context, heartbeat DaemonHeartbeat) error {
	if heartbeat.ProcessID == "" {
		return errors.New("upsert daemon heartbeat: process_id is required")
	}
	if heartbeat.LaneName == "" {
		return errors.New("upsert daemon heartbeat: lane_name is required")
	}
	if heartbeat.UpdatedAt.IsZero() {
		heartbeat.UpdatedAt = time.Now().UTC()
	}
	lastSuccessAt := ""
	if !heartbeat.LastSuccessAt.IsZero() {
		lastSuccessAt = heartbeat.LastSuccessAt.UTC().Format(time.RFC3339Nano)
	}
	now := heartbeat.UpdatedAt.UTC().Format(time.RFC3339Nano)
	recoveryRequired := 0
	if heartbeat.RecoveryRequired {
		recoveryRequired = 1
	}
	res, err := s.db.ExecContext(ctx, `UPDATE daemon_heartbeats SET workflow_path = ?, cycle_number = ?, last_success_at = COALESCE(NULLIF(?, ''), last_success_at), last_error = ?, recovery_required = ?, updated_at = ? WHERE process_id = ? AND lane_name = ?`, heartbeat.WorkflowPath, heartbeat.CycleNumber, lastSuccessAt, heartbeat.LastError, recoveryRequired, now, heartbeat.ProcessID, heartbeat.LaneName)
	if err != nil {
		return fmt.Errorf("update daemon heartbeat: %w", err)
	}
	if rows, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("update daemon heartbeat rows affected: %w", err)
	} else if rows > 0 {
		return nil
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO daemon_heartbeats(process_id, lane_name, workflow_path, cycle_number, last_success_at, last_error, recovery_required, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, heartbeat.ProcessID, heartbeat.LaneName, heartbeat.WorkflowPath, heartbeat.CycleNumber, lastSuccessAt, heartbeat.LastError, recoveryRequired, now)
	if err != nil {
		return fmt.Errorf("insert daemon heartbeat: %w", err)
	}
	return nil
}

func (s *Store) UpsertCleanupState(ctx context.Context, cleanup CleanupState) error {
	if cleanup.IssueKey == "" {
		return errors.New("upsert cleanup state: issue key is required")
	}
	if cleanup.Attempt <= 0 {
		cleanup.Attempt = 1
	}
	now := cleanup.UpdatedAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	var attemptID int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM issue_attempts WHERE issue_key = ? AND attempt = ?`, cleanup.IssueKey, cleanup.Attempt).Scan(&attemptID); err != nil {
		return fmt.Errorf("read cleanup attempt id: %w", err)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO cleanup_states(attempt_id, workspace_exists, eligible, decision, deletion_result, artifact_ref, blocked_reason, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(attempt_id) DO UPDATE SET workspace_exists=excluded.workspace_exists, eligible=excluded.eligible, decision=excluded.decision, deletion_result=excluded.deletion_result, artifact_ref=excluded.artifact_ref, blocked_reason=excluded.blocked_reason, updated_at=excluded.updated_at`, attemptID, boolInt(cleanup.WorkspaceExists), boolInt(cleanup.Eligible), cleanup.Decision, cleanup.DeletionResult, cleanup.ArtifactRef, cleanup.BlockedReason, now.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("upsert cleanup state: %w", err)
	}
	return nil
}

func (s *Store) UpsertRunArtifact(ctx context.Context, snap RunArtifactSnapshot) error {
	if snap.IssueKey == "" {
		return errors.New("upsert run artifact: issue key is required")
	}
	if snap.Attempt <= 0 {
		snap.Attempt = 1
	}
	if snap.BranchName == "" {
		snap.BranchName = snap.IssueKey
	}
	if snap.BaseBranch == "" {
		snap.BaseBranch = "main"
	}
	now := snap.UpdatedAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	created := snap.StartedAt.UTC()
	if created.IsZero() {
		created = now
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin run artifact mirror: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `INSERT INTO issue_attempts(issue_key, issue_id, attempt, workspace_path, branch_name, base_branch, status, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(issue_key, attempt) DO UPDATE SET issue_id=excluded.issue_id, workspace_path=excluded.workspace_path, branch_name=excluded.branch_name, base_branch=excluded.base_branch, status=excluded.status, updated_at=excluded.updated_at`, snap.IssueKey, snap.IssueID, snap.Attempt, snap.WorkspacePath, snap.BranchName, snap.BaseBranch, snap.Status, created.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("upsert issue attempt: %w", err)
	}
	attemptID, err := attemptID(ctx, tx, snap.IssueKey, snap.Attempt)
	if err != nil {
		return err
	}
	if snap.PRURL != "" || snap.Repository != "" {
		_, err = tx.ExecContext(ctx, `INSERT INTO pr_mappings(attempt_id, repository, branch_name, base_branch, pr_number, pr_url, symphony_owned, updated_at)
VALUES (?, ?, ?, ?, ?, ?, 1, ?)
ON CONFLICT(repository, branch_name) DO UPDATE SET attempt_id=excluded.attempt_id, base_branch=excluded.base_branch, pr_number=excluded.pr_number, pr_url=excluded.pr_url, symphony_owned=excluded.symphony_owned, updated_at=excluded.updated_at`, attemptID, snap.Repository, snap.BranchName, snap.BaseBranch, nullZeroInt(snap.PRNumber), snap.PRURL, now.Format(time.RFC3339Nano))
		if err != nil {
			return fmt.Errorf("upsert pr mapping: %w", err)
		}
	}
	if snap.ReviewStatus != "" || snap.ReviewClassification != "" {
		_, err = tx.ExecContext(ctx, `INSERT INTO review_states(attempt_id, command_status, passed, classification, output_ref, output_hash, merge_eligible, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(attempt_id) DO UPDATE SET command_status=excluded.command_status, passed=excluded.passed, classification=excluded.classification, output_ref=excluded.output_ref, output_hash=excluded.output_hash, merge_eligible=excluded.merge_eligible, updated_at=excluded.updated_at`, attemptID, snap.ReviewStatus, boolInt(snap.ReviewPassed), snap.ReviewClassification, snap.ReviewOutputRef, snap.ReviewOutputHash, boolInt(snap.MergeEligible), now.Format(time.RFC3339Nano))
		if err != nil {
			return fmt.Errorf("upsert review state: %w", err)
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO feedback_states(attempt_id, feedback_hash, next_action, updated_at) VALUES (?, ?, ?, ?)
ON CONFLICT(attempt_id) DO UPDATE SET feedback_hash=excluded.feedback_hash, next_action=excluded.next_action, updated_at=excluded.updated_at`, attemptID, snap.FeedbackHash, snap.FeedbackNextAction, now.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("upsert feedback state: %w", err)
	}
	if snap.RetryNextState != "" || snap.RetryReason != "" {
		if _, err = tx.ExecContext(ctx, `DELETE FROM retry_decisions WHERE attempt_id = ?`, attemptID); err != nil {
			return fmt.Errorf("replace retry decision: %w", err)
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO retry_decisions(attempt_id, retry_count, budget_state, reason, input_hash, next_state, decided_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, attemptID, snap.RetryCount, snap.RetryBudgetState, snap.RetryReason, snap.RetryInputHash, snap.RetryNextState, now.Format(time.RFC3339Nano))
		if err != nil {
			return fmt.Errorf("insert retry decision: %w", err)
		}
	}
	if snap.TerminalOutcome != "" {
		_, err = tx.ExecContext(ctx, `INSERT INTO terminal_outcomes(attempt_id, outcome, reason, recorded_at) VALUES (?, ?, ?, ?)
ON CONFLICT(attempt_id) DO UPDATE SET outcome=excluded.outcome, reason=excluded.reason, recorded_at=excluded.recorded_at`, attemptID, snap.TerminalOutcome, snap.TerminalReason, now.Format(time.RFC3339Nano))
		if err != nil {
			return fmt.Errorf("upsert terminal outcome: %w", err)
		}
	}
	for key, ref := range map[string]string{"run_record": snap.RunArtifactRef, "evaluation": snap.EvaluationRef} {
		if ref == "" {
			continue
		}
		factJSON, err := json.Marshal(map[string]string{"ref": ref})
		if err != nil {
			return fmt.Errorf("encode external artifact ref: %w", err)
		}
		_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO external_fact_snapshots(attempt_id, source, fact_key, fact_json, fact_hash, captured_at) VALUES (?, 'artifact', ?, ?, ?, ?)`, attemptID, key, string(factJSON), ref, now.Format(time.RFC3339Nano))
		if err != nil {
			return fmt.Errorf("record external artifact ref: %w", err)
		}
	}
	return tx.Commit()
}

func (s *Store) RecordArtifactExportFailure(ctx context.Context, issueKey string, attempt int, artifact string, message string, capturedAt time.Time) error {
	if issueKey == "" {
		return errors.New("record artifact export failure: issue key is required")
	}
	if attempt <= 0 {
		attempt = 1
	}
	if artifact == "" {
		artifact = "unknown"
	}
	if capturedAt.IsZero() {
		capturedAt = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin artifact export failure record: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	attemptID, err := attemptID(ctx, tx, issueKey, attempt)
	if err != nil {
		return err
	}
	factJSON, err := json.Marshal(map[string]string{"artifact": artifact, "error": message})
	if err != nil {
		return fmt.Errorf("encode artifact export failure: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO external_fact_snapshots(attempt_id, source, fact_key, fact_json, fact_hash, captured_at) VALUES (?, 'artifact', ?, ?, ?, ?)`, attemptID, "artifact_export_failure", string(factJSON), artifact+":"+message, capturedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("record artifact export failure: %w", err)
	}
	return tx.Commit()
}

func attemptID(ctx context.Context, tx *sql.Tx, issueKey string, attempt int) (int64, error) {
	var id int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM issue_attempts WHERE issue_key = ? AND attempt = ?`, issueKey, attempt).Scan(&id); err != nil {
		return 0, fmt.Errorf("read issue attempt id: %w", err)
	}
	return id, nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func nullZeroInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}

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
	counts, err := s.Counts(ctx)
	if err != nil {
		return Health{}, fmt.Errorf("state store health: counts: %w", err)
	}
	return Health{OK: true, Exists: true, SchemaVersion: version, JournalMode: journal, BusyTimeoutMS: timeout, Counts: counts}, nil
}

func InspectHealth(ctx context.Context, path string) (Health, error) {
	if path == "" {
		return Health{}, errors.New("inspect state store: path is required")
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return Health{Exists: false}, nil
		}
		return Health{}, fmt.Errorf("inspect state store: stat: %w", err)
	}
	dsn := (&url.URL{Scheme: "file", Path: path}).String() + "?mode=ro"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return Health{}, fmt.Errorf("inspect state store: open read-only: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `PRAGMA busy_timeout = 5000`); err != nil {
		return Health{}, fmt.Errorf("inspect state store: busy_timeout: %w", err)
	}
	s := &Store{db: db}
	return s.Health(ctx)
}

func (s *Store) Counts(ctx context.Context) (Counts, error) {
	var counts Counts
	for table, dest := range map[string]*int{
		"issue_attempts":    &counts.IssueAttempts,
		"pr_mappings":       &counts.PRMappings,
		"review_states":     &counts.ReviewStates,
		"terminal_outcomes": &counts.TerminalOutcomes,
		"daemon_heartbeats": &counts.DaemonHeartbeats,
		"cleanup_states":    &counts.CleanupStates,
	} {
		if err := s.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s`, table)).Scan(dest); err != nil {
			return Counts{}, fmt.Errorf("count %s: %w", table, err)
		}
	}
	return counts, nil
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
