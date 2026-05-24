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

	"github.com/weskor/pi-symphony/internal/state"
)

func printStatus(client linearClient, config runnerConfig) error {
	log("mode=status; project=%s", config.ProjectSlug)
	issues, err := client.candidates(config.ProjectSlug, statusIssueStates(config))
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
	snapshot, err := buildOrchestrationSnapshot(context.Background(), config, time.Now().UTC())
	if err != nil {
		return err
	}
	artifacts := snapshot.Artifacts
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
	store, _ := commandScopedStateStore(context.Background(), config.WorkspaceRoot, "status-reconciliation")
	if store != nil {
		defer store.Close()
	}
	decisions := repairableReviewFailedReconciliationDecisions(config, issues, prsByIssue, newReconciliationModule(store).ReconcileIssues(config, issues, prsByIssue, artifactIndex.byIssue))
	for _, line := range summarizeReadyReconciliationDecisions(decisions, config.ReadyState) {
		log("- %s", line)
	}
	for _, line := range summarizeRunningReconciliationDecisions(decisions, config) {
		log("- %s", line)
	}
	for _, line := range summarizeSnapshotStateStore(config.WorkspaceRoot, snapshot) {
		log("- %s", line)
	}
	return nil
}

func statusIssueStates(config runnerConfig) []string {
	states := append([]string{}, config.ActiveStates...)
	states = append(states, config.HandoffState, config.DoneState)
	seen := map[string]bool{}
	unique := states[:0]
	for _, state := range states {
		if state == "" || seen[state] {
			continue
		}
		seen[state] = true
		unique = append(unique, state)
	}
	return unique
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

func summarizeSnapshotStateStore(workspaceRoot string, snapshot orchestrationSnapshot) []string {
	path := state.DefaultDBPath(workspaceRoot)
	lines := []string{fmt.Sprintf("SQLite state path: %s", emptyAsNA(path))}
	if path == "" {
		return append(lines, "SQLite state health: degraded path=unconfigured action=set workspace.root")
	}
	if snapshot.SQLiteHealthError != "" {
		return append(lines, fmt.Sprintf("SQLite state health: degraded error=%q action=check state DB path and permissions", snapshot.SQLiteHealthError))
	}
	health := snapshot.SQLiteHealth
	if !health.Exists {
		return append(lines, "SQLite state health: missing action=run pi-symphony --continuous to initialize durable state")
	}
	status := "degraded"
	if health.OK {
		status = "healthy"
	}
	lines = append(lines,
		fmt.Sprintf("SQLite state health: %s schema_version=%d journal_mode=%s busy_timeout_ms=%d", status, health.SchemaVersion, emptyAsNA(health.JournalMode), health.BusyTimeoutMS),
		formatStateCounts(health.Counts),
	)
	lines = append(lines, summarizeActiveLanes(snapshot.ActiveLanes)...)
	lines = append(lines, summarizeWorkerTasks(snapshot.WorkerTasks)...)
	lines = append(lines, summarizeWorkerResults(snapshot.WorkerResults)...)
	if len(snapshot.RecentEvents) > 0 {
		lines = append(lines, "SQLite recent events:")
		for _, event := range snapshot.RecentEvents {
			lines = append(lines, formatEventSummary(event))
		}
	}
	return lines
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
		return append(lines, "SQLite state health: missing action=run pi-symphony --continuous to initialize durable state")
	}
	status := "degraded"
	if health.OK {
		status = "healthy"
	}
	lines = append(lines,
		fmt.Sprintf("SQLite state health: %s schema_version=%d journal_mode=%s busy_timeout_ms=%d", status, health.SchemaVersion, emptyAsNA(health.JournalMode), health.BusyTimeoutMS),
		formatStateCounts(health.Counts),
	)
	store, err := state.Open(context.Background(), path)
	if err != nil {
		return lines
	}
	defer store.Close()
	heartbeats, err := store.SnapshotHeartbeats(context.Background())
	if err == nil {
		lanes := make([]snapshotLane, 0, len(heartbeats))
		for _, heartbeat := range heartbeats {
			lanes = append(lanes, snapshotLane{Name: heartbeat.LaneName, ProcessID: heartbeat.ProcessID, CycleNumber: heartbeat.CycleNumber, LastSuccessAt: heartbeat.LastSuccessAt, LastError: heartbeat.LastError, RecoveryRequired: heartbeat.RecoveryRequired, ActiveTaskKey: heartbeat.ActiveTaskKey, ActiveTaskRole: heartbeat.ActiveTaskRole, ActiveLeaseName: heartbeat.ActiveLeaseName, ActiveTaskStartedAt: heartbeat.ActiveTaskStartedAt, UpdatedAt: heartbeat.UpdatedAt, Source: "sqlite"})
		}
		lines = append(lines, summarizeActiveLanes(lanes)...)
	}
	tasks, err := store.WorkerTasks(context.Background(), "")
	if err == nil {
		lines = append(lines, summarizeWorkerTasks(snapshotWorkerTasks(tasks))...)
	}
	results, err := store.WorkerResults(context.Background(), "")
	if err == nil {
		lines = append(lines, summarizeWorkerResults(snapshotWorkerResults(results))...)
	}
	events, err := store.RecentEvents(context.Background(), 5)
	if err != nil || len(events) == 0 {
		return lines
	}
	lines = append(lines, "SQLite recent events:")
	for _, event := range events {
		lines = append(lines, formatEventSummary(eventSummary{Sequence: event.Sequence, OccurredAt: event.OccurredAt, IssueKey: event.IssueKey, Source: event.Source, Type: event.Type}))
	}
	return lines
}

func formatStateCounts(counts state.Counts) string {
	return fmt.Sprintf("SQLite state counts: issue_attempts=%d pr_mappings=%d review_states=%d terminal_outcomes=%d daemon_heartbeats=%d cleanup_states=%d worker_tasks=%d worker_results=%d worker_payload_refs=%d pr_handoff_intents=%d events=%d", counts.IssueAttempts, counts.PRMappings, counts.ReviewStates, counts.TerminalOutcomes, counts.DaemonHeartbeats, counts.CleanupStates, counts.WorkerTasks, counts.WorkerResults, counts.WorkerPayloadRefs, counts.PRHandoffIntents, counts.Events)
}

func snapshotWorkerTasks(tasks []state.WorkerTask) []snapshotWorkerTask {
	out := make([]snapshotWorkerTask, 0, len(tasks))
	for _, task := range tasks {
		out = append(out, snapshotWorkerTask{TaskKey: task.TaskKey, Role: task.Role, IssueKey: task.IssueKey, Attempt: task.Attempt, Status: task.Status, Priority: task.Priority, LeaseName: task.LeaseName, AvailableAt: task.AvailableAt, UpdatedAt: task.UpdatedAt})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		return out[i].TaskKey < out[j].TaskKey
	})
	return out
}

func snapshotWorkerResults(results []state.WorkerResult) []snapshotWorkerResult {
	out := make([]snapshotWorkerResult, 0, len(results))
	for _, result := range results {
		out = append(out, snapshotWorkerResult{TaskKey: result.TaskKey, Role: result.Role, LaneName: result.LaneName, IssueKey: result.IssueKey, Attempt: result.Attempt, Status: result.Status, DidWork: result.DidWork, Reason: result.Reason, Error: result.Error, StartedAt: result.StartedAt, FinishedAt: result.FinishedAt, UpdatedAt: result.UpdatedAt})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		return out[i].TaskKey < out[j].TaskKey
	})
	return out
}

func summarizeActiveLanes(lanes []snapshotLane) []string {
	if len(lanes) == 0 {
		return nil
	}
	sort.Slice(lanes, func(i, j int) bool {
		if !lanes[i].UpdatedAt.Equal(lanes[j].UpdatedAt) {
			return lanes[i].UpdatedAt.After(lanes[j].UpdatedAt)
		}
		return lanes[i].Name < lanes[j].Name
	})
	lines := []string{fmt.Sprintf("SQLite active lanes: total=%d", len(lanes))}
	for _, lane := range lanes {
		lines = append(lines, formatLaneSummary(lane))
	}
	return lines
}

func formatLaneSummary(lane snapshotLane) string {
	active := ""
	if lane.ActiveTaskKey != "" {
		active = fmt.Sprintf(" active_task=%s active_role=%s active_lease=%s active_started_at=%s", emptyAsNA(lane.ActiveTaskKey), emptyAsNA(lane.ActiveTaskRole), emptyAsNA(lane.ActiveLeaseName), formatOptionalTime(lane.ActiveTaskStartedAt))
	}
	errorText := ""
	if lane.LastError != "" {
		errorText = fmt.Sprintf(" error=%q", lane.LastError)
	}
	return fmt.Sprintf("- lane=%s process=%s cycle=%d recovery_required=%t updated_at=%s%s%s", emptyAsNA(lane.Name), emptyAsNA(lane.ProcessID), lane.CycleNumber, lane.RecoveryRequired, formatOptionalTime(lane.UpdatedAt), active, errorText)
}

func summarizeWorkerTasks(tasks []snapshotWorkerTask) []string {
	if len(tasks) == 0 {
		return nil
	}
	counts := map[string]int{}
	keys := make([]string, 0)
	for _, task := range tasks {
		key := fmt.Sprintf("%s:%s", emptyAsUnknown(task.Role), emptyAsUnknown(task.Status))
		if _, ok := counts[key]; !ok {
			keys = append(keys, key)
		}
		counts[key]++
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, counts[key]))
	}
	lines := []string{fmt.Sprintf("SQLite worker tasks: total=%d %s", len(tasks), strings.Join(parts, " "))}
	lines = append(lines, "SQLite recent worker tasks:")
	limit := len(tasks)
	if limit > 5 {
		limit = 5
	}
	for i := 0; i < limit; i++ {
		lines = append(lines, formatWorkerTaskSummary(tasks[i]))
	}
	return lines
}

func formatWorkerTaskSummary(task snapshotWorkerTask) string {
	return fmt.Sprintf("- task=%s role=%s status=%s issue=%s attempt=%d priority=%d lease=%s available_at=%s updated_at=%s", emptyAsNA(task.TaskKey), emptyAsUnknown(task.Role), emptyAsUnknown(task.Status), emptyAsNA(task.IssueKey), task.Attempt, task.Priority, emptyAsNA(task.LeaseName), formatOptionalTime(task.AvailableAt), formatOptionalTime(task.UpdatedAt))
}

func summarizeWorkerResults(results []snapshotWorkerResult) []string {
	if len(results) == 0 {
		return nil
	}
	counts := map[string]int{}
	keys := make([]string, 0)
	for _, result := range results {
		key := fmt.Sprintf("%s:%s", emptyAsUnknown(result.Role), emptyAsUnknown(result.Status))
		if _, ok := counts[key]; !ok {
			keys = append(keys, key)
		}
		counts[key]++
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, counts[key]))
	}
	lines := []string{fmt.Sprintf("SQLite worker results: total=%d %s", len(results), strings.Join(parts, " "))}
	lines = append(lines, "SQLite recent worker results:")
	limit := len(results)
	if limit > 5 {
		limit = 5
	}
	for i := 0; i < limit; i++ {
		lines = append(lines, formatWorkerResultSummary(results[i]))
	}
	return lines
}

func formatWorkerResultSummary(result snapshotWorkerResult) string {
	errorText := ""
	if strings.TrimSpace(result.Error) != "" {
		errorText = fmt.Sprintf(" error=%q", result.Error)
	}
	return fmt.Sprintf("- task=%s role=%s lane=%s status=%s did_work=%t reason=%s issue=%s attempt=%d finished_at=%s%s", emptyAsNA(result.TaskKey), emptyAsUnknown(result.Role), emptyAsNA(result.LaneName), emptyAsUnknown(result.Status), result.DidWork, emptyAsNA(result.Reason), emptyAsNA(result.IssueKey), result.Attempt, formatOptionalTime(result.FinishedAt), errorText)
}

func formatOptionalTime(value time.Time) string {
	if value.IsZero() {
		return "UNKNOWN"
	}
	return value.UTC().Format(time.RFC3339)
}

func formatEventSummary(event eventSummary) string {
	issue := emptyAsNA(event.IssueKey)
	return fmt.Sprintf("- #%d %s issue=%s source=%s at=%s", event.Sequence, event.Type, issue, event.Source, event.OccurredAt.UTC().Format(time.RFC3339))
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
		if os.IsNotExist(err) {
			return nil, nil
		}
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
		if entry.Name() == "state" {
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
			if usage := recordRuntimeUsage(record); usage != nil {
				summary.TotalTokens += usage.TotalTokens
				if usage.Cost != nil {
					summary.TotalCost += usage.Cost.Total
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
	lines = append(lines, summarizeRecurringFriction(artifacts, 5)...)
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

func repairableReviewFailedReconciliationDecisions(config runnerConfig, issues []issue, prsByIssue map[string]*pullRequestSummary, decisions []reconciliationDecision) []reconciliationDecision {
	if len(decisions) == 0 || len(prsByIssue) == 0 {
		return decisions
	}
	issuesByIdentifier := map[string]issue{}
	for _, candidate := range issues {
		issuesByIdentifier[candidate.Identifier] = candidate
	}
	out := append([]reconciliationDecision(nil), decisions...)
	for i := range out {
		candidate, ok := issuesByIdentifier[out[i].IssueIdentifier]
		if !ok {
			continue
		}
		out[i] = decisionWithRepairableReviewFailedPR(config, candidate, prsByIssue[candidate.Identifier], out[i])
	}
	return out
}

func summarizeReadyReconciliationDecisions(decisions []reconciliationDecision, readyState string) []string {
	var lines []string
	for _, decision := range decisions {
		if decision.StateName != readyState {
			continue
		}
		if decision.Artifact == nil {
			if !decision.ReconciliationNeeded {
				continue
			}
			next := decision.NextAction
			if next == "" {
				next = "reconcile_sqlite_external_facts"
			}
			lines = append(lines, fmt.Sprintf("Reconcile Ready issue from durable state: %s status=%s pr=%s next=%s reconciliation_needed=true", decision.IssueIdentifier, emptyAsNA(sqliteFactStatus(decision.DBFacts)), emptyAsNA(sqliteFactPRURL(decision.DBFacts)), next))
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
		reconcile := ""
		if decision.ReconciliationNeeded {
			reconcile = " reconciliation_needed=true"
		}
		lines = append(lines, fmt.Sprintf("Reconcile Ready issue with terminal artifact: %s status=%s outcome=%s next=%s attention=%t%s", decision.IssueIdentifier, artifact.Status, emptyAsNA(artifact.Outcome), next, artifact.OperatorAttention, reconcile))
	}
	if len(lines) == 0 {
		return nil
	}
	sort.Strings(lines)
	return append([]string{"Actionable reconciliation:"}, lines...)
}

func sqliteFactStatus(facts *state.ReconciliationFacts) string {
	if facts == nil {
		return ""
	}
	return facts.Status
}

func sqliteFactPRURL(facts *state.ReconciliationFacts) string {
	if facts == nil {
		return ""
	}
	return facts.PRURL
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
		reconcile := ""
		if decision.ReconciliationNeeded {
			reconcile = " reconciliation_needed=true"
		}
		lines = append(lines, fmt.Sprintf("Reconcile In Progress issue with no active run lock: %s artifact_status=%s outcome=%s next=%s%s", decision.IssueIdentifier, status, outcome, next, reconcile))
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
