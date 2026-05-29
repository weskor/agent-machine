package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	artifactio "github.com/weskor/agent-machine/internal/artifacts"
	mergegate "github.com/weskor/agent-machine/internal/mergegate"
	orchstate "github.com/weskor/agent-machine/internal/state"
	"github.com/weskor/agent-machine/internal/terminalpr"
	"github.com/weskor/agent-machine/internal/workertask"
)

type mergeGateDecision = mergegate.Decision

type mergeWorkerTaskPayload struct {
	Phase       string `json:"phase"`
	IssueKey    string `json:"issue_key"`
	PRNumber    int    `json:"pr_number"`
	PRURL       string `json:"pr_url"`
	HeadRefName string `json:"head_ref_name"`
	BaseRefName string `json:"base_ref_name"`
}

func mergeGateBlockReason(pr pullRequestSummary) string {
	return evaluatePullRequestMergeGate(pr).Reason()
}

func evaluatePullRequestMergeGate(pr pullRequestSummary) mergeGateDecision {
	return mergegate.EvaluatePullRequest(mergegate.PullRequest{
		Subject:          firstNonEmpty(pr.URL, pr.HeadRefName),
		HeadRefName:      pr.HeadRefName,
		Mergeable:        pr.Mergeable,
		MergeStateStatus: pr.MergeStateStatus,
		StatusChecks:     mergegateStatusChecks(pr.StatusCheckRollup),
	})
}

func evaluateRunRecordMergeGate(record runRecord) mergeGateDecision {
	return mergegate.EvaluateRunRecord(mergegate.RunRecord{
		Subject: firstNonEmpty(record.PRURL, record.IssueIdentifier),
		Status:  record.Status,
		Error:   record.Error,
	})
}

func checksPassed(checks []statusCheck) bool {
	return mergegate.ChecksPassed(mergegateStatusChecks(checks))
}

func checksBlockReason(checks []statusCheck) string {
	return mergegate.ChecksBlockReason(mergegateStatusChecks(checks))
}

func checkLabel(check statusCheck) string {
	return mergegate.CheckLabel(mergegateStatusCheck(check))
}

func mergeConflictReason(pr pullRequestSummary) string {
	return mergegate.ConflictReason(mergegate.PullRequest{
		HeadRefName:      pr.HeadRefName,
		Mergeable:        pr.Mergeable,
		MergeStateStatus: pr.MergeStateStatus,
	})
}

func mergegateStatusChecks(checks []statusCheck) []mergegate.StatusCheck {
	out := make([]mergegate.StatusCheck, 0, len(checks))
	for _, check := range checks {
		out = append(out, mergegateStatusCheck(check))
	}
	return out
}

func mergegateStatusCheck(check statusCheck) mergegate.StatusCheck {
	return mergegate.StatusCheck{
		Typename:   check.Typename,
		Status:     check.Status,
		Conclusion: check.Conclusion,
		State:      check.State,
		Name:       check.Name,
		Context:    check.Context,
	}
}

// mergeApprovedPRs is intentionally conservative: it only merges Human Review
// issues when GitHub reports an approval and every reported check is green.
func mergeApprovedPRs(client linearClient, config runnerConfig) error {
	log("mode=merge-approved; project=%s", config.ProjectSlug)
	store, stateDBPath := commandScopedStateStore(context.Background(), config.WorkspaceRoot, "merge-approved")
	if store == nil {
		return fmt.Errorf("SQLite state store unavailable for merge-approved at %s", stateDBPath)
	}
	if store != nil {
		defer store.Close()
	}
	return mergeApprovedPRsWithStore(client, config, store)
}

func mergeApprovedPRsWithStore(client linearClient, config runnerConfig, store *orchstate.Store) error {
	return mergeApprovedPRsWithStoreContext(context.Background(), client, config, store)
}

func mergeApprovedPRsWithStoreContext(parent context.Context, client linearClient, config runnerConfig, store *orchstate.Store) error {
	if err := parent.Err(); err != nil {
		return err
	}
	if store == nil {
		return fmt.Errorf("SQLite state store unavailable for merge at %s", orchstate.DefaultDBPath(config.WorkspaceRoot))
	}
	github, ctx, cancel, err := codeHostClientWithContextTimeout(parent, config, config.Budget.MergeTimeout)
	if err != nil {
		recordMergeErrorContext(parent, store, "", "", 0, err)
		return err
	}
	defer cancel()
	prs, err := github.OpenPullRequests(ctx)
	if err != nil {
		recordMergeErrorContext(ctx, store, "", "", 0, err)
		return fmt.Errorf("code-host API open PR metadata lookup failed: %w", err)
	}
	prs = amPRs(prs)
	log("found %d Agent Machine-owned open PR(s)", len(prs))
	worker := mergeWorker{client: client, config: config, store: store, github: github}
	for _, pr := range prs {
		if err := worker.HandlePullRequest(ctx, pr); err != nil {
			return err
		}
	}
	return nil
}

func scheduleMergeWorkerTasks(ctx context.Context, client linearClient, config runnerConfig, store *orchstate.Store) (bool, error) {
	if store == nil {
		return false, fmt.Errorf("SQLite state store unavailable for merge scheduling at %s", orchstate.DefaultDBPath(config.WorkspaceRoot))
	}
	github, ghCtx, cancel, err := codeHostClientWithContextTimeout(ctx, config, config.Budget.MergeTimeout)
	if err != nil {
		recordMergeErrorContext(ctx, store, "", "", 0, err)
		return false, err
	}
	defer cancel()
	prs, err := github.OpenPullRequests(ghCtx)
	if err != nil {
		recordMergeErrorContext(ctx, store, "", "", 0, err)
		return false, fmt.Errorf("code-host API open PR metadata lookup failed: %w", err)
	}
	didWork := false
	now := time.Now().UTC()
	for _, pr := range amPRs(prs) {
		identifier := issueIdentifierFromBranch(pr.HeadRefName)
		if identifier == "" {
			continue
		}
		candidate, err := client.issueByIdentifierContext(ctx, identifier)
		if err != nil {
			recordMergeErrorContext(ctx, store, identifier, "", pr.Number, err)
			return didWork, err
		}
		if candidate == nil || candidate.State.Name != config.HandoffState {
			continue
		}
		if _, enqueued, err := enqueueMergeWorkerTask(ctx, store, *candidate, pr, now); err != nil {
			return didWork, err
		} else if enqueued {
			didWork = true
		}
	}
	return didWork, nil
}

func mergeWorkerTaskKey(issueKey string, prNumber int) string {
	return fmt.Sprintf("%s:%s:%d", mergeWorkerRole, issueKey, prNumber)
}

func enqueueMergeWorkerTask(ctx context.Context, store *orchstate.Store, candidate issue, pr pullRequestSummary, now time.Time) (orchstate.WorkerTask, bool, error) {
	if store == nil {
		return orchstate.WorkerTask{}, false, nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	taskKey := mergeWorkerTaskKey(candidate.Identifier, pr.Number)
	tasks, err := store.WorkerTasks(ctx, mergeWorkerRole)
	if err != nil {
		return orchstate.WorkerTask{}, false, err
	}
	for _, task := range tasks {
		if task.TaskKey != taskKey {
			continue
		}
		if workertask.BlocksDispatch(task.Status) {
			return task, false, nil
		}
		break
	}
	availableAt, err := workertask.AvailableAtAfterLatestFailure(ctx, store, taskKey, mergeWorkerRole, now)
	if err != nil {
		return orchstate.WorkerTask{}, false, err
	}
	payload, err := json.Marshal(mergeWorkerTaskPayload{
		Phase:       "merge",
		IssueKey:    candidate.Identifier,
		PRNumber:    pr.Number,
		PRURL:       pr.URL,
		HeadRefName: pr.HeadRefName,
		BaseRefName: pr.BaseRefName,
	})
	if err != nil {
		return orchstate.WorkerTask{}, false, fmt.Errorf("encode merge worker task payload: %w", err)
	}
	task := orchstate.WorkerTask{
		TaskKey:     taskKey,
		Role:        mergeWorkerRole,
		IssueKey:    candidate.Identifier,
		IssueID:     candidate.ID,
		Attempt:     1,
		Status:      "queued",
		Priority:    candidate.Priority,
		AvailableAt: availableAt,
		LeaseName:   "worker:merge:" + candidate.Identifier,
		Payload:     payload,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := store.UpsertWorkerTask(ctx, task); err != nil {
		return orchstate.WorkerTask{}, false, err
	}
	recordContinuousWorkerTaskEvent(ctx, store, orchstate.EventWorkerTaskQueued, task, map[string]any{"lane": "merge", "issue_key": candidate.Identifier, "pr_url": pr.URL, "pr_number": pr.Number})
	return task, true, nil
}

func runQueuedMergeWorkerTask(client linearClient, config runnerConfig, store *orchstate.Store) (bool, error) {
	return runQueuedMergeWorkerTaskContext(context.Background(), client, config, store)
}

func runQueuedMergeWorkerTaskContext(parent context.Context, client linearClient, config runnerConfig, store *orchstate.Store) (bool, error) {
	if err := parent.Err(); err != nil {
		return false, err
	}
	if store == nil {
		return false, fmt.Errorf("SQLite state store unavailable for merge worker at %s", orchstate.DefaultDBPath(config.WorkspaceRoot))
	}
	github, ctx, cancel, err := codeHostClientWithContextTimeout(parent, config, config.Budget.MergeTimeout)
	if err != nil {
		recordMergeErrorContext(parent, store, "", "", 0, err)
		return false, err
	}
	defer cancel()
	now := time.Now().UTC()
	task, ok, err := claimNextQueuedMergeWorkerTask(ctx, store, now)
	if err != nil || !ok {
		return false, err
	}
	startedAt := time.Now().UTC()
	taskPayload := mergeWorkerTaskPayload{IssueKey: task.IssueKey}
	if len(task.Payload) > 0 {
		_ = json.Unmarshal(task.Payload, &taskPayload)
	}
	prs, err := github.OpenPullRequests(ctx)
	if err != nil {
		finishedAt := time.Now().UTC()
		recordMergeErrorContext(ctx, store, task.IssueKey, task.IssueID, taskPayload.PRNumber, err)
		return true, errors.Join(fmt.Errorf("code-host API open PR metadata lookup failed: %w", err), completeClaimedMergeWorkerTask(ctx, store, task, "failed", true, "codehost_open_pr_lookup_failed", err.Error(), startedAt, finishedAt))
	}
	pr, ok := findMergeTaskPullRequest(amPRs(prs), task, taskPayload)
	if !ok {
		finishedAt := time.Now().UTC()
		handled, reason, handleErr := handleAlreadyMergedMissingMergeTaskPR(ctx, client, config, store, github, task, taskPayload)
		if handleErr != nil {
			recordMergeErrorContext(ctx, store, task.IssueKey, task.IssueID, taskPayload.PRNumber, handleErr)
			return true, errors.Join(handleErr, completeClaimedMergeWorkerTask(ctx, store, task, "failed", true, "already_merged_convergence_failed", handleErr.Error(), startedAt, finishedAt))
		}
		if handled {
			return true, completeClaimedMergeWorkerTask(ctx, store, task, "completed", true, reason, "", startedAt, finishedAt)
		}
		return true, completeClaimedMergeWorkerTask(ctx, store, task, "completed", true, "pull_request_not_open", "", startedAt, finishedAt)
	}
	worker := mergeWorker{client: client, config: config, store: store, github: github}
	if err := worker.HandlePullRequest(ctx, pr); err != nil {
		finishedAt := time.Now().UTC()
		return true, errors.Join(err, completeClaimedMergeWorkerTask(ctx, store, task, "failed", true, "merge_worker_error", err.Error(), startedAt, finishedAt))
	}
	finishedAt := time.Now().UTC()
	return true, completeClaimedMergeWorkerTask(ctx, store, task, "completed", true, "merge_task_processed", "", startedAt, finishedAt)
}

func claimNextQueuedMergeWorkerTask(ctx context.Context, store *orchstate.Store, now time.Time) (orchstate.WorkerTask, bool, error) {
	if store == nil {
		return orchstate.WorkerTask{}, false, nil
	}
	claimed, ok, err := store.ClaimNextWorkerTask(ctx, mergeWorkerRole, now)
	if err != nil || !ok {
		return claimed, ok, err
	}
	if claimed.TaskKey == "continuous:merge" || claimed.TaskKey == "process:merge" {
		if err := store.CompleteWorkerTask(ctx, claimed.TaskKey, "completed", now); err != nil {
			return orchstate.WorkerTask{}, false, err
		}
		return orchstate.WorkerTask{}, false, nil
	}
	recordContinuousWorkerTaskEvent(ctx, store, orchstate.EventWorkerTaskClaimed, claimed, map[string]any{"lane": "merge", "issue_key": claimed.IssueKey})
	return claimed, true, nil
}

func findMergeTaskPullRequest(prs []pullRequestSummary, task orchstate.WorkerTask, payload mergeWorkerTaskPayload) (pullRequestSummary, bool) {
	for _, pr := range prs {
		if payload.PRNumber > 0 && pr.Number == payload.PRNumber {
			return pr, true
		}
		if payload.PRURL != "" && pr.URL == payload.PRURL {
			return pr, true
		}
		if task.IssueKey != "" && issueIdentifierFromBranch(pr.HeadRefName) == task.IssueKey {
			return pr, true
		}
	}
	return pullRequestSummary{}, false
}

func handleAlreadyMergedMissingMergeTaskPR(ctx context.Context, client linearClient, config runnerConfig, store *orchstate.Store, github githubAPI, task orchstate.WorkerTask, payload mergeWorkerTaskPayload) (bool, string, error) {
	prURL := strings.TrimSpace(payload.PRURL)
	if prURL == "" {
		return false, "", nil
	}
	facts, merged, err := terminalpr.MergedFacts(ctx, github, prURL)
	if err != nil {
		log("queued merge task %s PR state lookup failed after PR disappeared from open list: %v", task.TaskKey, err)
		return false, "", nil
	}
	if !merged {
		return false, "", nil
	}
	issueKey := firstNonEmpty(payload.IssueKey, task.IssueKey, issueIdentifierFromBranch(payload.HeadRefName))
	if issueKey == "" {
		return true, terminalpr.ReasonAlreadyMerged, fmt.Errorf("queued merge task %s has merged PR %s but no issue key", task.TaskKey, prURL)
	}
	candidate, err := client.issueByIdentifierContext(ctx, issueKey)
	if err != nil {
		return true, terminalpr.ReasonAlreadyMerged, err
	}
	if candidate == nil {
		return true, terminalpr.ReasonAlreadyMerged, fmt.Errorf("issue %s not found while converging already-merged PR %s", issueKey, prURL)
	}
	states, err := client.workflowStatesContext(ctx, candidate.Team.ID)
	if err != nil {
		return true, terminalpr.ReasonAlreadyMerged, err
	}
	linearStatus := newLinearStatusWorker(client, candidate, states)
	prNumber := firstNonZero(payload.PRNumber, taskPRNumberFromKey(task.TaskKey))
	recordMergeEventContext(ctx, store, orchstate.EventMergeSucceeded, candidate.Identifier, candidate.ID, prNumber, map[string]any{"pr_url": facts.PRURL, "head_ref": payload.HeadRefName, "state": facts.State, "result": "already_merged"})
	if candidate.State.Name != config.DoneState && stateID(states, config.DoneState) != "" {
		recordMergeEventContext(ctx, store, orchstate.EventLinearDoneTransitionAttempted, candidate.Identifier, candidate.ID, prNumber, map[string]any{"pr_url": facts.PRURL, "done_state": config.DoneState})
		if _, err := linearStatus.MoveToContext(ctx, config.DoneState); err != nil {
			recordMergeEventContext(ctx, store, orchstate.EventLinearDoneTransitionFinished, candidate.Identifier, candidate.ID, prNumber, map[string]any{"pr_url": facts.PRURL, "done_state": config.DoneState, "result": "failed", "error": err.Error()})
			return true, terminalpr.ReasonAlreadyMerged, err
		}
		recordMergeEventContext(ctx, store, orchstate.EventLinearDoneTransitionFinished, candidate.Identifier, candidate.ID, prNumber, map[string]any{"pr_url": facts.PRURL, "done_state": config.DoneState, "result": "success"})
	}
	if err := removeDoneWorkspaceContext(ctx, config.WorkspaceRoot, candidate.Identifier); err != nil {
		recordMergeEventContext(ctx, store, orchstate.EventMergeFailed, candidate.Identifier, candidate.ID, prNumber, map[string]any{"pr_url": facts.PRURL, "phase": "workspace_cleanup", "error": err.Error()})
		return true, terminalpr.ReasonAlreadyMerged, err
	}
	recordMergeEventContext(ctx, store, orchstate.EventMergeCompleted, candidate.Identifier, candidate.ID, prNumber, map[string]any{"pr_url": facts.PRURL, "head_ref": payload.HeadRefName, "done_state": config.DoneState, "result": "already_merged"})
	_ = linearStatus.CommentContext(ctx, fmt.Sprintf("PR was already merged; converged Agent Machine state and cleaned workspace: %s", facts.PRURL))
	log("converged already-merged PR %s for %s; moved to %s", facts.PRURL, candidate.Identifier, config.DoneState)
	return true, terminalpr.ReasonAlreadyMerged, nil
}

func completeClaimedMergeWorkerTask(ctx context.Context, store *orchstate.Store, task orchstate.WorkerTask, status string, didWork bool, reason, errorText string, startedAt, finishedAt time.Time) error {
	if store == nil || task.TaskKey == "" {
		return nil
	}
	if status == "" {
		status = "completed"
	}
	if reason == "" {
		reason = "merge_task_completed"
	}
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	if finishedAt.IsZero() {
		finishedAt = time.Now().UTC()
	}
	completeErr := store.CompleteWorkerTask(ctx, task.TaskKey, status, finishedAt)
	payload := map[string]any{
		"lane":      "merge",
		"task_key":  task.TaskKey,
		"role":      mergeWorkerRole,
		"status":    status,
		"reason":    reason,
		"did_work":  didWork,
		"issue_key": task.IssueKey,
	}
	if errorText != "" {
		payload["error"] = errorText
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode merge worker result payload: %w", err)
	}
	resultErr := store.UpsertWorkerResult(ctx, orchstate.WorkerResult{
		TaskKey:    task.TaskKey,
		Role:       mergeWorkerRole,
		LaneName:   "merge",
		IssueKey:   task.IssueKey,
		IssueID:    task.IssueID,
		Attempt:    task.Attempt,
		Status:     status,
		DidWork:    didWork,
		Reason:     reason,
		Error:      errorText,
		Payload:    payloadJSON,
		StartedAt:  startedAt,
		FinishedAt: finishedAt,
		UpdatedAt:  finishedAt,
	})
	eventType := orchstate.EventWorkerTaskCompleted
	if status == "failed" {
		eventType = orchstate.EventWorkerTaskFailed
	}
	task.Status = status
	task.UpdatedAt = finishedAt
	recordContinuousWorkerTaskEvent(ctx, store, eventType, task, payload)
	return errors.Join(completeErr, resultErr)
}

func firstNonZero(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func taskPRNumberFromKey(taskKey string) int {
	parts := strings.Split(taskKey, ":")
	if len(parts) == 0 {
		return 0
	}
	prNumber, _ := strconv.Atoi(parts[len(parts)-1])
	return prNumber
}

func recordMergeEventContext(ctx context.Context, store *orchstate.Store, eventType, issueKey, issueID string, prNumber int, payload map[string]any) {
	if store == nil {
		return
	}
	if payload == nil {
		payload = map[string]any{}
	}
	if prNumber > 0 {
		payload["pr_number"] = prNumber
	}
	if _, err := store.AppendEvent(ctx, orchstate.EventInput{OccurredAt: time.Now().UTC(), IssueKey: issueKey, IssueID: issueID, Source: "merge-lane", Type: eventType, Payload: payload}); err != nil {
		log("skipping sqlite merge event %s: %v", eventType, err)
	}
}

func recordMergeErrorContext(ctx context.Context, store *orchstate.Store, issueKey, issueID string, prNumber int, err error) {
	if err == nil {
		return
	}
	recordMergeEventContext(ctx, store, orchstate.EventErrorRecorded, issueKey, issueID, prNumber, map[string]any{"error": err.Error(), "lane": "merge"})
}

func feedbackAlreadyAddressed(store *orchstate.Store, issueKey, prURL, feedback string) bool {
	return feedbackAlreadyAddressedContext(context.Background(), store, issueKey, prURL, feedback)
}

func feedbackAlreadyAddressedContext(ctx context.Context, store *orchstate.Store, issueKey, prURL, feedback string) bool {
	if store == nil {
		return false
	}
	currentHash := feedbackHash(feedback)
	if currentHash == "" {
		return false
	}
	facts, ok, err := store.ReconciliationFacts(ctx, issueKey)
	if err != nil || !ok {
		return false
	}
	return facts.Status == runAttemptStatusSuccess &&
		facts.ReviewStatus == "passed" &&
		facts.PRURL == prURL &&
		facts.FeedbackHash == currentHash
}

func renderPRConflictFeedback(pr pullRequestSummary, reason string) string {
	return fmt.Sprintf(`# PR #%d merge conflict feedback

## Blocked merge reason

%s

## Repair instructions

- Update this PR branch from the configured base branch.
- Resolve merge conflicts without starting unrelated work.
- Rerun the validation expected by the Linear issue.
- Push the same PR branch and stop for Human Review again.

PR: %s
Branch: %s
`, pr.Number, reason, pr.URL, pr.HeadRefName)
}

func emptyAsUnknown(value string) string {
	if strings.TrimSpace(value) == "" {
		return "UNKNOWN"
	}
	return value
}

func amPRs(prs []pullRequestSummary) []pullRequestSummary {
	filtered := make([]pullRequestSummary, 0, len(prs))
	for _, pr := range prs {
		if issueIdentifierFromBranch(pr.HeadRefName) == "" {
			continue
		}
		filtered = append(filtered, pr)
	}
	return filtered
}

func issueIdentifierFromBranch(branch string) string {
	match := regexp.MustCompile(`(?i)(CAG[-_][0-9]+)`).FindString(branch)
	return strings.ToUpper(strings.ReplaceAll(match, "_", "-"))
}

func workspaceLockedOrModified(workspaceRoot, identifier, _ string) (bool, string) {
	workspace := filepath.Join(workspaceRoot, identifier)
	if _, err := os.Stat(workspace); os.IsNotExist(err) {
		return false, ""
	}
	for _, lockPath := range []string{
		filepath.Join(workspace, ".git", "index.lock"),
		filepath.Join(workspace, ".am.lock"),
	} {
		if _, err := os.Stat(lockPath); err == nil {
			return true, fmt.Sprintf("workspace %s is locked", workspace)
		}
	}
	changed, err := workspaceHasChanges(workspace)
	if err != nil {
		return true, fmt.Sprintf("workspace %s status could not be checked", workspace)
	}
	if changed {
		return true, fmt.Sprintf("workspace %s has uncommitted changes", workspace)
	}
	return false, ""
}

func runArtifactMergeBlockReason(workspaceRoot, identifier, prURL string) string {
	record, ok := readRunArtifact(filepath.Join(workspaceRoot, identifier))
	if !ok {
		return "missing run artifact for approved Agent Machine PR"
	}
	if strings.TrimSpace(record.PRURL) != "" && strings.TrimSpace(prURL) != "" && strings.TrimSpace(record.PRURL) != strings.TrimSpace(prURL) {
		return fmt.Sprintf("run artifact PR URL %s does not match candidate PR %s", record.PRURL, prURL)
	}
	if record.Status != "success" {
		return fmt.Sprintf("run status is %s", emptyAsUnknown(record.Status))
	}
	if runArtifactHasHumanApprovedMissingEvidence(record) {
		return ""
	}
	if record.ReviewStatus != "passed" {
		return fmt.Sprintf("review status is %s", emptyAsUnknown(record.ReviewStatus))
	}
	return ""
}

func runArtifactHasHumanApprovedMissingEvidence(record runRecord) bool {
	return record.Status == "success" &&
		record.ReviewStatus == "failed" &&
		record.ReviewClassification == reviewClassificationMissingEvidenceOnly
}

func readRunArtifact(workspace string) (runRecord, bool) {
	data, err := os.ReadFile(filepath.Join(workspace, artifactio.RunRecordName))
	if err != nil {
		return runRecord{}, false
	}
	if _, _, err := artifactio.ValidateArtifactSchema(data, artifactio.RunRecordName); err != nil {
		return runRecord{}, false
	}
	var record runRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return runRecord{}, false
	}
	return record, true
}
