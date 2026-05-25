package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	artifactio "github.com/weskor/agent-machine/internal/artifacts"
	orchstate "github.com/weskor/agent-machine/internal/state"
)

func mergeConflictReason(pr pullRequestSummary) string {
	if strings.EqualFold(pr.Mergeable, "CONFLICTING") || strings.EqualFold(pr.MergeStateStatus, "DIRTY") {
		return fmt.Sprintf("GitHub reports mergeable=%s mergeStateStatus=%s; branch %s has conflicts with the base branch.", emptyAsUnknown(pr.Mergeable), emptyAsUnknown(pr.MergeStateStatus), pr.HeadRefName)
	}
	return ""
}

func mergeGateBlockReason(pr pullRequestSummary) string {
	return evaluatePullRequestMergeGate(pr).Reason()
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
	github, ctx, cancel, err := githubClientWithContextTimeout(parent, config.Budget.MergeTimeout)
	if err != nil {
		recordMergeErrorContext(parent, store, "", "", 0, err)
		return err
	}
	defer cancel()
	prs, err := github.OpenPullRequests(ctx)
	if err != nil {
		recordMergeErrorContext(ctx, store, "", "", 0, err)
		return fmt.Errorf("GitHub API open PR metadata lookup failed: %w", err)
	}
	prs = symphonyPRs(prs)
	log("found %d Symphony-owned open PR(s)", len(prs))
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
	github, ghCtx, cancel, err := githubClientWithContextTimeout(ctx, config.Budget.MergeTimeout)
	if err != nil {
		recordMergeErrorContext(ctx, store, "", "", 0, err)
		return false, err
	}
	defer cancel()
	prs, err := github.OpenPullRequests(ghCtx)
	if err != nil {
		recordMergeErrorContext(ctx, store, "", "", 0, err)
		return false, fmt.Errorf("GitHub API open PR metadata lookup failed: %w", err)
	}
	didWork := false
	now := time.Now().UTC()
	for _, pr := range symphonyPRs(prs) {
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
	github, ctx, cancel, err := githubClientWithContextTimeout(parent, config.Budget.MergeTimeout)
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
		return true, errors.Join(fmt.Errorf("GitHub API open PR metadata lookup failed: %w", err), completeClaimedMergeWorkerTask(ctx, store, task, "failed", true, "github_open_pr_lookup_failed", err.Error(), startedAt, finishedAt))
	}
	pr, ok := findMergeTaskPullRequest(symphonyPRs(prs), task, taskPayload)
	if !ok {
		finishedAt := time.Now().UTC()
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

func symphonyPRs(prs []pullRequestSummary) []pullRequestSummary {
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

func checksPassed(checks []statusCheck) bool {
	return checksBlockReason(checks) == ""
}

func checksBlockReason(checks []statusCheck) string {
	if len(checks) == 0 {
		return "no status checks were reported by GitHub"
	}
	for _, check := range checks {
		switch check.Typename {
		case "CheckRun":
			if !strings.EqualFold(check.Status, "COMPLETED") || !strings.EqualFold(check.Conclusion, "SUCCESS") {
				return fmt.Sprintf("check run %q is status=%s conclusion=%s", checkLabel(check), emptyAsUnknown(check.Status), emptyAsUnknown(check.Conclusion))
			}
		case "StatusContext":
			if !strings.EqualFold(check.State, "SUCCESS") {
				return fmt.Sprintf("status context %q is state=%s", checkLabel(check), emptyAsUnknown(check.State))
			}
		default:
			return fmt.Sprintf("unknown status check shape %q for %q", emptyAsUnknown(check.Typename), checkLabel(check))
		}
	}
	return ""
}

func checkLabel(check statusCheck) string {
	if strings.TrimSpace(check.Name) != "" {
		return check.Name
	}
	if strings.TrimSpace(check.Context) != "" {
		return check.Context
	}
	return "unnamed"
}

func workspaceLockedOrModified(workspaceRoot, identifier, _ string) (bool, string) {
	workspace := filepath.Join(workspaceRoot, identifier)
	if _, err := os.Stat(workspace); os.IsNotExist(err) {
		return false, ""
	}
	for _, lockPath := range []string{
		filepath.Join(workspace, ".git", "index.lock"),
		filepath.Join(workspace, ".pi-symphony.lock"),
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
		return "missing run artifact for approved Symphony PR"
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
