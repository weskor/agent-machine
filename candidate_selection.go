package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	cfg "github.com/weskor/pi-symphony/internal/config"
	"github.com/weskor/pi-symphony/internal/state"
)

type candidateSelectionOptions struct {
	SkipReviewReadyResumes bool
}

func nextRunnableCandidate(client linearClient, config runnerConfig, store *state.Store) (*issue, *pullRequestSummary, error) {
	return nextRunnableCandidateContext(context.Background(), client, config, store)
}

func nextRunnableCandidateContext(ctx context.Context, client linearClient, config runnerConfig, store *state.Store) (*issue, *pullRequestSummary, error) {
	return nextRunnableCandidateWithOptionsContext(ctx, client, config, store, candidateSelectionOptions{})
}

func nextRunnableCandidateWithOptions(client linearClient, config runnerConfig, store *state.Store, options candidateSelectionOptions) (*issue, *pullRequestSummary, error) {
	return nextRunnableCandidateWithOptionsContext(context.Background(), client, config, store, options)
}

func nextRunnableCandidateWithOptionsContext(ctx context.Context, client linearClient, config runnerConfig, store *state.Store, options candidateSelectionOptions) (*issue, *pullRequestSummary, error) {
	candidates, err := client.candidatesContext(ctx, config.ProjectSlug, config.ActiveStates)
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
		if skipCandidateForSelectionOptionsContext(ctx, config, candidates[i], pr, store, options) {
			continue
		}
		if active, err := hasActiveImplementationWorkerTask(ctx, store, candidates[i].Identifier); err != nil {
			return nil, nil, err
		} else if active {
			log("skipping %s: implementation worker task already queued or claimed", candidates[i].Identifier)
			emitCandidateEventContext(ctx, store, state.EventCandidateSkipped, candidates[i], map[string]any{"reason": "implementation_task_active"})
			continue
		}
		decision := reconcileCandidateForSelectionContext(ctx, config, candidates[i], pr, store)
		retryDecision, retryDecisionFound := retryBackoffDecision(ctx, store, candidates[i], config, time.Now().UTC())
		if store != nil {
			if retryDecisionFound && !retryDecision.runnable {
				log("skipping %s: %s", candidates[i].Identifier, retryDecision.reason)
				emitCandidateEventContext(ctx, store, state.EventCandidateSkipped, candidates[i], map[string]any{"reason": retryDecision.reason})
				continue
			}
		}
		if decision.Lifecycle == lifecycleBlocked && strings.Contains(strings.Join(decision.Blockers, ","), "blocked label") {
			blockedCount++
			log("skipping %s: blocked label", candidates[i].Identifier)
			emitCandidateEventContext(ctx, store, state.EventCandidateSkipped, candidates[i], map[string]any{"reason": "blocked_label", "lifecycle": decision.Lifecycle, "blockers": decision.Blockers, "next_action": decision.NextAction})
			continue
		}
		if candidates[i].State.Name == config.ReadyState && (decision.CanRun || retryBackoffOverridesTerminalBlock(decision, retryDecision, retryDecisionFound)) {
			emitCandidateEventContext(ctx, store, state.EventCandidateSelected, candidates[i], map[string]any{"state": candidates[i].State.Name, "reason": candidateOrderReason(candidates[i], config.ReadyState)})
			return &candidates[i], pr, nil
		}
		log("skipping %s: lifecycle=%s blockers=%s next=%s", candidates[i].Identifier, decision.Lifecycle, strings.Join(decision.Blockers, "; "), decision.NextAction)
		if !decision.CanRun {
			emitCandidateEventContext(ctx, store, state.EventCandidateSkipped, candidates[i], map[string]any{"reason": "not_runnable", "state": candidates[i].State.Name, "lifecycle": decision.Lifecycle, "blockers": decision.Blockers, "next_action": decision.NextAction})
		}
	}
	for i := range candidates {
		pr := prsByIssue[candidates[i].Identifier]
		if skipCandidateForSelectionOptionsContext(ctx, config, candidates[i], pr, store, options) {
			continue
		}
		if active, err := hasActiveImplementationWorkerTask(ctx, store, candidates[i].Identifier); err != nil {
			return nil, nil, err
		} else if active {
			continue
		}
		decision := reconcileCandidateForSelectionContext(ctx, config, candidates[i], pr, store)
		retryDecision, retryDecisionFound := retryBackoffDecision(ctx, store, candidates[i], config, time.Now().UTC())
		if store != nil {
			if retryDecisionFound && !retryDecision.runnable {
				continue
			}
		}
		if decision.Lifecycle == lifecycleBlocked && strings.Contains(strings.Join(decision.Blockers, ","), "blocked label") {
			continue
		}
		if decision.CanRun || retryBackoffOverridesTerminalBlock(decision, retryDecision, retryDecisionFound) {
			emitCandidateEventContext(ctx, store, state.EventCandidateSelected, candidates[i], map[string]any{"state": candidates[i].State.Name, "reason": candidateOrderReason(candidates[i], config.ReadyState)})
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

func hasActiveImplementationWorkerTask(ctx context.Context, store *state.Store, issueKey string) (bool, error) {
	return hasActiveWorkerTask(ctx, store, implementationWorkerRole, issueKey)
}

func hasActiveWorkerTask(ctx context.Context, store *state.Store, role, issueKey string) (bool, error) {
	if store == nil || strings.TrimSpace(issueKey) == "" {
		return false, nil
	}
	tasks, err := store.WorkerTasks(ctx, role)
	if err != nil {
		return false, err
	}
	for _, task := range tasks {
		if task.IssueKey == issueKey && workerTaskBlocksDispatch(task.Status) {
			return true, nil
		}
	}
	return false, nil
}

func workerTaskBlocksDispatch(status string) bool {
	switch status {
	case state.WorkerTaskStatusQueued, state.WorkerTaskStatusClaimed, state.WorkerTaskStatusReconciliationNeeded:
		return true
	default:
		return false
	}
}

func skipCandidateForSelectionOptionsContext(ctx context.Context, config runnerConfig, candidate issue, pr *pullRequestSummary, store *state.Store, options candidateSelectionOptions) bool {
	if !options.SkipReviewReadyResumes || strings.TrimSpace(config.ReviewCommand) == "" || pr == nil {
		return false
	}
	decision := reconcileCandidateForSelectionContext(ctx, config, candidate, pr, store)
	if !decision.CanRun || decision.NextAction != "run_semantic_review_after_checks_ready" {
		return false
	}
	log("skipping %s: review-ready resume is owned by review worker", candidate.Identifier)
	emitCandidateEventContext(ctx, store, state.EventCandidateSkipped, candidate, map[string]any{"reason": "review_ready_resume", "next_action": "run_review_worker"})
	return true
}

func reconcileCandidateForSelection(config runnerConfig, candidate issue, pr *pullRequestSummary, store *state.Store) reconciliationDecision {
	return reconcileCandidateForSelectionContext(context.Background(), config, candidate, pr, store)
}

func reconcileCandidateForSelectionContext(ctx context.Context, config runnerConfig, candidate issue, pr *pullRequestSummary, store *state.Store) reconciliationDecision {
	decision := newReconciliationModule(store).ReconcileIssueContext(ctx, config, candidate, pr)
	return decisionWithRepairableReviewFailedPR(config, candidate, pr, decision)
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
	if facts.RetryNextState == "no_retry" {
		return candidateRetryDecision{runnable: false, reason: "terminal_or_no_retry"}, true
	}
	if facts.RetryNextState != "retry_after_backoff" {
		return candidateRetryDecision{runnable: false, reason: "terminal_or_no_retry"}, true
	}
	backoff := retryBackoffDuration(facts.RetryCount, configuredMaxRetryBackoff(config))
	if backoff <= 0 || facts.RetryDecidedAt.IsZero() || !now.Before(facts.RetryDecidedAt.Add(backoff)) {
		return candidateRetryDecision{runnable: true, reason: "retry_backoff_elapsed"}, true
	}
	return candidateRetryDecision{runnable: false, reason: fmt.Sprintf("retry_backoff_until_%s", facts.RetryDecidedAt.Add(backoff).Format(time.RFC3339))}, true
}

func retryBackoffOverridesTerminalBlock(decision reconciliationDecision, retryDecision candidateRetryDecision, retryDecisionFound bool) bool {
	if !retryDecisionFound || !retryDecision.runnable || decision.PR != nil || decision.RunRecord == nil {
		return false
	}
	if decision.Lifecycle != lifecycleBlocked || decision.NextAction != "operator_repair_or_clear_artifact" {
		return false
	}
	return retryableRunStatus(decision.RunRecord.Status)
}

func configuredMaxRetryBackoff(config runnerConfig) time.Duration {
	proj, err := cfg.ReadProject(config.ConfigPath)
	if err != nil {
		return 0
	}
	schema, err := cfg.ParseConfig(proj.YAML)
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

func emitCandidateEventContext(ctx context.Context, store *state.Store, eventType string, candidate issue, payload map[string]any) {
	if store == nil {
		return
	}
	if payload == nil {
		payload = map[string]any{}
	}
	if _, err := store.AppendEvent(ctx, state.EventInput{OccurredAt: time.Now().UTC(), IssueKey: candidate.Identifier, IssueID: candidate.ID, Attempt: 1, Source: "runner.candidate_selection", Type: eventType, Payload: payload}); err != nil {
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

func feedbackRetryAvailable(workspace string, candidate *issue, record runRecord, config runnerConfig, prArg ...*pullRequestSummary) bool {
	if candidate == nil || candidate.State.Name != config.ReadyState || record.PRURL == "" {
		return false
	}
	var pr *pullRequestSummary
	if len(prArg) > 0 {
		pr = prArg[0]
	}
	if record.Status == runAttemptStatusReviewFailed {
		decision := reconciliationDecision{NextAction: repairReviewFindingsNextAction}
		return repairableReviewFailedPR(config, *candidate, pr, decision)
	}
	if record.Status != "success" {
		return false
	}
	feedback, err := readPRFeedback(workspace)
	return err == nil && strings.TrimSpace(feedback) != ""
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
