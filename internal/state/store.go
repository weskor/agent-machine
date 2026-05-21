package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	CurrentSchemaVersion = 2
	busyTimeoutMS        = 5000
)

var ErrUnsupportedSchema = errors.New("unsupported sqlite orchestration schema")
var ErrLeaseHeld = errors.New("lease is already held")

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
	Events           int
}

const (
	EventCandidateSelected = "candidate_selected"
	EventCandidateSkipped  = "candidate_skipped"
	EventAttemptStarted    = "attempt_started"
	EventAttemptFinished   = "attempt_finished"
	EventPRDetected        = "pr_detected"
	EventReviewCompleted   = "review_completed"
	EventMergeBlocked      = "merge_blocked"
	EventMergeCompleted    = "merge_completed"
	EventCleanupStarted    = "cleanup_started"
	EventCleanupCompleted  = "cleanup_completed"
	EventErrorRecorded     = "error_recorded"
)

type Event struct {
	ID         string
	Sequence   int64
	OccurredAt time.Time
	IssueKey   string
	IssueID    string
	Attempt    int
	RunID      string
	Source     string
	Type       string
	Payload    json.RawMessage
}

type EventInput struct {
	OccurredAt time.Time
	IssueKey   string
	IssueID    string
	Attempt    int
	RunID      string
	Source     string
	Type       string
	Payload    any
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

type CleanupFacts struct {
	IssueKey        string
	Attempt         int
	WorkspacePath   string
	BranchName      string
	BaseBranch      string
	Status          string
	TerminalOutcome string
	PRURL           string
	CleanupDecision string
	DeletionResult  string
	ArtifactRef     string
	UpdatedAt       time.Time
}

type ReconciliationFacts struct {
	IssueKey           string
	Attempt            int
	WorkspacePath      string
	BranchName         string
	Status             string
	PRURL              string
	RetryNextState     string
	RetryReason        string
	TerminalOutcome    string
	TerminalReason     string
	CleanupDecision    string
	CleanupResult      string
	CleanupArtifactRef string
	UpdatedAt          time.Time
}

type RunArtifactSnapshot struct {
	SchemaVersion         int
	ArtifactSchemaVersion int
	ArtifactSchemaSource  string
	IssueKey              string
	IssueID               string
	Attempt               int
	WorkspacePath         string
	BranchName            string
	BaseBranch            string
	Status                string
	StartedAt             time.Time
	UpdatedAt             time.Time
	Repository            string
	PRNumber              int
	PRURL                 string
	ReviewStatus          string
	ReviewPassed          bool
	ReviewClassification  string
	ReviewOutputRef       string
	ReviewOutputHash      string
	MergeEligible         bool
	FeedbackHash          string
	FeedbackNextAction    string
	RetryCount            int
	RetryBudgetState      string
	RetryReason           string
	RetryInputHash        string
	RetryNextState        string
	TerminalOutcome       string
	TerminalReason        string
	RunArtifactRef        string
	EvaluationRef         string
}

type SnapshotAttempt struct {
	IssueKey         string
	Attempt          int
	WorkspacePath    string
	BranchName       string
	Status           string
	ReviewStatus     string
	ReviewPassed     bool
	PRURL            string
	TerminalOutcome  string
	RetryBudgetState string
	RetryNextState   string
	UpdatedAt        time.Time
}

type SnapshotHeartbeat struct {
	ProcessID        string
	LaneName         string
	WorkflowPath     string
	CycleNumber      int
	LastSuccessAt    time.Time
	LastError        string
	RecoveryRequired bool
	UpdatedAt        time.Time
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
	if err := normalizeLease(&lease, time.Now().UTC()); err != nil {
		return fmt.Errorf("upsert lease: %w", err)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO leases(name, scope, owner, acquired_at, expires_at, renewed_at, released_at, release_reason) VALUES (?, ?, ?, ?, ?, ?, '', '') ON CONFLICT(name) DO UPDATE SET scope = excluded.scope, owner = excluded.owner, acquired_at = excluded.acquired_at, expires_at = excluded.expires_at, renewed_at = excluded.renewed_at, released_at = '', release_reason = ''`, lease.Name, lease.Scope, lease.Owner, formatTime(lease.AcquiredAt), formatTime(lease.ExpiresAt), formatTime(lease.RenewedAt))
	if err != nil {
		return fmt.Errorf("upsert lease: %w", err)
	}
	return nil
}

func (s *Store) AcquireLease(ctx context.Context, lease Lease, now time.Time) (bool, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if err := normalizeLease(&lease, now); err != nil {
		return false, fmt.Errorf("acquire lease: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin acquire lease: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO leases(name, scope, owner, acquired_at, expires_at, renewed_at, released_at, release_reason) VALUES (?, ?, ?, ?, ?, ?, '', '')`, lease.Name, lease.Scope, lease.Owner, formatTime(lease.AcquiredAt), formatTime(lease.ExpiresAt), formatTime(lease.RenewedAt))
	if err != nil {
		return false, fmt.Errorf("insert lease: %w", err)
	}
	if rows, _ := result.RowsAffected(); rows == 1 {
		return true, tx.Commit()
	}

	var oldOwner, expiresRaw, releasedRaw string
	if err := tx.QueryRowContext(ctx, `SELECT owner, expires_at, released_at FROM leases WHERE name = ?`, lease.Name).Scan(&oldOwner, &expiresRaw, &releasedRaw); err != nil {
		return false, fmt.Errorf("read existing lease: %w", err)
	}
	expiresAt, err := parseTime(expiresRaw)
	if err != nil {
		return false, fmt.Errorf("parse existing lease expiry: %w", err)
	}
	if releasedRaw == "" && expiresAt.After(now.UTC()) {
		return false, nil
	}
	reason := "released"
	if releasedRaw == "" {
		reason = "stale"
	}
	result, err = tx.ExecContext(ctx, `UPDATE leases SET scope = ?, owner = ?, acquired_at = ?, expires_at = ?, renewed_at = ?, released_at = '', release_reason = '' WHERE name = ? AND (released_at <> '' OR julianday(expires_at) <= julianday(?))`, lease.Scope, lease.Owner, formatTime(lease.AcquiredAt), formatTime(lease.ExpiresAt), formatTime(lease.RenewedAt), lease.Name, formatTime(now))
	if err != nil {
		return false, fmt.Errorf("reclaim lease: %w", err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return false, nil
	}
	if err := recordLeaseReclaim(ctx, tx, lease.Name, oldOwner, lease.Owner, reason, now); err != nil {
		return false, err
	}
	return true, tx.Commit()
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
	result, err := s.db.ExecContext(ctx, `UPDATE leases SET expires_at = ?, renewed_at = ? WHERE name = ? AND released_at = ''`, formatTime(expiresAt), formatTime(renewedAt), name)
	if err != nil {
		return fmt.Errorf("renew lease: %w", err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return fmt.Errorf("renew lease: %w", ErrLeaseHeld)
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
	result, err := s.db.ExecContext(ctx, `UPDATE leases SET released_at = ?, release_reason = ? WHERE name = ?`, formatTime(releasedAt), reason, name)
	if err != nil {
		return fmt.Errorf("release lease: %w", err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return fmt.Errorf("release lease: %w", ErrLeaseHeld)
	}
	return nil
}

func (s *Store) Lease(ctx context.Context, name string) (Lease, bool, error) {
	if name == "" {
		return Lease{}, false, errors.New("lease: name is required")
	}
	var lease Lease
	var acquiredRaw, expiresRaw, renewedRaw, releasedRaw string
	err := s.db.QueryRowContext(ctx, `SELECT name, scope, owner, acquired_at, expires_at, renewed_at, released_at, release_reason FROM leases WHERE name = ?`, name).Scan(&lease.Name, &lease.Scope, &lease.Owner, &acquiredRaw, &expiresRaw, &renewedRaw, &releasedRaw, &lease.ReleaseReason)
	if errors.Is(err, sql.ErrNoRows) {
		return Lease{}, false, nil
	}
	if err != nil {
		return Lease{}, false, fmt.Errorf("read lease: %w", err)
	}
	if lease.AcquiredAt, err = parseTime(acquiredRaw); err != nil {
		return Lease{}, false, fmt.Errorf("parse lease acquired_at: %w", err)
	}
	if lease.ExpiresAt, err = parseTime(expiresRaw); err != nil {
		return Lease{}, false, fmt.Errorf("parse lease expires_at: %w", err)
	}
	if lease.RenewedAt, err = parseTime(renewedRaw); err != nil {
		return Lease{}, false, fmt.Errorf("parse lease renewed_at: %w", err)
	}
	if lease.ReleasedAt, err = parseTime(releasedRaw); err != nil {
		return Lease{}, false, fmt.Errorf("parse lease released_at: %w", err)
	}
	return lease, true, nil
}

func (s *Store) ReclaimLease(ctx context.Context, lease Lease, now time.Time, reason string) (bool, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if reason == "" {
		reason = "reclaimed"
	}
	if err := normalizeLease(&lease, now); err != nil {
		return false, fmt.Errorf("reclaim lease: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin reclaim lease: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var oldOwner string
	if err := tx.QueryRowContext(ctx, `SELECT owner FROM leases WHERE name = ? AND released_at = ''`, lease.Name).Scan(&oldOwner); errors.Is(err, sql.ErrNoRows) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("read reclaim lease owner: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE leases SET scope = ?, owner = ?, acquired_at = ?, expires_at = ?, renewed_at = ?, released_at = '', release_reason = '' WHERE name = ? AND owner = ? AND released_at = ''`, lease.Scope, lease.Owner, formatTime(lease.AcquiredAt), formatTime(lease.ExpiresAt), formatTime(lease.RenewedAt), lease.Name, oldOwner)
	if err != nil {
		return false, fmt.Errorf("reclaim lease: %w", err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return false, nil
	}
	if err := recordLeaseReclaim(ctx, tx, lease.Name, oldOwner, lease.Owner, reason, now); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func normalizeLease(lease *Lease, now time.Time) error {
	if lease.Name == "" {
		return errors.New("name is required")
	}
	if lease.Scope == "" {
		return errors.New("scope is required")
	}
	if lease.Owner == "" {
		return errors.New("owner is required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if lease.AcquiredAt.IsZero() {
		lease.AcquiredAt = now.UTC()
	}
	if lease.RenewedAt.IsZero() {
		lease.RenewedAt = lease.AcquiredAt
	}
	if lease.ExpiresAt.IsZero() {
		lease.ExpiresAt = lease.RenewedAt
	}
	return nil
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}

func recordLeaseReclaim(ctx context.Context, tx *sql.Tx, name, oldOwner, newOwner, reason string, capturedAt time.Time) error {
	payload, err := json.Marshal(map[string]string{"lease": name, "old_owner": oldOwner, "new_owner": newOwner, "reason": reason})
	if err != nil {
		return fmt.Errorf("encode lease reclaim: %w", err)
	}
	factHash := fmt.Sprintf("%s:%s:%s:%s:%s", name, oldOwner, newOwner, reason, formatTime(capturedAt))
	_, err = tx.ExecContext(ctx, `INSERT INTO external_fact_snapshots(attempt_id, source, fact_key, fact_json, fact_hash, captured_at) VALUES (NULL, 'lease', ?, ?, ?, ?)`, "lease_reclaim:"+name, string(payload), factHash, formatTime(capturedAt))
	if err != nil {
		return fmt.Errorf("record lease reclaim: %w", err)
	}
	return nil
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin cleanup state upsert: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var attemptID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM issue_attempts WHERE issue_key = ? AND attempt = ?`, cleanup.IssueKey, cleanup.Attempt).Scan(&attemptID); err != nil {
		return fmt.Errorf("read cleanup attempt id: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO cleanup_states(attempt_id, workspace_exists, eligible, decision, deletion_result, artifact_ref, blocked_reason, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(attempt_id) DO UPDATE SET workspace_exists=excluded.workspace_exists, eligible=excluded.eligible, decision=excluded.decision, deletion_result=excluded.deletion_result, artifact_ref=excluded.artifact_ref, blocked_reason=excluded.blocked_reason, updated_at=excluded.updated_at`, attemptID, boolInt(cleanup.WorkspaceExists), boolInt(cleanup.Eligible), cleanup.Decision, cleanup.DeletionResult, cleanup.ArtifactRef, cleanup.BlockedReason, now.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("upsert cleanup state: %w", err)
	}
	if _, err := appendEvent(ctx, tx, EventInput{OccurredAt: now, IssueKey: cleanup.IssueKey, Attempt: cleanup.Attempt, Source: "runner.cleanup", Type: EventCleanupCompleted, Payload: map[string]any{"decision": cleanup.Decision, "deletion_result": cleanup.DeletionResult, "eligible": cleanup.Eligible}}); err != nil {
		return fmt.Errorf("append cleanup event: %w", err)
	}
	return tx.Commit()
}

func (s *Store) CleanupFacts(ctx context.Context, issueKey string) (CleanupFacts, bool, error) {
	if issueKey == "" {
		return CleanupFacts{}, false, errors.New("cleanup facts: issue key is required")
	}
	var facts CleanupFacts
	var updatedRaw string
	err := s.db.QueryRowContext(ctx, `SELECT a.issue_key, a.attempt, a.workspace_path, a.branch_name, a.base_branch, a.status, COALESCE(t.outcome, ''), COALESCE(p.pr_url, ''), COALESCE(c.decision, ''), COALESCE(c.deletion_result, ''), COALESCE(c.artifact_ref, ''), a.updated_at
FROM issue_attempts a
LEFT JOIN terminal_outcomes t ON t.attempt_id = a.id
LEFT JOIN pr_mappings p ON p.attempt_id = a.id
LEFT JOIN cleanup_states c ON c.attempt_id = a.id
WHERE a.issue_key = ?
ORDER BY a.attempt DESC LIMIT 1`, issueKey).Scan(&facts.IssueKey, &facts.Attempt, &facts.WorkspacePath, &facts.BranchName, &facts.BaseBranch, &facts.Status, &facts.TerminalOutcome, &facts.PRURL, &facts.CleanupDecision, &facts.DeletionResult, &facts.ArtifactRef, &updatedRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return CleanupFacts{}, false, nil
	}
	if err != nil {
		return CleanupFacts{}, false, fmt.Errorf("cleanup facts: %w", err)
	}
	if facts.UpdatedAt, err = parseTime(updatedRaw); err != nil {
		return CleanupFacts{}, false, fmt.Errorf("parse cleanup facts updated_at: %w", err)
	}
	return facts, true, nil
}

func (s *Store) ReconciliationFacts(ctx context.Context, issueKey string) (ReconciliationFacts, bool, error) {
	if issueKey == "" {
		return ReconciliationFacts{}, false, errors.New("reconciliation facts: issue key is required")
	}
	var facts ReconciliationFacts
	var updatedRaw string
	err := s.db.QueryRowContext(ctx, `SELECT a.issue_key, a.attempt, a.workspace_path, a.branch_name, a.status,
COALESCE(p.pr_url, ''), COALESCE(r.next_state, ''), COALESCE(r.reason, ''), COALESCE(t.outcome, ''), COALESCE(t.reason, ''),
COALESCE(c.decision, ''), COALESCE(c.deletion_result, ''), COALESCE(c.artifact_ref, ''), a.updated_at
FROM issue_attempts a
LEFT JOIN pr_mappings p ON p.attempt_id = a.id
LEFT JOIN retry_decisions r ON r.attempt_id = a.id
LEFT JOIN terminal_outcomes t ON t.attempt_id = a.id
LEFT JOIN cleanup_states c ON c.attempt_id = a.id
WHERE a.issue_key = ?
ORDER BY a.attempt DESC LIMIT 1`, issueKey).Scan(&facts.IssueKey, &facts.Attempt, &facts.WorkspacePath, &facts.BranchName, &facts.Status, &facts.PRURL, &facts.RetryNextState, &facts.RetryReason, &facts.TerminalOutcome, &facts.TerminalReason, &facts.CleanupDecision, &facts.CleanupResult, &facts.CleanupArtifactRef, &updatedRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return ReconciliationFacts{}, false, nil
	}
	if err != nil {
		return ReconciliationFacts{}, false, fmt.Errorf("reconciliation facts: %w", err)
	}
	if facts.UpdatedAt, err = parseTime(updatedRaw); err != nil {
		return ReconciliationFacts{}, false, fmt.Errorf("parse reconciliation facts updated_at: %w", err)
	}
	return facts, true, nil
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
	if snap.SchemaVersion == 0 {
		snap.SchemaVersion = CurrentSchemaVersion
	}
	if snap.ArtifactSchemaVersion == 0 {
		snap.ArtifactSchemaVersion = 1
	}
	if snap.ArtifactSchemaSource == "" {
		snap.ArtifactSchemaSource = "current"
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
		factJSON, err := json.Marshal(map[string]any{"ref": ref, "schema_version": snap.ArtifactSchemaVersion, "schema_source": snap.ArtifactSchemaSource, "projection_schema_version": snap.SchemaVersion})
		if err != nil {
			return fmt.Errorf("encode external artifact ref: %w", err)
		}
		_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO external_fact_snapshots(attempt_id, source, fact_key, fact_json, fact_hash, captured_at) VALUES (?, 'artifact', ?, ?, ?, ?)`, attemptID, key, string(factJSON), ref, now.Format(time.RFC3339Nano))
		if err != nil {
			return fmt.Errorf("record external artifact ref: %w", err)
		}
	}
	if _, err := appendEvent(ctx, tx, runArtifactEventInput(snap, now)); err != nil {
		return fmt.Errorf("append run artifact event: %w", err)
	}
	return tx.Commit()
}

func (s *Store) SnapshotAttempts(ctx context.Context) ([]SnapshotAttempt, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ia.issue_key, ia.attempt, ia.workspace_path, ia.branch_name, ia.status, ia.updated_at,
COALESCE(rs.command_status, ''), COALESCE(rs.passed, 0), COALESCE(pm.pr_url, ''), COALESCE(t.term_outcome, ''), COALESCE(rd.budget_state, ''), COALESCE(rd.next_state, '')
FROM issue_attempts ia
LEFT JOIN review_states rs ON rs.attempt_id = ia.id
LEFT JOIN pr_mappings pm ON pm.attempt_id = ia.id
LEFT JOIN (SELECT attempt_id, outcome AS term_outcome FROM terminal_outcomes) t ON t.attempt_id = ia.id
LEFT JOIN retry_decisions rd ON rd.attempt_id = ia.id
ORDER BY ia.issue_key, ia.attempt`)
	if err != nil {
		return nil, fmt.Errorf("query snapshot attempts: %w", err)
	}
	defer rows.Close()
	var attempts []SnapshotAttempt
	for rows.Next() {
		var attempt SnapshotAttempt
		var updated string
		var passed int
		if err := rows.Scan(&attempt.IssueKey, &attempt.Attempt, &attempt.WorkspacePath, &attempt.BranchName, &attempt.Status, &updated, &attempt.ReviewStatus, &passed, &attempt.PRURL, &attempt.TerminalOutcome, &attempt.RetryBudgetState, &attempt.RetryNextState); err != nil {
			return nil, fmt.Errorf("scan snapshot attempt: %w", err)
		}
		attempt.ReviewPassed = passed == 1
		attempt.UpdatedAt, _ = parseTime(updated)
		attempts = append(attempts, attempt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate snapshot attempts: %w", err)
	}
	return attempts, nil
}

func (s *Store) SnapshotHeartbeats(ctx context.Context) ([]SnapshotHeartbeat, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT process_id, lane_name, workflow_path, cycle_number, last_success_at, last_error, recovery_required, updated_at FROM daemon_heartbeats ORDER BY lane_name, updated_at`)
	if err != nil {
		return nil, fmt.Errorf("query snapshot heartbeats: %w", err)
	}
	defer rows.Close()
	var heartbeats []SnapshotHeartbeat
	for rows.Next() {
		var heartbeat SnapshotHeartbeat
		var lastSuccessAt, updatedAt string
		var recoveryRequired int
		if err := rows.Scan(&heartbeat.ProcessID, &heartbeat.LaneName, &heartbeat.WorkflowPath, &heartbeat.CycleNumber, &lastSuccessAt, &heartbeat.LastError, &recoveryRequired, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan snapshot heartbeat: %w", err)
		}
		heartbeat.LastSuccessAt, _ = parseTime(lastSuccessAt)
		heartbeat.UpdatedAt, _ = parseTime(updatedAt)
		heartbeat.RecoveryRequired = recoveryRequired == 1
		heartbeats = append(heartbeats, heartbeat)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate snapshot heartbeats: %w", err)
	}
	return heartbeats, nil
}

func runArtifactEventInput(snap RunArtifactSnapshot, occurredAt time.Time) EventInput {
	eventType := EventAttemptStarted
	if snap.TerminalOutcome != "" {
		eventType = EventAttemptFinished
	}
	payload := map[string]any{"status": snap.Status}
	payload["schema_version"] = snap.SchemaVersion
	payload["artifact_schema_version"] = snap.ArtifactSchemaVersion
	payload["artifact_schema_source"] = snap.ArtifactSchemaSource
	if snap.PRURL != "" {
		payload["pr_url"] = snap.PRURL
	}
	if snap.ReviewStatus != "" {
		payload["review_status"] = snap.ReviewStatus
	}
	if snap.ReviewClassification != "" {
		payload["review_classification"] = snap.ReviewClassification
	}
	if snap.TerminalOutcome != "" {
		payload["terminal_outcome"] = snap.TerminalOutcome
	}
	if snap.TerminalReason != "" {
		payload["terminal_reason"] = snap.TerminalReason
	}
	if snap.FeedbackNextAction != "" {
		payload["next_action"] = snap.FeedbackNextAction
	}
	return EventInput{
		OccurredAt: occurredAt,
		IssueKey:   snap.IssueKey,
		IssueID:    snap.IssueID,
		Attempt:    snap.Attempt,
		RunID:      snap.WorkspacePath,
		Source:     "runner.run_attempt",
		Type:       eventType,
		Payload:    payload,
	}
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

func (s *Store) AppendEvent(ctx context.Context, input EventInput) (Event, error) {
	if s == nil || s.db == nil {
		return Event{}, errors.New("append event: store is nil")
	}
	return appendEvent(ctx, s.db, input)
}

type eventExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func appendEvent(ctx context.Context, execer eventExecer, input EventInput) (Event, error) {
	payload, err := normalizeEventPayload(input.Payload)
	if err != nil {
		return Event{}, fmt.Errorf("append event: %w", err)
	}
	event, err := normalizeEvent(input, payload)
	if err != nil {
		return Event{}, fmt.Errorf("append event: %w", err)
	}
	_, err = execer.ExecContext(ctx, `INSERT OR IGNORE INTO orchestration_events(event_id, occurred_at, issue_key, issue_id, attempt, run_id, source, event_type, payload_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, event.ID, formatTime(event.OccurredAt), event.IssueKey, event.IssueID, nullZeroInt(event.Attempt), event.RunID, event.Source, event.Type, string(event.Payload))
	if err != nil {
		return Event{}, fmt.Errorf("append event: %w", err)
	}
	return event, nil
}

func (s *Store) RecentEvents(ctx context.Context, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.db.QueryContext(ctx, `SELECT sequence, event_id, occurred_at, issue_key, issue_id, COALESCE(attempt, 0), run_id, source, event_type, payload_json FROM orchestration_events ORDER BY sequence DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("recent events: %w", err)
	}
	defer rows.Close()
	var events []Event
	for rows.Next() {
		var event Event
		var occurredRaw, payload string
		if err := rows.Scan(&event.Sequence, &event.ID, &occurredRaw, &event.IssueKey, &event.IssueID, &event.Attempt, &event.RunID, &event.Source, &event.Type, &payload); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		event.OccurredAt, err = parseTime(occurredRaw)
		if err != nil {
			return nil, fmt.Errorf("parse event occurred_at: %w", err)
		}
		event.Payload = json.RawMessage(payload)
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("recent events: %w", err)
	}
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}
	return events, nil
}

func normalizeEventPayload(payload any) ([]byte, error) {
	if payload == nil {
		return []byte(`{}`), nil
	}
	if raw, ok := payload.(json.RawMessage); ok {
		if !json.Valid(raw) {
			return nil, errors.New("payload must be valid JSON")
		}
		return raw, nil
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode payload: %w", err)
	}
	if !json.Valid(encoded) {
		return nil, errors.New("payload must be valid JSON")
	}
	return encoded, nil
}

func normalizeEvent(input EventInput, payload []byte) (Event, error) {
	if input.Source == "" {
		return Event{}, errors.New("source is required")
	}
	if input.Type == "" {
		return Event{}, errors.New("type is required")
	}
	occurredAt := input.OccurredAt.UTC()
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
	}
	if input.Attempt < 0 {
		return Event{}, errors.New("attempt cannot be negative")
	}
	idInput := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%d\x00%s\x00%s", formatTime(occurredAt), input.Source, input.Type, input.IssueKey, input.Attempt, input.RunID, string(payload))
	sum := sha256.Sum256([]byte(idInput))
	return Event{ID: fmt.Sprintf("evt_%x", sum[:16]), OccurredAt: occurredAt, IssueKey: input.IssueKey, IssueID: input.IssueID, Attempt: input.Attempt, RunID: input.RunID, Source: input.Source, Type: input.Type, Payload: append([]byte(nil), payload...)}, nil
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
		"issue_attempts":       &counts.IssueAttempts,
		"pr_mappings":          &counts.PRMappings,
		"review_states":        &counts.ReviewStates,
		"terminal_outcomes":    &counts.TerminalOutcomes,
		"daemon_heartbeats":    &counts.DaemonHeartbeats,
		"cleanup_states":       &counts.CleanupStates,
		"orchestration_events": &counts.Events,
	} {
		if err := s.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s`, table)).Scan(dest); err != nil {
			if table == "orchestration_events" && strings.Contains(err.Error(), "no such table") {
				*dest = 0
				continue
			}
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
		version = 1
	}
	if version < 2 {
		if err := migrateV2(ctx, tx); err != nil {
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
	_, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, name, checksum, applied_at, success) VALUES (?, ?, ?, ?, 1)`, 1, "initial orchestration state", "v1", time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("record migration v1: %w", err)
	}
	return nil
}

func migrateV2(ctx context.Context, tx *sql.Tx) error {
	for _, stmt := range v2Schema {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("apply migration v2: %w", err)
		}
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, name, checksum, applied_at, success) VALUES (?, ?, ?, ?, 1)`, 2, "durable orchestration event log", "v2", time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("record migration v2: %w", err)
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

var v2Schema = []string{
	`CREATE TABLE orchestration_events (sequence INTEGER PRIMARY KEY AUTOINCREMENT, event_id TEXT NOT NULL UNIQUE, occurred_at TEXT NOT NULL, issue_key TEXT NOT NULL DEFAULT '', issue_id TEXT NOT NULL DEFAULT '', attempt INTEGER, run_id TEXT NOT NULL DEFAULT '', source TEXT NOT NULL, event_type TEXT NOT NULL, payload_json TEXT NOT NULL CHECK (json_valid(payload_json)))`,
	`CREATE INDEX idx_orchestration_events_issue ON orchestration_events(issue_key, attempt, sequence)`,
	`CREATE INDEX idx_orchestration_events_type ON orchestration_events(event_type, sequence)`,
}
