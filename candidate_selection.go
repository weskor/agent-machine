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

	cfg "github.com/weskor/pi-symphony/internal/config"
	"github.com/weskor/pi-symphony/internal/state"
)

func nextRunnableCandidate(client linearClient, config runnerConfig, store *state.Store) (*issue, *pullRequestSummary, error) {
	candidates, err := client.candidates(config.ProjectSlug, config.ActiveStates)
	if err != nil || len(candidates) == 0 {
		return nil, nil, err
	}
	prsByIssue, err := openPRsByIssueForSelection(config)
	if err != nil {
		return nil, nil, err
	}
	candidates = orderCandidates(candidates, config.ReadyState)
	blockedCount := 0
	for i := range candidates {
		pr := prsByIssue[candidates[i].Identifier]
		decision := reconcileIssue(config, candidates[i], pr)
		if store != nil {
			if retryDecision, ok := retryBackoffDecision(context.Background(), store, candidates[i], config, time.Now().UTC()); ok && !retryDecision.runnable {
				log("skipping %s: %s", candidates[i].Identifier, retryDecision.reason)
				emitCandidateEvent(store, state.EventCandidateSkipped, candidates[i], map[string]any{"reason": retryDecision.reason})
				continue
			}
		}
		if decision.Lifecycle == lifecycleBlocked && strings.Contains(strings.Join(decision.Blockers, ","), "blocked label") {
			blockedCount++
			log("skipping %s: blocked label", candidates[i].Identifier)
			emitCandidateEvent(store, state.EventCandidateSkipped, candidates[i], map[string]any{"reason": "blocked_label", "lifecycle": decision.Lifecycle, "blockers": decision.Blockers, "next_action": decision.NextAction})
			continue
		}
		if candidates[i].State.Name == config.ReadyState && decision.CanRun {
			emitCandidateEvent(store, state.EventCandidateSelected, candidates[i], map[string]any{"state": candidates[i].State.Name, "reason": candidateOrderReason(candidates[i], config.ReadyState)})
			return &candidates[i], pr, nil
		}
		log("skipping %s: lifecycle=%s blockers=%s next=%s", candidates[i].Identifier, decision.Lifecycle, strings.Join(decision.Blockers, "; "), decision.NextAction)
		if !decision.CanRun {
			emitCandidateEvent(store, state.EventCandidateSkipped, candidates[i], map[string]any{"reason": "not_runnable", "state": candidates[i].State.Name, "lifecycle": decision.Lifecycle, "blockers": decision.Blockers, "next_action": decision.NextAction})
		}
	}
	for i := range candidates {
		pr := prsByIssue[candidates[i].Identifier]
		decision := reconcileIssue(config, candidates[i], pr)
		if store != nil {
			if retryDecision, ok := retryBackoffDecision(context.Background(), store, candidates[i], config, time.Now().UTC()); ok && !retryDecision.runnable {
				continue
			}
		}
		if decision.Lifecycle == lifecycleBlocked && strings.Contains(strings.Join(decision.Blockers, ","), "blocked label") {
			continue
		}
		if decision.CanRun {
			emitCandidateEvent(store, state.EventCandidateSelected, candidates[i], map[string]any{"state": candidates[i].State.Name, "reason": candidateOrderReason(candidates[i], config.ReadyState)})
			return &candidates[i], pr, nil
		}
	}
	if blockedCount == len(candidates) {
		log("all eligible issues are blocked by labels")
		return nil, nil, nil
	}
	log("all eligible issues are waiting on prior review-failure findings, terminal run artifacts, or active locks")
	return nil, nil, nil
}

type candidateRetryDecision struct {
	runnable bool
	reason   string
}

func retryBackoffDecision(ctx context.Context, store *state.Store, candidate issue, config runnerConfig, now time.Time) (candidateRetryDecision, bool) {
	if store == nil {
		return candidateRetryDecision{}, false
	}
	if candidate.State.Name == config.NeedsInfoState || candidate.State.Name == config.DoneState {
		return candidateRetryDecision{runnable: false, reason: "terminal_or_needs_info_state"}, true
	}
	facts, ok, err := store.ReconciliationFacts(ctx, candidate.Identifier)
	if err != nil || !ok || facts.RetryNextState == "" {
		if err != nil {
			log("retry reconciliation unavailable for %s: %v", candidate.Identifier, err)
		}
		return candidateRetryDecision{}, false
	}
	if facts.TerminalOutcome != "" || facts.RetryNextState == "no_retry" {
		return candidateRetryDecision{runnable: false, reason: "terminal_or_no_retry"}, true
	}
	backoff := retryBackoffDuration(facts.RetryCount, configuredMaxRetryBackoff(config))
	if backoff <= 0 || facts.RetryDecidedAt.IsZero() || !now.Before(facts.RetryDecidedAt.Add(backoff)) {
		return candidateRetryDecision{runnable: true, reason: "retry_backoff_elapsed"}, true
	}
	return candidateRetryDecision{runnable: false, reason: fmt.Sprintf("retry_backoff_until_%s", facts.RetryDecidedAt.Add(backoff).Format(time.RFC3339))}, true
}

func configuredMaxRetryBackoff(config runnerConfig) time.Duration {
	wf, err := cfg.ReadWorkflow(config.WorkflowPath)
	if err != nil {
		return 0
	}
	schema, err := cfg.ParseConfig(wf.YAML)
	if err != nil {
		return 0
	}
	return schema.Agent.MaxRetryBackoff
}

func retryBackoffDuration(retryCount int, max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	if retryCount < 1 {
		retryCount = 1
	}
	d := time.Second
	for i := 1; i < retryCount; i++ {
		d *= 2
		if d >= max {
			return max
		}
	}
	if d > max {
		return max
	}
	return d
}

func emitCandidateEvent(store *state.Store, eventType string, candidate issue, payload map[string]any) {
	if store == nil {
		return
	}
	if payload == nil {
		payload = map[string]any{}
	}
	if _, err := store.AppendEvent(context.Background(), state.EventInput{OccurredAt: time.Now().UTC(), IssueKey: candidate.Identifier, IssueID: candidate.ID, Attempt: 1, Source: "runner.candidate_selection", Type: eventType, Payload: payload}); err != nil {
		log("failed to append orchestration event %s for %s: %v", eventType, candidate.Identifier, err)
	}
}

func openPRsByIssue(config runnerConfig) (map[string]*pullRequestSummary, error) {
	github, ctx, cancel, err := githubClientWithTimeout(config.Budget.MergeTimeout)
	if err != nil {
		return nil, err
	}
	defer cancel()
	prs, err := github.OpenPullRequests(ctx)
	if err != nil {
		return nil, fmt.Errorf("GitHub API open PR metadata lookup failed: %w", err)
	}
	prs = symphonyPRs(prs)
	byIssue := map[string]*pullRequestSummary{}
	for i := range prs {
		identifier := issueIdentifierFromBranch(prs[i].HeadRefName)
		if identifier == "" {
			continue
		}
		copy := prs[i]
		byIssue[identifier] = &copy
	}
	return byIssue, nil
}

func isRunnableWorkspace(workspaceRoot, identifier string) bool {
	return isRunnableWorkspaceForCandidate(workspaceRoot, issue{Identifier: identifier}, runnerConfig{ReadyState: "Ready for Agent"})
}

func isRunnableWorkspaceForCandidate(workspaceRoot string, candidate issue, config runnerConfig) bool {
	workspace := filepath.Join(workspaceRoot, candidate.Identifier)
	if hasRunLock(workspace) {
		log("skipping %s because an active run lock exists", candidate.Identifier)
		return false
	}
	if hasUnresolvedReviewFailure(workspaceRoot, candidate.Identifier) {
		return false
	}
	if record, ok := reusableRunRecord(workspace); ok {
		if feedbackRetryAvailable(workspace, &candidate, record, config) {
			log("%s has terminal artifact but captured PR feedback is available; allowing retry", candidate.Identifier)
			return true
		}
		log("skipping %s because a terminal run artifact already exists", candidate.Identifier)
		return false
	}
	return true
}

func feedbackRetryAvailable(workspace string, candidate *issue, record runRecord, config runnerConfig) bool {
	if candidate == nil || candidate.State.Name != config.ReadyState || record.Status != "success" || record.PRURL == "" {
		return false
	}
	feedback, err := readPRFeedback(workspace)
	return err == nil && strings.TrimSpace(feedback) != ""
}

func reusableRunRecord(workspace string) (runRecord, bool) {
	data, err := os.ReadFile(filepath.Join(workspace, ".pi-symphony-run.json"))
	if err != nil {
		return runRecord{}, false
	}
	var record runRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return runRecord{}, false
	}
	if record.Status == "success" && record.PRURL != "" {
		return record, true
	}
	return runRecord{}, false
}

func orderCandidates(candidates []issue, readyState string) []issue {
	ordered := append([]issue(nil), candidates...)
	sort.SliceStable(ordered, func(i, j int) bool {
		left := candidateSortKey(ordered[i], readyState)
		right := candidateSortKey(ordered[j], readyState)
		if left.stateRank != right.stateRank {
			return left.stateRank < right.stateRank
		}
		if left.safetyRank != right.safetyRank {
			return left.safetyRank < right.safetyRank
		}
		if left.priorityRank != right.priorityRank {
			return left.priorityRank < right.priorityRank
		}
		if !left.createdAt.Equal(right.createdAt) {
			return left.createdAt.Before(right.createdAt)
		}
		return ordered[i].Identifier < ordered[j].Identifier
	})
	return ordered
}

type candidateKey struct {
	stateRank    int
	safetyRank   int
	priorityRank int
	createdAt    time.Time
}

func candidateSortKey(candidate issue, readyState string) candidateKey {
	stateRank := 1
	if candidate.State.Name == readyState {
		stateRank = 0
	}
	priorityRank := candidate.Priority
	if priorityRank <= 0 {
		priorityRank = 99
	}
	createdAt, err := time.Parse(time.RFC3339, candidate.CreatedAt)
	if err != nil {
		createdAt = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return candidateKey{stateRank: stateRank, safetyRank: safetyRank(candidate), priorityRank: priorityRank, createdAt: createdAt}
}

func safetyRank(candidate issue) int {
	labels := labelSet(candidate)
	if labels["runner-safety"] || labels["harness"] {
		return 0
	}
	if labels["docs-only"] || labels["low-risk"] {
		return 1
	}
	return 2
}

func isBlockedCandidate(candidate issue) bool {
	labels := labelSet(candidate)
	return labels["blocked"] || labels["needs-info"]
}

func labelSet(candidate issue) map[string]bool {
	out := map[string]bool{}
	for _, label := range candidate.Labels.Nodes {
		out[strings.ToLower(strings.TrimSpace(label.Name))] = true
	}
	return out
}

func candidateOrderReason(candidate issue, readyState string) string {
	key := candidateSortKey(candidate, readyState)
	priority := "none"
	if candidate.Priority > 0 {
		priority = fmt.Sprintf("P%d", candidate.Priority)
	}
	labels := make([]string, 0, len(candidate.Labels.Nodes))
	for _, label := range candidate.Labels.Nodes {
		labels = append(labels, label.Name)
	}
	if len(labels) == 0 {
		labels = append(labels, "none")
	}
	return fmt.Sprintf("state_rank=%d safety_rank=%d priority=%s created_at=%s labels=%s", key.stateRank, key.safetyRank, priority, candidate.CreatedAt, strings.Join(labels, ","))
}
