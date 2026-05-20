package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/weskor/pi-symphony/internal/state"
)

func printStatus(client linearClient, config runnerConfig) error {
	log("mode=status; project=%s", config.ProjectSlug)
	issues, err := client.candidates(config.ProjectSlug, append(config.ActiveStates, config.DoneState))
	if err != nil {
		return err
	}
	byState := map[string]int{}
	for _, issue := range issues {
		byState[issue.State.Name]++
	}
	log("Linear issues: ready=%d running=%d review=%d done=%d", byState[config.ReadyState], byState[config.RunningState], byState[config.HandoffState], byState[config.DoneState])
	for _, issue := range issues {
		if issue.State.Name == config.DoneState {
			continue
		}
		log("- %s [%s] %s", issue.Identifier, issue.State.Name, issue.Title)
	}

	prs, err := openSymphonyPRs()
	if err != nil {
		return err
	}
	artifacts, err := workspaceArtifactSummaries(config.WorkspaceRoot)
	if err != nil {
		return err
	}
	artifactIndex := indexArtifacts(artifacts)
	log("Open Symphony PRs: %d", len(prs))
	for _, pr := range prs {
		log("- %s", summarizePR(pr, artifactIndex.matchPR(pr)))
	}
	log("Workspaces: %d", len(artifacts))
	for _, summary := range summarizeArtifacts(artifacts) {
		log("- %s", summary)
	}
	prsByIssue := indexPRsByIssue(prs)
	decisions := reconcileIssues(config, issues, prsByIssue, artifactIndex.byIssue)
	for _, line := range summarizeReadyReconciliationDecisions(decisions, config.ReadyState) {
		log("- %s", line)
	}
	for _, line := range summarizeRunningReconciliationDecisions(decisions, config) {
		log("- %s", line)
	}
	for _, line := range summarizeStateStore(config.WorkspaceRoot) {
		log("- %s", line)
	}
	return nil
}

func indexPRsByIssue(prs []pullRequestSummary) map[string]*pullRequestSummary {
	byIssue := map[string]*pullRequestSummary{}
	for i := range prs {
		identifier := issueIdentifierFromBranch(prs[i].HeadRefName)
		if identifier == "" {
			continue
		}
		copy := prs[i]
		byIssue[identifier] = &copy
	}
	return byIssue
}

func summarizeStateStore(workspaceRoot string) []string {
	path := state.DefaultDBPath(workspaceRoot)
	lines := []string{fmt.Sprintf("SQLite state path: %s", emptyAsNA(path))}
	if path == "" {
		return append(lines, "SQLite state health: degraded path=unconfigured action=set workspace.root")
	}
	health, err := state.InspectHealth(context.Background(), path)
	if err != nil {
		return append(lines, fmt.Sprintf("SQLite state health: degraded error=%q action=check state DB path and permissions", err.Error()))
	}
	if !health.Exists {
		return append(lines, "SQLite state health: missing action=run pi-symphony once to initialize mirrored state")
	}
	status := "degraded"
	if health.OK {
		status = "healthy"
	}
	return append(lines,
		fmt.Sprintf("SQLite state health: %s schema_version=%d journal_mode=%s busy_timeout_ms=%d", status, health.SchemaVersion, emptyAsNA(health.JournalMode), health.BusyTimeoutMS),
		fmt.Sprintf("SQLite state counts: issue_attempts=%d pr_mappings=%d review_states=%d terminal_outcomes=%d daemon_heartbeats=%d cleanup_states=%d", health.Counts.IssueAttempts, health.Counts.PRMappings, health.Counts.ReviewStates, health.Counts.TerminalOutcomes, health.Counts.DaemonHeartbeats, health.Counts.CleanupStates),
	)
}

func openSymphonyPRs() ([]pullRequestSummary, error) {
	github, ctx, cancel, err := githubClientWithTimeout(defaultGitHubCommandTimeout)
	if err != nil {
		return nil, err
	}
	defer cancel()
	prs, err := github.OpenPullRequests(ctx)
	if err != nil {
		return nil, fmt.Errorf("GitHub API open PR metadata lookup failed: %w", err)
	}
	return symphonyPRs(prs), nil
}

type artifactSummary struct {
	Issue             string
	Status            string
	Review            string
	PRURL             string
	Outcome           string
	RootCause         string
	NextAction        string
	ChecksStatus      string
	MergeBlockReason  string
	MergeBlockerCodes []string
	BlockedBy         []string
	Frictions         []string
	TotalTokens       float64
	TotalCost         float64
	HasArtifact       bool
	HasEvaluation     bool
	Cleanable         bool
	Class             string
	MergeEligible     bool
	ShouldRetry       bool
	OperatorAttention bool
	TicketContract    []string
}

type artifactIndex struct {
	byIssue map[string]artifactSummary
	byPRURL map[string]artifactSummary
}

func workspaceArtifactSummaries(workspaceRoot string) ([]artifactSummary, error) {
	entries, err := os.ReadDir(workspaceRoot)
	if err != nil {
		return nil, err
	}
	summaries := make([]artifactSummary, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		workspace := filepath.Join(workspaceRoot, entry.Name())
		summary := artifactSummary{Issue: entry.Name()}
		data, err := os.ReadFile(filepath.Join(workspace, ".pi-symphony-run.json"))
		if err == nil {
			var record runRecord
			if err := json.Unmarshal(data, &record); err != nil {
				return nil, err
			}
			summary.HasArtifact = true
			summary.Status = record.Status
			summary.Class = cleanupCategoryForTerminalStatus(record.Status)
			summary.Review = record.ReviewStatus
			summary.PRURL = record.PRURL
			if record.PiUsage != nil {
				summary.TotalTokens += record.PiUsage.TotalTokens
				if record.PiUsage.Cost != nil {
					summary.TotalCost += record.PiUsage.Cost.Total
				}
			}
			if record.ReviewUsage != nil {
				summary.TotalTokens += record.ReviewUsage.TotalTokens
				if record.ReviewUsage.Cost != nil {
					summary.TotalCost += record.ReviewUsage.Cost.Total
				}
			}
			if terminalRunStatus(record.Status) {
				summary.Cleanable = true
			}
		} else if !os.IsNotExist(err) {
			return nil, err
		}
		evaluationData, err := os.ReadFile(filepath.Join(workspace, evaluationArtifactName))
		if err == nil {
			var evaluation evaluationArtifact
			if err := json.Unmarshal(evaluationData, &evaluation); err != nil {
				return nil, err
			}
			summary.HasEvaluation = true
			if evaluation.IssueIdentifier != "" {
				summary.Issue = evaluation.IssueIdentifier
			}
			if evaluation.PRURL != "" {
				summary.PRURL = evaluation.PRURL
			}
			summary.Outcome = evaluation.Outcome
			summary.MergeEligible = evaluation.MergeEligible
			summary.BlockedBy = evaluation.BlockedBy
			summary.RootCause = evaluation.RootCause
			summary.NextAction = evaluation.NextAction
			summary.ShouldRetry = evaluation.ShouldRetry
			summary.OperatorAttention = evaluation.OperatorAttentionRequired
			summary.ChecksStatus = evaluation.ChecksStatus
			summary.MergeBlockReason = evaluation.MergeBlockReason
			summary.MergeBlockerCodes = evaluation.MergeBlockerCodes
			summary.Frictions = evaluation.FrictionSignals
			summary.TicketContract = evaluation.TicketContractEvidence
		} else if !os.IsNotExist(err) {
			return nil, err
		}
		summaries = append(summaries, summary)
	}
	sort.Slice(summaries, func(i, j int) bool { return summaries[i].Issue < summaries[j].Issue })
	return summaries, nil
}

func summarizeArtifacts(artifacts []artifactSummary) []string {
	if len(artifacts) == 0 {
		return []string{"none"}
	}
	lines := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		if !artifact.HasArtifact {
			lines = append(lines, fmt.Sprintf("%s missing artifact", artifact.Issue))
			continue
		}
		review := artifact.Review
		if review == "" {
			review = "n/a"
		}
		cleanable := "actionable"
		if artifact.Cleanable {
			cleanable = "historical"
		}
		class := artifact.Class
		if class == "" {
			class = cleanupCategoryForTerminalStatus(artifact.Status)
		}
		evaluation := "eval=missing"
		if artifact.HasEvaluation {
			evaluation = fmt.Sprintf("outcome=%s root=%s next=%s retry=%t attention=%t merge_eligible=%t blocked_by=%s checks=%s friction=%s ticket_contract=%s", emptyAsNA(artifact.Outcome), emptyAsNA(artifact.RootCause), emptyAsNA(artifact.NextAction), artifact.ShouldRetry, artifact.OperatorAttention, artifact.MergeEligible, summarizeFrictionSignals(artifact.BlockedBy), emptyAsNA(artifact.ChecksStatus), summarizeFrictionSignals(artifact.Frictions), summarizeFrictionSignals(artifact.TicketContract))
		}
		lines = append(lines, fmt.Sprintf("%s class=%s status=%s review=%s tokens=%.0f cost=$%.4f bucket=%s pr=%s %s", artifact.Issue, class, artifact.Status, review, artifact.TotalTokens, artifact.TotalCost, cleanable, artifact.PRURL, evaluation))
	}
	for _, line := range summarizeRecurringFriction(artifacts, 5) {
		lines = append(lines, line)
	}
	return lines
}

func summarizeFrictionSignals(signals []string) string {
	if len(signals) == 0 {
		return "none"
	}
	return strings.Join(signals, ",")
}

func indexArtifacts(artifacts []artifactSummary) artifactIndex {
	index := artifactIndex{byIssue: map[string]artifactSummary{}, byPRURL: map[string]artifactSummary{}}
	for _, artifact := range artifacts {
		if artifact.Issue != "" {
			index.byIssue[artifact.Issue] = artifact
		}
		if artifact.PRURL != "" {
			index.byPRURL[artifact.PRURL] = artifact
		}
	}
	return index
}

func (index artifactIndex) matchPR(pr pullRequestSummary) *artifactSummary {
	if artifact, ok := index.byPRURL[pr.URL]; ok {
		return &artifact
	}
	for issue, artifact := range index.byIssue {
		if strings.Contains(pr.HeadRefName, issue) {
			return &artifact
		}
	}
	return nil
}

func summarizePR(pr pullRequestSummary, artifact *artifactSummary) string {
	checks := "pending/failing"
	if checksPassed(pr.StatusCheckRollup) {
		checks = "green"
	}
	merge := "mergeable"
	reason := ""
	prGate := evaluatePullRequestMergeGate(pr)
	if hasString(prGate.Codes(), "merge_conflict") {
		merge = "conflicting"
		reason = fmt.Sprintf(" reason=%s", prGate.Reason())
	}
	gate := "artifact_gate=unknown"
	if artifact != nil && artifact.HasEvaluation {
		gate = fmt.Sprintf("artifact_gate=outcome:%s merge_eligible:%t retry:%t attention:%t next:%s", emptyAsNA(artifact.Outcome), artifact.MergeEligible, artifact.ShouldRetry, artifact.OperatorAttention, emptyAsNA(artifact.NextAction))
	}
	return fmt.Sprintf("#%d %s review=%s checks=%s merge=%s branch=%s %s%s", pr.Number, pr.URL, pr.ReviewDecision, checks, merge, pr.HeadRefName, gate, reason)
}

func summarizeReadyReconciliation(issues []issue, artifacts map[string]artifactSummary, readyState string) []string {
	return summarizeReadyReconciliationDecisions(reconcileIssues(runnerConfig{ReadyState: readyState}, issues, nil, artifacts), readyState)
}

func summarizeReadyReconciliationDecisions(decisions []reconciliationDecision, readyState string) []string {
	var lines []string
	for _, decision := range decisions {
		if decision.StateName != readyState || decision.Artifact == nil {
			continue
		}
		artifact := *decision.Artifact
		if !artifact.Cleanable {
			continue
		}
		next := artifact.NextAction
		if next == "" {
			next = decision.NextAction
		}
		if next == "" {
			next = "reconcile_linear_state_or_clear_terminal_artifact"
		}
		lines = append(lines, fmt.Sprintf("Reconcile Ready issue with terminal artifact: %s status=%s outcome=%s next=%s attention=%t", decision.IssueIdentifier, artifact.Status, emptyAsNA(artifact.Outcome), next, artifact.OperatorAttention))
	}
	if len(lines) == 0 {
		return nil
	}
	sort.Strings(lines)
	return append([]string{"Actionable reconciliation:"}, lines...)
}

func summarizeRunningReconciliation(issues []issue, artifacts map[string]artifactSummary, config runnerConfig) []string {
	return summarizeRunningReconciliationDecisions(reconcileIssues(config, issues, nil, artifacts), config)
}

func summarizeRunningReconciliationDecisions(decisions []reconciliationDecision, config runnerConfig) []string {
	var lines []string
	for _, decision := range decisions {
		if decision.StateName != config.RunningState || decision.Lifecycle == lifecycleRunning || decision.IssueIdentifier == "" {
			continue
		}
		artifact := decision.Artifact
		if artifact != nil && !artifact.Cleanable {
			continue
		}
		next := "restart_runner_or_move_issue_ready"
		if artifact != nil && artifact.NextAction != "" {
			next = artifact.NextAction
		}
		status := "missing"
		outcome := "n/a"
		if artifact != nil {
			status = emptyAsNA(artifact.Status)
			outcome = emptyAsNA(artifact.Outcome)
		}
		lines = append(lines, fmt.Sprintf("Reconcile In Progress issue with no active run lock: %s artifact_status=%s outcome=%s next=%s", decision.IssueIdentifier, status, outcome, next))
	}
	if len(lines) == 0 {
		return nil
	}
	sort.Strings(lines)
	return append([]string{"Actionable In Progress reconciliation:"}, lines...)
}

func emptyAsNA(value string) string {
	if strings.TrimSpace(value) == "" {
		return "n/a"
	}
	return value
}

func summarizeRecurringFriction(artifacts []artifactSummary, limit int) []string {
	counts := map[string]int{}
	for _, artifact := range artifacts {
		for _, signal := range artifact.Frictions {
			counts[signal]++
		}
		if artifact.Outcome != "" {
			counts["outcome:"+artifact.Outcome]++
		}
		if artifact.RootCause != "" && artifact.RootCause != "none" {
			counts["root:"+artifact.RootCause]++
		}
		if artifact.NextAction != "" {
			counts["next:"+artifact.NextAction]++
		}
	}
	if len(counts) == 0 {
		return nil
	}
	type pair struct {
		Signal string
		Count  int
	}
	pairs := make([]pair, 0, len(counts))
	for signal, count := range counts {
		pairs = append(pairs, pair{Signal: signal, Count: count})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].Count == pairs[j].Count {
			return pairs[i].Signal < pairs[j].Signal
		}
		return pairs[i].Count > pairs[j].Count
	})
	if limit > 0 && len(pairs) > limit {
		pairs = pairs[:limit]
	}
	parts := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		parts = append(parts, fmt.Sprintf("%s=%d", pair.Signal, pair.Count))
	}
	return []string{fmt.Sprintf("Recurring friction: %s", strings.Join(parts, ", "))}
}
