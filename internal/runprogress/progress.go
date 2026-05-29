package runprogress

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/weskor/agent-machine/internal/artifacts"
	"github.com/weskor/agent-machine/internal/attemptoutcome"
	"github.com/weskor/agent-machine/internal/domain"
	"github.com/weskor/agent-machine/internal/runledger"
)

const (
	ArtifactName                = "progress.json"
	PRHandoffPendingPayloadName = "pr-handoff-pending.json"
	ReviewPendingPayloadName    = "review-pending.json"
	HandoffPendingPayloadName   = "handoff-pending.json"
	PhasePRHandoffPending       = "pr_handoff_pending"
	PhaseReviewPending          = "review_pending"
	PhaseHandoffPending         = "handoff_pending"
)

type Snapshot struct {
	IssueIdentifier      string    `json:"issue_identifier"`
	IssueTitle           string    `json:"issue_title,omitempty"`
	Phase                string    `json:"phase"`
	Status               string    `json:"status,omitempty"`
	Outcome              string    `json:"outcome,omitempty"`
	ChecksStatus         string    `json:"checks_status,omitempty"`
	ReviewStatus         string    `json:"review_status,omitempty"`
	ReviewClassification string    `json:"review_classification,omitempty"`
	PRURL                string    `json:"pr_url,omitempty"`
	Branch               string    `json:"branch,omitempty"`
	ExpectedBranch       string    `json:"expected_branch,omitempty"`
	Workspace            string    `json:"workspace,omitempty"`
	StartedAt            time.Time `json:"started_at"`
	UpdatedAt            time.Time `json:"updated_at"`
	DurationMS           int64     `json:"duration_ms"`
	NextAction           string    `json:"next_action,omitempty"`
	Error                string    `json:"error,omitempty"`
	RunRecordPath        string    `json:"run_record_path,omitempty"`
	EvaluationPath       string    `json:"evaluation_path,omitempty"`
	ProgressPath         string    `json:"progress_path,omitempty"`
	PRHandoffPayloadPath string    `json:"pr_handoff_payload_path,omitempty"`
	ReviewPayloadPath    string    `json:"review_payload_path,omitempty"`
	HandoffPayloadPath   string    `json:"handoff_payload_path,omitempty"`
}

func Root(workspaceRoot string) string {
	if strings.TrimSpace(workspaceRoot) == "" {
		return ""
	}
	clean := filepath.Clean(workspaceRoot)
	if filepath.Base(clean) == "workspaces" && filepath.Base(filepath.Dir(clean)) == ".am" {
		return filepath.Join(filepath.Dir(clean), "state", "run-progress")
	}
	return filepath.Join(clean, "state", "run-progress")
}

func Path(workspaceRoot, issueIdentifier string) (string, error) {
	root := Root(workspaceRoot)
	if root == "" {
		return "", errors.New("workspace root is required")
	}
	issue := strings.TrimSpace(issueIdentifier)
	if issue == "" || filepath.Base(filepath.Clean(issue)) != issue {
		return "", fmt.Errorf("issue identifier %q is not a safe run progress name", issueIdentifier)
	}
	return filepath.Join(root, issue, ArtifactName), nil
}

func HandoffPendingPayloadPath(workspaceRoot, issueIdentifier string) (string, error) {
	progressPath, err := Path(workspaceRoot, issueIdentifier)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(progressPath), HandoffPendingPayloadName), nil
}

func PRHandoffPendingPayloadPath(workspaceRoot, issueIdentifier string) (string, error) {
	progressPath, err := Path(workspaceRoot, issueIdentifier)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(progressPath), PRHandoffPendingPayloadName), nil
}

func ReviewPendingPayloadPath(workspaceRoot, issueIdentifier string) (string, error) {
	progressPath, err := Path(workspaceRoot, issueIdentifier)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(progressPath), ReviewPendingPayloadName), nil
}

func WriteResult(workspaceRoot string, snapshot Snapshot, logf func(string, ...any)) error {
	path, err := Path(workspaceRoot, snapshot.IssueIdentifier)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if snapshot.StartedAt.IsZero() {
		snapshot.StartedAt = now
	}
	if snapshot.UpdatedAt.IsZero() {
		snapshot.UpdatedAt = now
	}
	if snapshot.DurationMS == 0 && !snapshot.StartedAt.IsZero() {
		snapshot.DurationMS = snapshot.UpdatedAt.Sub(snapshot.StartedAt).Milliseconds()
	}
	snapshot.ProgressPath = path
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if err := AppendLedger(workspaceRoot, snapshot); err != nil && logf != nil {
		logf("failed to append run ledger for %s: %v", snapshot.IssueIdentifier, err)
	}
	return nil
}

func Read(workspaceRoot, issueIdentifier string) (Snapshot, error) {
	path, err := Path(workspaceRoot, issueIdentifier)
	if err != nil {
		return Snapshot{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Snapshot{}, fmt.Errorf("no run progress snapshot for %s at %s", issueIdentifier, path)
		}
		return Snapshot{}, err
	}
	var snapshot Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return Snapshot{}, err
	}
	if snapshot.ProgressPath == "" {
		snapshot.ProgressPath = path
	}
	if snapshot.PRHandoffPayloadPath == "" && snapshot.Phase == PhasePRHandoffPending {
		if payloadPath, err := PRHandoffPendingPayloadPath(workspaceRoot, issueIdentifier); err == nil {
			snapshot.PRHandoffPayloadPath = payloadPath
		}
	}
	if snapshot.ReviewPayloadPath == "" && snapshot.Phase == PhaseReviewPending {
		if payloadPath, err := ReviewPendingPayloadPath(workspaceRoot, issueIdentifier); err == nil {
			snapshot.ReviewPayloadPath = payloadPath
		}
	}
	if snapshot.HandoffPayloadPath == "" && snapshot.Phase == PhaseHandoffPending {
		if payloadPath, err := HandoffPendingPayloadPath(workspaceRoot, issueIdentifier); err == nil {
			snapshot.HandoffPayloadPath = payloadPath
		}
	}
	return snapshot, nil
}

func PrintProgress(workspaceRoot, issueIdentifier string) error {
	snapshot, err := Read(workspaceRoot, issueIdentifier)
	if err != nil {
		return err
	}
	fmt.Println(Format(snapshot))
	return nil
}

func PrintLedger(workspaceRoot, issueIdentifier string) error {
	events, path, err := runledger.Read(workspaceRoot, issueIdentifier)
	if err != nil {
		if !errors.Is(err, runledger.ErrNotFound) {
			return err
		}
		snapshot, progressErr := Read(workspaceRoot, issueIdentifier)
		if progressErr != nil {
			return err
		}
		path, _ = runledger.Path(workspaceRoot, issueIdentifier)
		events = []runledger.Event{LedgerEventFromSnapshot(snapshot)}
	}
	fmt.Println(runledger.Format(issueIdentifier, events, path))
	return nil
}

func AppendLedger(workspaceRoot string, snapshot Snapshot) error {
	return runledger.Append(workspaceRoot, LedgerEventFromSnapshot(snapshot))
}

func LedgerEventFromSnapshot(snapshot Snapshot) runledger.Event {
	return runledger.Event{
		IssueIdentifier:      snapshot.IssueIdentifier,
		IssueTitle:           snapshot.IssueTitle,
		Phase:                snapshot.Phase,
		Status:               snapshot.Status,
		Outcome:              snapshot.Outcome,
		ChecksStatus:         snapshot.ChecksStatus,
		ReviewStatus:         snapshot.ReviewStatus,
		ReviewClassification: snapshot.ReviewClassification,
		PRURL:                snapshot.PRURL,
		Branch:               snapshot.Branch,
		ExpectedBranch:       snapshot.ExpectedBranch,
		Workspace:            snapshot.Workspace,
		StartedAt:            snapshot.StartedAt,
		ObservedAt:           snapshot.UpdatedAt,
		DurationMS:           snapshot.DurationMS,
		NextAction:           snapshot.NextAction,
		Error:                snapshot.Error,
		RunRecordPath:        snapshot.RunRecordPath,
		EvaluationPath:       snapshot.EvaluationPath,
		ProgressPath:         snapshot.ProgressPath,
		PRHandoffPayloadPath: snapshot.PRHandoffPayloadPath,
		ReviewPayloadPath:    snapshot.ReviewPayloadPath,
		HandoffPayloadPath:   snapshot.HandoffPayloadPath,
	}
}

func Format(snapshot Snapshot) string {
	parts := []string{
		fmt.Sprintf("issue=%s", emptyAsUnknown(snapshot.IssueIdentifier)),
		fmt.Sprintf("phase=%s", emptyAsUnknown(snapshot.Phase)),
	}
	if snapshot.Status != "" {
		parts = append(parts, "status="+snapshot.Status)
	}
	if snapshot.Outcome != "" {
		parts = append(parts, "outcome="+snapshot.Outcome)
	}
	if snapshot.ChecksStatus != "" {
		parts = append(parts, "checks="+snapshot.ChecksStatus)
	}
	if snapshot.PRURL != "" {
		parts = append(parts, "pr="+snapshot.PRURL)
	}
	if snapshot.ReviewStatus != "" {
		parts = append(parts, "review="+snapshot.ReviewStatus)
	}
	if snapshot.ReviewClassification != "" {
		parts = append(parts, "classification="+snapshot.ReviewClassification)
	}
	if snapshot.Error != "" {
		parts = append(parts, "error="+snapshot.Error)
	}
	if snapshot.NextAction != "" {
		parts = append(parts, "next="+snapshot.NextAction)
	}
	if snapshot.DurationMS > 0 {
		parts = append(parts, fmt.Sprintf("duration_ms=%d", snapshot.DurationMS))
	}
	if snapshot.RunRecordPath != "" {
		parts = append(parts, "run_record="+snapshot.RunRecordPath)
	}
	if snapshot.EvaluationPath != "" {
		parts = append(parts, "evaluation="+snapshot.EvaluationPath)
	}
	if snapshot.PRHandoffPayloadPath != "" {
		parts = append(parts, "pr_handoff_payload="+snapshot.PRHandoffPayloadPath)
	}
	if snapshot.ReviewPayloadPath != "" {
		parts = append(parts, "review_payload="+snapshot.ReviewPayloadPath)
	}
	if snapshot.HandoffPayloadPath != "" {
		parts = append(parts, "handoff_payload="+snapshot.HandoffPayloadPath)
	}
	parts = append(parts, "progress="+snapshot.ProgressPath)
	return strings.Join(parts, " ")
}

func ForIssue(candidate domain.Issue, workspace, branch, phase string, startedAt time.Time) Snapshot {
	return Snapshot{
		IssueIdentifier: candidate.Identifier,
		IssueTitle:      candidate.Title,
		Phase:           phase,
		Branch:          branch,
		ExpectedBranch:  attemptoutcome.ExpectedWorkspaceBranch(candidate.Identifier),
		Workspace:       workspace,
		StartedAt:       startedAt,
		UpdatedAt:       time.Now().UTC(),
	}
}

func ForRecord(workspace string, record domain.RunRecord, evaluation artifacts.EvaluationArtifact) Snapshot {
	phase := record.Status
	switch record.Status {
	case attemptoutcome.StatusSuccess:
		phase = "completed"
	case attemptoutcome.StatusFailed, attemptoutcome.StatusTimeout, attemptoutcome.StatusBudgetExceeded, attemptoutcome.StatusGitHubAppError, attemptoutcome.StatusNeedsInfoFail:
		phase = "failed"
	}
	return Snapshot{
		IssueIdentifier:      record.IssueIdentifier,
		IssueTitle:           record.IssueTitle,
		Phase:                phase,
		Status:               record.Status,
		Outcome:              evaluation.Outcome,
		ChecksStatus:         evaluation.ChecksStatus,
		ReviewStatus:         record.ReviewStatus,
		ReviewClassification: record.ReviewClassification,
		PRURL:                record.PRURL,
		Branch:               record.Branch,
		ExpectedBranch:       record.ExpectedBranch,
		Workspace:            workspace,
		StartedAt:            record.StartedAt,
		UpdatedAt:            record.EndedAt,
		DurationMS:           record.DurationMS,
		NextAction:           evaluation.NextAction,
		Error:                record.Error,
		RunRecordPath:        artifacts.RunRecordPath(workspace),
		EvaluationPath:       artifacts.EvaluationPath(workspace),
	}
}

func emptyAsUnknown(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return value
}
