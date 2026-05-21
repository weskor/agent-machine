package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	orchstate "github.com/weskor/pi-symphony/internal/state"
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
	store, _ := commandScopedStateStore(context.Background(), config.WorkspaceRoot, "merge-approved")
	if store != nil {
		defer store.Close()
	}
	github, ctx, cancel, err := githubClientWithTimeout(config.Budget.MergeTimeout)
	if err != nil {
		recordMergeError(store, "", "", 0, err)
		return err
	}
	defer cancel()
	prs, err := github.OpenPullRequests(ctx)
	if err != nil {
		recordMergeError(store, "", "", 0, err)
		return fmt.Errorf("GitHub API open PR metadata lookup failed: %w", err)
	}
	prs = symphonyPRs(prs)
	log("found %d Symphony-owned open PR(s)", len(prs))
	for _, pr := range prs {
		identifier := issueIdentifierFromBranch(pr.HeadRefName)
		candidate, err := client.issueByIdentifier(identifier)
		if err != nil {
			recordMergeError(store, identifier, "", pr.Number, err)
			return err
		}
		if candidate == nil || candidate.State.Name != config.HandoffState {
			continue
		}

		states, err := client.workflowStates(candidate.Team.ID)
		if err != nil {
			recordMergeError(store, candidate.Identifier, candidate.ID, pr.Number, err)
			return err
		}
		gate := evaluatePullRequestMergeGate(pr)
		if hasString(gate.Codes(), "merge_conflict") {
			reason := gate.Reason()
			recordMergeEvent(store, orchstate.EventMergeBlocked, candidate.Identifier, candidate.ID, pr.Number, map[string]any{"pr_url": pr.URL, "reason": reason, "codes": gate.Codes()})
			workspace := filepath.Join(config.WorkspaceRoot, candidate.Identifier)
			if err := os.MkdirAll(workspace, 0o755); err != nil {
				return err
			}
			if err := writePRFeedback(workspace, pr.Number, renderPRConflictFeedback(pr, reason)); err != nil {
				return err
			}
			if id := stateID(states, config.ReadyState); id != "" {
				if err := client.updateIssueState(candidate.ID, id); err != nil {
					return err
				}
			}
			_ = client.createComment(candidate.ID, fmt.Sprintf("PR merge blocked by conflicts; captured repair instructions and moved back to %s for pickup: %s", config.ReadyState, pr.URL))
			log("blocked merge for %s: %s", candidate.Identifier, reason)
			continue
		}
		decision := reconcileIssue(config, *candidate, &pr)
		if decision.ShouldQuarantine && len(decision.Blockers) > 0 {
			recordMergeEvent(store, orchstate.EventMergeBlocked, candidate.Identifier, candidate.ID, pr.Number, map[string]any{"pr_url": pr.URL, "reason": strings.Join(decision.Blockers, "; "), "next_action": decision.NextAction})
			_ = client.createComment(candidate.ID, fmt.Sprintf("Symphony PR blocked by reconciliation invariant; next=%s; reason: %s", decision.NextAction, strings.Join(decision.Blockers, "; ")))
			log("%s quarantined: %s", pr.URL, strings.Join(decision.Blockers, "; "))
			continue
		}
		switch pr.ReviewDecision {
		case "APPROVED":
			if !decision.CanMerge {
				recordMergeEvent(store, orchstate.EventMergeBlocked, candidate.Identifier, candidate.ID, pr.Number, map[string]any{"pr_url": pr.URL, "reason": strings.Join(decision.Blockers, "; "), "lifecycle": decision.Lifecycle, "next_action": decision.NextAction})
				log("%s approved but merge is blocked: lifecycle=%s blockers=%s next=%s", pr.URL, decision.Lifecycle, strings.Join(decision.Blockers, "; "), decision.NextAction)
				continue
			}
			recordMergeEvent(store, orchstate.EventMergeAttempted, candidate.Identifier, candidate.ID, pr.Number, map[string]any{"pr_url": pr.URL, "head_ref": pr.HeadRefName, "base_ref": pr.BaseRefName})
			if err := github.SquashMergePullRequest(ctx, pr.Number); err != nil {
				recordMergeEvent(store, orchstate.EventMergeFailed, candidate.Identifier, candidate.ID, pr.Number, map[string]any{"pr_url": pr.URL, "phase": "squash_merge", "error": err.Error()})
				recordMergeError(store, candidate.Identifier, candidate.ID, pr.Number, err)
				return fmt.Errorf("GitHub API squash merge failed for PR #%d: %w", pr.Number, err)
			}
			recordMergeEvent(store, orchstate.EventMergeSucceeded, candidate.Identifier, candidate.ID, pr.Number, map[string]any{"pr_url": pr.URL, "head_ref": pr.HeadRefName})
			recordMergeEvent(store, orchstate.EventBranchDeletionAttempted, candidate.Identifier, candidate.ID, pr.Number, map[string]any{"pr_url": pr.URL, "head_ref": pr.HeadRefName})
			if err := github.DeleteBranch(ctx, pr.HeadRefName); err != nil {
				recordMergeEvent(store, orchstate.EventBranchDeletionFinished, candidate.Identifier, candidate.ID, pr.Number, map[string]any{"pr_url": pr.URL, "head_ref": pr.HeadRefName, "result": "failed", "error": err.Error()})
				recordMergeEvent(store, orchstate.EventMergeFailed, candidate.Identifier, candidate.ID, pr.Number, map[string]any{"pr_url": pr.URL, "phase": "branch_deletion", "error": err.Error()})
				recordMergeError(store, candidate.Identifier, candidate.ID, pr.Number, err)
				return fmt.Errorf("GitHub API branch deletion failed for %s after merged PR #%d: %w", pr.HeadRefName, pr.Number, err)
			}
			recordMergeEvent(store, orchstate.EventBranchDeletionFinished, candidate.Identifier, candidate.ID, pr.Number, map[string]any{"pr_url": pr.URL, "head_ref": pr.HeadRefName, "result": "success"})
			if id := stateID(states, config.DoneState); id != "" {
				recordMergeEvent(store, orchstate.EventLinearDoneTransitionAttempted, candidate.Identifier, candidate.ID, pr.Number, map[string]any{"pr_url": pr.URL, "done_state": config.DoneState})
				if err := client.updateIssueState(candidate.ID, id); err != nil {
					recordMergeEvent(store, orchstate.EventLinearDoneTransitionFinished, candidate.Identifier, candidate.ID, pr.Number, map[string]any{"pr_url": pr.URL, "done_state": config.DoneState, "result": "failed", "error": err.Error()})
					recordMergeEvent(store, orchstate.EventMergeFailed, candidate.Identifier, candidate.ID, pr.Number, map[string]any{"pr_url": pr.URL, "phase": "linear_done_transition", "error": err.Error()})
					recordMergeError(store, candidate.Identifier, candidate.ID, pr.Number, err)
					return err
				}
				recordMergeEvent(store, orchstate.EventLinearDoneTransitionFinished, candidate.Identifier, candidate.ID, pr.Number, map[string]any{"pr_url": pr.URL, "done_state": config.DoneState, "result": "success"})
			}
			if err := removeDoneWorkspace(config.WorkspaceRoot, candidate.Identifier); err != nil {
				recordMergeEvent(store, orchstate.EventMergeFailed, candidate.Identifier, candidate.ID, pr.Number, map[string]any{"pr_url": pr.URL, "phase": "workspace_cleanup", "error": err.Error()})
				recordMergeError(store, candidate.Identifier, candidate.ID, pr.Number, err)
				return err
			}
			recordMergeEvent(store, orchstate.EventMergeCompleted, candidate.Identifier, candidate.ID, pr.Number, map[string]any{"pr_url": pr.URL, "head_ref": pr.HeadRefName, "done_state": config.DoneState})
			_ = client.createComment(candidate.ID, fmt.Sprintf("Merged approved PR: %s", pr.URL))
			log("merged %s and moved %s to %s", pr.URL, candidate.Identifier, config.DoneState)
		case "CHANGES_REQUESTED":
			feedback, err := collectPRFeedback(pr.Number)
			if err != nil {
				return err
			}
			workspace := filepath.Join(config.WorkspaceRoot, candidate.Identifier)
			if err := os.MkdirAll(workspace, 0o755); err != nil {
				return err
			}
			if feedbackAlreadyAddressed(workspace, pr.URL, feedback) {
				log("%s has CHANGES_REQUESTED but feedback was already addressed; waiting for human approval", pr.URL)
				continue
			}
			if err := writePRFeedback(workspace, pr.Number, feedback); err != nil {
				return err
			}
			if id := stateID(states, config.ReadyState); id != "" {
				if err := client.updateIssueState(candidate.ID, id); err != nil {
					return err
				}
			}
			_ = client.createComment(candidate.ID, fmt.Sprintf("PR changes requested; captured GitHub review feedback and moved back to %s for pickup: %s", config.ReadyState, pr.URL))
			log("moved %s back to %s after requested changes; feedback captured", candidate.Identifier, config.ReadyState)
		default:
			log("%s waiting for approval; reviewDecision=%s", pr.URL, pr.ReviewDecision)
		}
	}
	return nil
}

func recordMergeEvent(store *orchstate.Store, eventType, issueKey, issueID string, prNumber int, payload map[string]any) {
	if store == nil {
		return
	}
	if payload == nil {
		payload = map[string]any{}
	}
	if prNumber > 0 {
		payload["pr_number"] = prNumber
	}
	if _, err := store.AppendEvent(context.Background(), orchstate.EventInput{OccurredAt: time.Now().UTC(), IssueKey: issueKey, IssueID: issueID, Source: "merge-lane", Type: eventType, Payload: payload}); err != nil {
		log("skipping sqlite merge event %s: %v", eventType, err)
	}
}

func recordMergeError(store *orchstate.Store, issueKey, issueID string, prNumber int, err error) {
	if err == nil {
		return
	}
	recordMergeEvent(store, orchstate.EventErrorRecorded, issueKey, issueID, prNumber, map[string]any{"error": err.Error(), "lane": "merge"})
}

func feedbackAlreadyAddressed(workspace, prURL, feedback string) bool {
	record, ok := reusableRunRecord(workspace)
	if !ok || record.Status != "success" || record.ReviewStatus != "passed" || record.PRURL != prURL {
		return false
	}
	currentHash := feedbackHash(feedback)
	if currentHash == "" {
		return false
	}
	if record.FeedbackHash != "" {
		return record.FeedbackHash == currentHash
	}
	previousFeedback, err := readPRFeedback(workspace)
	return err == nil && feedbackHash(previousFeedback) == currentHash
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
	if record.ReviewStatus != "passed" {
		return fmt.Sprintf("review status is %s", emptyAsUnknown(record.ReviewStatus))
	}
	return ""
}

func readRunArtifact(workspace string) (runRecord, bool) {
	data, err := os.ReadFile(filepath.Join(workspace, ".pi-symphony-run.json"))
	if err != nil {
		return runRecord{}, false
	}
	var record runRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return runRecord{}, false
	}
	return record, true
}
