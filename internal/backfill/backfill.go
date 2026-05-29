package backfill

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/weskor/agent-machine/internal/artifacts"
	"github.com/weskor/agent-machine/internal/domain"
	"github.com/weskor/agent-machine/internal/state"
	"github.com/weskor/agent-machine/internal/stateprojection"
)

type Summary struct {
	Scanned              int
	Seeded               int
	ReconciliationNeeded int
	Skipped              []Skip
}

type Skip struct {
	Workspace string
	Reason    string
}

type candidate struct {
	Workspace    string
	Record       domain.RunRecord
	Evaluation   artifacts.EvaluationArtifact
	ArtifactTime time.Time
}

func StateFromArtifacts(workspaceRoot string, manager artifacts.Manager, projection stateprojection.Projection) (Summary, error) {
	var summary Summary
	dbPath := state.DefaultDBPath(workspaceRoot)
	if dbPath == "" {
		return summary, fmt.Errorf("workspace.root is required")
	}
	store, err := state.Open(context.Background(), dbPath)
	if err != nil {
		return summary, err
	}
	defer store.Close()

	entries, err := os.ReadDir(workspaceRoot)
	if err != nil {
		return summary, err
	}
	candidatesByIssue := map[string][]candidate{}
	for _, entry := range entries {
		if !entry.IsDir() || ignoredWorkspaceDir(entry.Name()) {
			continue
		}
		workspace := filepath.Join(workspaceRoot, entry.Name())
		summary.Scanned++
		candidate, err := readArtifacts(manager, workspace, workspaceRoot)
		if err != nil {
			summary.Skipped = append(summary.Skipped, Skip{Workspace: workspace, Reason: err.Error()})
			continue
		}
		candidatesByIssue[candidate.Record.IssueIdentifier] = append(candidatesByIssue[candidate.Record.IssueIdentifier], candidate)
	}
	for _, issueKey := range sortedIssueKeys(candidatesByIssue) {
		selected, conflictReason := selectCandidate(candidatesByIssue[issueKey])
		snapshot := projection.RunArtifact(selected.Workspace, selected.Record, selected.Evaluation)
		if conflictReason != "" {
			snapshot.Status = "reconciliation-needed"
			snapshot.TerminalOutcome = "reconciliation-needed"
			snapshot.TerminalReason = conflictReason
		}
		if err := store.UpsertRunArtifact(context.Background(), snapshot); err != nil {
			summary.Skipped = append(summary.Skipped, Skip{Workspace: selected.Workspace, Reason: err.Error()})
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

func readArtifacts(manager artifacts.Manager, workspace, workspaceRoot string) (candidate, error) {
	record, evaluation, artifactTime, err := manager.ReadBackfill(workspace, workspaceRoot)
	if err != nil {
		return candidate{}, err
	}
	return candidate{Workspace: workspace, Record: record, Evaluation: evaluation, ArtifactTime: artifactTime}, nil
}

func sortedIssueKeys(candidatesByIssue map[string][]candidate) []string {
	keys := make([]string, 0, len(candidatesByIssue))
	for key := range candidatesByIssue {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func selectCandidate(candidates []candidate) (candidate, string) {
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].ArtifactTime.Equal(candidates[j].ArtifactTime) {
			return candidates[i].Workspace < candidates[j].Workspace
		}
		return candidates[i].ArtifactTime.After(candidates[j].ArtifactTime)
	})
	selected := candidates[0]
	selectedFingerprint := fingerprint(selected)
	for _, next := range candidates[1:] {
		if fingerprint(next) != selectedFingerprint {
			return selected, fmt.Sprintf("conflicting artifacts for %s require reconciliation", selected.Record.IssueIdentifier)
		}
	}
	return selected, ""
}

func fingerprint(candidate candidate) string {
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
