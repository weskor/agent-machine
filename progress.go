package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const runProgressArtifactName = "progress.json"
const handoffPendingPayloadArtifactName = "handoff-pending.json"

type runProgressSnapshot struct {
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
	HandoffPayloadPath   string    `json:"handoff_payload_path,omitempty"`
}

func runProgressRoot(workspaceRoot string) string {
	if strings.TrimSpace(workspaceRoot) == "" {
		return ""
	}
	clean := filepath.Clean(workspaceRoot)
	if filepath.Base(clean) == "workspaces" && filepath.Base(filepath.Dir(clean)) == ".symphony" {
		return filepath.Join(filepath.Dir(clean), "state", "run-progress")
	}
	return filepath.Join(clean, "state", "run-progress")
}

func runProgressPath(workspaceRoot, issueIdentifier string) (string, error) {
	root := runProgressRoot(workspaceRoot)
	if root == "" {
		return "", errors.New("workspace root is required")
	}
	issue := strings.TrimSpace(issueIdentifier)
	if issue == "" || filepath.Base(filepath.Clean(issue)) != issue {
		return "", fmt.Errorf("issue identifier %q is not a safe run progress name", issueIdentifier)
	}
	return filepath.Join(root, issue, runProgressArtifactName), nil
}

func handoffPendingPayloadPath(workspaceRoot, issueIdentifier string) (string, error) {
	progressPath, err := runProgressPath(workspaceRoot, issueIdentifier)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(progressPath), handoffPendingPayloadArtifactName), nil
}

func writeRunProgress(workspaceRoot string, snapshot runProgressSnapshot) {
	if err := writeRunProgressResult(workspaceRoot, snapshot); err != nil {
		log("failed to write run progress for %s: %v", snapshot.IssueIdentifier, err)
	}
}

func writeRunProgressResult(workspaceRoot string, snapshot runProgressSnapshot) error {
	path, err := runProgressPath(workspaceRoot, snapshot.IssueIdentifier)
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
	return os.Rename(tmp, path)
}

func readRunProgress(workspaceRoot, issueIdentifier string) (runProgressSnapshot, error) {
	path, err := runProgressPath(workspaceRoot, issueIdentifier)
	if err != nil {
		return runProgressSnapshot{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return runProgressSnapshot{}, fmt.Errorf("no run progress snapshot for %s at %s", issueIdentifier, path)
		}
		return runProgressSnapshot{}, err
	}
	var snapshot runProgressSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return runProgressSnapshot{}, err
	}
	if snapshot.ProgressPath == "" {
		snapshot.ProgressPath = path
	}
	if snapshot.HandoffPayloadPath == "" && snapshot.Phase == runProgressPhaseHandoffPending {
		if payloadPath, err := handoffPendingPayloadPath(workspaceRoot, issueIdentifier); err == nil {
			snapshot.HandoffPayloadPath = payloadPath
		}
	}
	return snapshot, nil
}

func printRunProgress(workspaceRoot, issueIdentifier string) error {
	snapshot, err := readRunProgress(workspaceRoot, issueIdentifier)
	if err != nil {
		return err
	}
	fmt.Println(formatRunProgress(snapshot))
	return nil
}

func formatRunProgress(snapshot runProgressSnapshot) string {
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
	if snapshot.HandoffPayloadPath != "" {
		parts = append(parts, "handoff_payload="+snapshot.HandoffPayloadPath)
	}
	parts = append(parts, "progress="+snapshot.ProgressPath)
	return strings.Join(parts, " ")
}

func runProgressForIssue(candidate *issue, workspace, phase string, startedAt time.Time) runProgressSnapshot {
	branch := ""
	if info, err := os.Stat(workspace); err == nil && info.IsDir() {
		branch, _ = currentGitBranch(workspace)
	}
	return runProgressSnapshot{
		IssueIdentifier: candidate.Identifier,
		IssueTitle:      candidate.Title,
		Phase:           phase,
		Branch:          branch,
		ExpectedBranch:  expectedWorkspaceBranch(candidate.Identifier),
		Workspace:       workspace,
		StartedAt:       startedAt,
		UpdatedAt:       time.Now().UTC(),
	}
}

func runProgressForRecord(workspace string, record runRecord, evaluation evaluationArtifact) runProgressSnapshot {
	phase := record.Status
	switch record.Status {
	case runAttemptStatusSuccess:
		phase = "completed"
	case runAttemptStatusFailed, runAttemptStatusTimeout, runAttemptStatusBudgetExceeded, runAttemptStatusGitHubAppError, runAttemptStatusNeedsInfoFail:
		phase = "failed"
	}
	return runProgressSnapshot{
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
		RunRecordPath:        filepath.Join(workspace, ".pi-symphony-run.json"),
		EvaluationPath:       filepath.Join(workspace, evaluationArtifactName),
	}
}
