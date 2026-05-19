package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	orchstate "github.com/weskor/pi-symphony/internal/state"
)

type backfillSummary struct {
	Scanned int
	Seeded  int
	Skipped []backfillSkip
}

type backfillSkip struct {
	Workspace string
	Reason    string
}

func backfillStateFromArtifacts(workspaceRoot string) (backfillSummary, error) {
	var summary backfillSummary
	dbPath := orchstate.DefaultDBPath(workspaceRoot)
	if dbPath == "" {
		return summary, fmt.Errorf("workspace.root is required")
	}
	store, err := orchstate.Open(context.Background(), dbPath)
	if err != nil {
		return summary, err
	}
	defer store.Close()

	entries, err := os.ReadDir(workspaceRoot)
	if err != nil {
		return summary, err
	}
	for _, entry := range entries {
		if !entry.IsDir() || ignoredWorkspaceDir(entry.Name()) {
			continue
		}
		workspace := filepath.Join(workspaceRoot, entry.Name())
		summary.Scanned++
		record, evaluation, err := readBackfillArtifacts(workspace, workspaceRoot)
		if err != nil {
			summary.Skipped = append(summary.Skipped, backfillSkip{Workspace: workspace, Reason: err.Error()})
			continue
		}
		if err := store.UpsertRunArtifact(context.Background(), runArtifactSnapshot(workspace, record, evaluation)); err != nil {
			summary.Skipped = append(summary.Skipped, backfillSkip{Workspace: workspace, Reason: err.Error()})
			continue
		}
		summary.Seeded++
	}
	return summary, nil
}

func ignoredWorkspaceDir(name string) bool {
	return strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
}

func readBackfillArtifacts(workspace, workspaceRoot string) (runRecord, evaluationArtifact, error) {
	path := filepath.Join(workspace, ".pi-symphony-run.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return runRecord{}, evaluationArtifact{}, fmt.Errorf("missing .pi-symphony-run.json")
		}
		return runRecord{}, evaluationArtifact{}, fmt.Errorf("read run record: %w", err)
	}
	var record runRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return runRecord{}, evaluationArtifact{}, fmt.Errorf("malformed .pi-symphony-run.json: %w", err)
	}
	if strings.TrimSpace(record.IssueIdentifier) == "" {
		return runRecord{}, evaluationArtifact{}, fmt.Errorf("missing required issue_identifier")
	}
	if strings.TrimSpace(record.Status) == "" {
		return runRecord{}, evaluationArtifact{}, fmt.Errorf("missing required status")
	}
	if record.Workspace == "" {
		record.Workspace = workspace
	}
	if record.WorkspaceRoot == "" {
		record.WorkspaceRoot = workspaceRoot
	}
	evaluation := evaluationForRun(workspace, record)
	evalPath := filepath.Join(workspace, evaluationArtifactName)
	if evalData, err := os.ReadFile(evalPath); err == nil {
		if err := json.Unmarshal(evalData, &evaluation); err != nil {
			return runRecord{}, evaluationArtifact{}, fmt.Errorf("malformed %s: %w", evaluationArtifactName, err)
		}
	} else if err != nil && !os.IsNotExist(err) {
		return runRecord{}, evaluationArtifact{}, fmt.Errorf("read %s: %w", evaluationArtifactName, err)
	}
	return record, evaluation, nil
}

func runArtifactSnapshot(workspace string, record runRecord, evaluation evaluationArtifact) orchstate.RunArtifactSnapshot {
	repo, prNumber := parseGitHubPR(record.PRURL)
	reviewHash := ""
	if strings.TrimSpace(record.ReviewFindings) != "" {
		sum := sha256.Sum256([]byte(record.ReviewFindings))
		reviewHash = fmt.Sprintf("%x", sum[:])
	}
	retryReason := ""
	retryNextState := ""
	if evaluation.ShouldRetry {
		retryReason = evaluation.RootCause
		if retryReason == "" {
			retryReason = evaluation.Outcome
		}
		retryNextState = evaluation.NextAction
	}
	terminalOutcome := ""
	if terminalRunStatus(record.Status) {
		terminalOutcome = evaluation.Outcome
	}
	return orchstate.RunArtifactSnapshot{
		IssueKey:             record.IssueIdentifier,
		IssueID:              record.IssueID,
		Attempt:              1,
		WorkspacePath:        record.Workspace,
		BranchName:           firstNonEmpty(record.Branch, record.ExpectedBranch),
		BaseBranch:           baseBranchForWorkspace(workspace),
		Status:               record.Status,
		StartedAt:            record.StartedAt,
		UpdatedAt:            record.EndedAt,
		Repository:           repo,
		PRNumber:             prNumber,
		PRURL:                record.PRURL,
		ReviewStatus:         record.ReviewStatus,
		ReviewPassed:         record.ReviewStatus == "passed",
		ReviewClassification: record.ReviewClassification,
		ReviewOutputRef:      filepath.Join(workspace, evaluationArtifactName),
		ReviewOutputHash:     reviewHash,
		MergeEligible:        evaluation.MergeEligible,
		FeedbackHash:         record.FeedbackHash,
		FeedbackNextAction:   evaluation.NextAction,
		RetryCount:           evaluation.FeedbackRetryCount,
		RetryBudgetState:     record.BudgetExceeded,
		RetryReason:          retryReason,
		RetryInputHash:       record.FeedbackHash,
		RetryNextState:       retryNextState,
		TerminalOutcome:      terminalOutcome,
		TerminalReason:       evaluation.RootCause,
		RunArtifactRef:       filepath.Join(workspace, ".pi-symphony-run.json"),
		EvaluationRef:        filepath.Join(workspace, evaluationArtifactName),
	}
}
