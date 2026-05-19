package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	orchstate "github.com/weskor/pi-symphony/internal/state"
)

type backfillSummary struct {
	Scanned              int
	Seeded               int
	ReconciliationNeeded int
	Skipped              []backfillSkip
}

type backfillSkip struct {
	Workspace string
	Reason    string
}

type backfillCandidate struct {
	Workspace    string
	Record       runRecord
	Evaluation   evaluationArtifact
	ArtifactTime time.Time
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
	candidatesByIssue := map[string][]backfillCandidate{}
	for _, entry := range entries {
		if !entry.IsDir() || ignoredWorkspaceDir(entry.Name()) {
			continue
		}
		workspace := filepath.Join(workspaceRoot, entry.Name())
		summary.Scanned++
		candidate, err := readBackfillArtifacts(workspace, workspaceRoot)
		if err != nil {
			summary.Skipped = append(summary.Skipped, backfillSkip{Workspace: workspace, Reason: err.Error()})
			continue
		}
		candidatesByIssue[candidate.Record.IssueIdentifier] = append(candidatesByIssue[candidate.Record.IssueIdentifier], candidate)
	}
	for _, issueKey := range sortedBackfillIssueKeys(candidatesByIssue) {
		selected, conflictReason := selectBackfillCandidate(candidatesByIssue[issueKey])
		snapshot := stateProjection{}.RunArtifact(selected.Workspace, selected.Record, selected.Evaluation)
		if conflictReason != "" {
			snapshot.Status = "reconciliation-needed"
			snapshot.TerminalOutcome = "reconciliation-needed"
			snapshot.TerminalReason = conflictReason
		}
		if err := store.UpsertRunArtifact(context.Background(), snapshot); err != nil {
			summary.Skipped = append(summary.Skipped, backfillSkip{Workspace: selected.Workspace, Reason: err.Error()})
			continue
		}
		if conflictReason != "" {
			summary.ReconciliationNeeded++
			continue
		}
		summary.Seeded++
	}
	return summary, nil
}

func ignoredWorkspaceDir(name string) bool {
	return strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
}

func readBackfillArtifacts(workspace, workspaceRoot string) (backfillCandidate, error) {
	path := filepath.Join(workspace, ".pi-symphony-run.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return backfillCandidate{}, fmt.Errorf("missing .pi-symphony-run.json")
		}
		return backfillCandidate{}, fmt.Errorf("read run record: %w", err)
	}
	var record runRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return backfillCandidate{}, fmt.Errorf("malformed .pi-symphony-run.json: %w", err)
	}
	if strings.TrimSpace(record.IssueIdentifier) == "" {
		return backfillCandidate{}, fmt.Errorf("missing required issue_identifier")
	}
	if strings.TrimSpace(record.Status) == "" {
		return backfillCandidate{}, fmt.Errorf("missing required status")
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
			return backfillCandidate{}, fmt.Errorf("malformed %s: %w", evaluationArtifactName, err)
		}
	} else if err != nil && !os.IsNotExist(err) {
		return backfillCandidate{}, fmt.Errorf("read %s: %w", evaluationArtifactName, err)
	}
	artifactTime := record.EndedAt
	if artifactTime.IsZero() {
		artifactTime = record.StartedAt
	}
	if artifactTime.IsZero() {
		if info, err := os.Stat(path); err == nil {
			artifactTime = info.ModTime()
		}
	}
	return backfillCandidate{Workspace: workspace, Record: record, Evaluation: evaluation, ArtifactTime: artifactTime}, nil
}

func sortedBackfillIssueKeys(candidatesByIssue map[string][]backfillCandidate) []string {
	keys := make([]string, 0, len(candidatesByIssue))
	for key := range candidatesByIssue {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func selectBackfillCandidate(candidates []backfillCandidate) (backfillCandidate, string) {
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].ArtifactTime.Equal(candidates[j].ArtifactTime) {
			return candidates[i].Workspace < candidates[j].Workspace
		}
		return candidates[i].ArtifactTime.After(candidates[j].ArtifactTime)
	})
	selected := candidates[0]
	fingerprint := backfillFingerprint(selected)
	for _, candidate := range candidates[1:] {
		if backfillFingerprint(candidate) != fingerprint {
			return selected, fmt.Sprintf("conflicting artifacts for %s require reconciliation", selected.Record.IssueIdentifier)
		}
	}
	return selected, ""
}

func backfillFingerprint(candidate backfillCandidate) string {
	return strings.Join([]string{
		candidate.Record.Status,
		candidate.Record.PRURL,
		firstNonEmpty(candidate.Record.Branch, candidate.Record.ExpectedBranch),
		candidate.Record.ReviewStatus,
		candidate.Record.ReviewClassification,
		candidate.Evaluation.Outcome,
		candidate.Evaluation.NextAction,
	}, "\x00")
}
