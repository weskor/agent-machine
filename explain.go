package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type explainReport struct {
	Mode    string                   `json:"mode"`
	Next    explainNextDecision      `json:"next"`
	Merge   []explainMergeDecision   `json:"merge"`
	Cleanup []explainCleanupDecision `json:"cleanup"`
}

type explainNextDecision struct {
	Selected   string                     `json:"selected,omitempty"`
	Candidates []explainCandidateDecision `json:"candidates"`
}

type explainCandidateDecision struct {
	Identifier string   `json:"identifier"`
	State      string   `json:"state"`
	Runnable   bool     `json:"runnable"`
	Selected   bool     `json:"selected"`
	Reason     string   `json:"reason"`
	Blockers   []string `json:"blockers,omitempty"`
	NextAction string   `json:"next_action,omitempty"`
	Order      string   `json:"order"`
}

type explainMergeDecision struct {
	Issue      string   `json:"issue,omitempty"`
	PR         string   `json:"pr"`
	CanMerge   bool     `json:"can_merge"`
	Reason     string   `json:"reason"`
	Blockers   []string `json:"blockers,omitempty"`
	NextAction string   `json:"next_action,omitempty"`
}

type explainCleanupDecision struct {
	Issue    string `json:"issue"`
	Eligible bool   `json:"eligible"`
	Category string `json:"category"`
	Reason   string `json:"reason"`
}

func printExplain(client linearClient, config runnerConfig) error {
	candidates, err := client.candidates(config.ProjectSlug, config.ActiveStates)
	if err != nil {
		return err
	}
	doneIssues, err := client.issueIdentifiersByState(config.ProjectSlug, config.DoneState)
	if err != nil {
		return err
	}
	prsByIssue, err := openPRsByIssueForSelection(config)
	if err != nil {
		return err
	}
	report, err := explain(config, candidates, prsByIssue, doneIssues)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

func explain(config runnerConfig, candidates []issue, prsByIssue map[string]*pullRequestSummary, doneIssues map[string]bool) (explainReport, error) {
	report := explainReport{Mode: "explain"}
	report.Next = explainCandidateSelection(config, candidates, prsByIssue)
	report.Merge = explainMergeDecisions(config, prsByIssue, candidates)
	cleanup, err := explainCleanup(config.WorkspaceRoot, doneIssues)
	if err != nil {
		return report, err
	}
	report.Cleanup = cleanup
	return report, nil
}

func explainCandidateSelection(config runnerConfig, candidates []issue, prsByIssue map[string]*pullRequestSummary) explainNextDecision {
	ordered := orderCandidates(candidates, config.ReadyState)
	decisions := make([]explainCandidateDecision, 0, len(ordered))
	selected := ""
	for pass := 0; pass < 2 && selected == ""; pass++ {
		for i := range ordered {
			candidate := ordered[i]
			decision := reconcileIssue(config, candidate, prsByIssue[candidate.Identifier])
			if pass == 0 && candidate.State.Name != config.ReadyState {
				continue
			}
			if decision.Lifecycle == lifecycleBlocked && strings.Contains(strings.Join(decision.Blockers, ","), "blocked label") {
				continue
			}
			if decision.CanRun {
				selected = candidate.Identifier
				break
			}
		}
	}
	for i := range ordered {
		candidate := ordered[i]
		decision := reconcileIssue(config, candidate, prsByIssue[candidate.Identifier])
		reason := "would run"
		if !decision.CanRun {
			reason = strings.Join(decision.Blockers, "; ")
			if reason == "" {
				reason = fmt.Sprintf("lifecycle=%s", decision.Lifecycle)
			}
		} else if candidate.Identifier != selected {
			reason = "lower ordered runnable candidate"
		}
		if decision.ReconciliationNeeded {
			reason = strings.TrimSpace(reason + "; reconciliation_needed")
		}
		decisions = append(decisions, explainCandidateDecision{
			Identifier: candidate.Identifier,
			State:      candidate.State.Name,
			Runnable:   decision.CanRun,
			Selected:   candidate.Identifier == selected,
			Reason:     reason,
			Blockers:   decision.Blockers,
			NextAction: decision.NextAction,
			Order:      candidateOrderReason(candidate, config.ReadyState),
		})
	}
	return explainNextDecision{Selected: selected, Candidates: decisions}
}

func explainMergeDecisions(config runnerConfig, prsByIssue map[string]*pullRequestSummary, candidates []issue) []explainMergeDecision {
	issues := map[string]issue{}
	for _, candidate := range candidates {
		issues[candidate.Identifier] = candidate
	}
	out := make([]explainMergeDecision, 0, len(prsByIssue))
	for identifier, pr := range prsByIssue {
		gate := evaluatePullRequestMergeGate(*pr)
		decision := reconciliationDecision{}
		if candidate, ok := issues[identifier]; ok {
			decision = reconcileIssue(config, candidate, pr)
		}
		reason := gate.Reason()
		canMerge := gate.Eligible && decision.CanMerge && pr.ReviewDecision == "APPROVED"
		blockers := make([]string, 0, len(gate.Blockers)+len(decision.Blockers)+1)
		for _, blocker := range gate.Blockers {
			blockers = append(blockers, blocker.Reason)
		}
		blockers = append(blockers, decision.Blockers...)
		if pr.ReviewDecision != "APPROVED" {
			blockers = append(blockers, "review decision is "+emptyAsUnknown(pr.ReviewDecision))
		}
		if len(blockers) > 0 {
			reason = strings.Join(blockers, "; ")
		}
		out = append(out, explainMergeDecision{Issue: identifier, PR: pr.URL, CanMerge: canMerge, Reason: reason, Blockers: blockers, NextAction: decision.NextAction})
	}
	return out
}

func explainCleanup(workspaceRoot string, doneIssues map[string]bool) ([]explainCleanupDecision, error) {
	safeRoot, err := safeWorkspaceRoot(workspaceRoot)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(safeRoot)
	if err != nil {
		return nil, err
	}
	out := []explainCleanupDecision{}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || entry.Name() == "state" {
			continue
		}
		workspace := filepath.Join(safeRoot, entry.Name())
		decision, err := cleanupDecisionForRootWithChanges(safeRoot, workspace, doneIssues, workspaceHasChangesForExplain)
		if err != nil {
			return nil, err
		}
		out = append(out, explainCleanupDecision{Issue: decision.IssueIdentifier, Eligible: decision.Delete, Category: decision.Category, Reason: decision.Reason})
	}
	return out, nil
}

func workspaceHasChangesForExplain(workspace string) (bool, error) {
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = workspace
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("git status --porcelain: %w: %s", err, strings.TrimSpace(string(output)))
	}
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		path := strings.TrimSpace(strings.TrimPrefix(line, "??"))
		if strings.HasPrefix(path, ".pi-symphony-debug/") || path == ".pi-symphony-debug" {
			continue
		}
		switch path {
		case ".pi-symphony-run.json", ".pi-symphony-evaluation.json", ".pi-symphony-prompt.md", ".pi-symphony-review-prompt.md":
			continue
		default:
			return true, nil
		}
	}
	return false, nil
}
