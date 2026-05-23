package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	orchstate "github.com/weskor/pi-symphony/internal/state"
)

type mergeWorker struct {
	client linearClient
	config runnerConfig
	store  *orchstate.Store
	github githubAPI
}

func (w mergeWorker) HandlePullRequest(ctx context.Context, pr pullRequestSummary) error {
	identifier := issueIdentifierFromBranch(pr.HeadRefName)
	candidate, err := w.client.issueByIdentifier(identifier)
	if err != nil {
		recordMergeError(w.store, identifier, "", pr.Number, err)
		return err
	}
	if candidate == nil || candidate.State.Name != w.config.HandoffState {
		return nil
	}

	states, err := w.client.workflowStates(candidate.Team.ID)
	if err != nil {
		recordMergeError(w.store, candidate.Identifier, candidate.ID, pr.Number, err)
		return err
	}
	gate := evaluatePullRequestMergeGate(pr)
	if hasString(gate.Codes(), "merge_conflict") {
		reason := gate.Reason()
		recordMergeEvent(w.store, orchstate.EventMergeBlocked, candidate.Identifier, candidate.ID, pr.Number, map[string]any{"pr_url": pr.URL, "reason": reason, "codes": gate.Codes()})
		workspace := filepath.Join(w.config.WorkspaceRoot, candidate.Identifier)
		if err := os.MkdirAll(workspace, 0o755); err != nil {
			return err
		}
		if err := writePRFeedback(workspace, pr.Number, renderPRConflictFeedback(pr, reason)); err != nil {
			return err
		}
		if id := stateID(states, w.config.ReadyState); id != "" {
			if err := w.client.updateIssueState(candidate.ID, id); err != nil {
				return err
			}
		}
		_ = w.client.createComment(candidate.ID, fmt.Sprintf("PR merge blocked by conflicts; captured repair instructions and moved back to %s for pickup: %s", w.config.ReadyState, pr.URL))
		log("blocked merge for %s: %s", candidate.Identifier, reason)
		return nil
	}
	decision := newReconciliationModule(w.store).ReconcileIssue(w.config, *candidate, &pr)
	if decision.ShouldQuarantine && len(decision.Blockers) > 0 {
		recordMergeEvent(w.store, orchstate.EventMergeBlocked, candidate.Identifier, candidate.ID, pr.Number, map[string]any{"pr_url": pr.URL, "reason": strings.Join(decision.Blockers, "; "), "next_action": decision.NextAction})
		_ = w.client.createComment(candidate.ID, fmt.Sprintf("Symphony PR blocked by reconciliation invariant; next=%s; reason: %s", decision.NextAction, strings.Join(decision.Blockers, "; ")))
		log("%s quarantined: %s", pr.URL, strings.Join(decision.Blockers, "; "))
		return nil
	}
	switch pr.ReviewDecision {
	case "APPROVED":
		return w.handleApprovedPR(ctx, candidate, states, pr, decision)
	case "CHANGES_REQUESTED":
		return w.handleChangesRequestedPR(candidate, states, pr)
	default:
		log("%s waiting for approval; reviewDecision=%s", pr.URL, pr.ReviewDecision)
		return nil
	}
}

func (w mergeWorker) handleApprovedPR(ctx context.Context, candidate *issue, states []workflowState, pr pullRequestSummary, decision reconciliationDecision) error {
	if !decision.CanMerge {
		recordMergeEvent(w.store, orchstate.EventMergeBlocked, candidate.Identifier, candidate.ID, pr.Number, map[string]any{"pr_url": pr.URL, "reason": strings.Join(decision.Blockers, "; "), "lifecycle": decision.Lifecycle, "next_action": decision.NextAction})
		log("%s approved but merge is blocked: lifecycle=%s blockers=%s next=%s", pr.URL, decision.Lifecycle, strings.Join(decision.Blockers, "; "), decision.NextAction)
		return nil
	}
	recordMergeEvent(w.store, orchstate.EventMergeAttempted, candidate.Identifier, candidate.ID, pr.Number, map[string]any{"pr_url": pr.URL, "head_ref": pr.HeadRefName, "base_ref": pr.BaseRefName})
	if err := w.github.SquashMergePullRequest(ctx, pr.Number); err != nil {
		recordMergeEvent(w.store, orchstate.EventMergeFailed, candidate.Identifier, candidate.ID, pr.Number, map[string]any{"pr_url": pr.URL, "phase": "squash_merge", "error": err.Error()})
		recordMergeError(w.store, candidate.Identifier, candidate.ID, pr.Number, err)
		return fmt.Errorf("GitHub API squash merge failed for PR #%d: %w", pr.Number, err)
	}
	recordMergeEvent(w.store, orchstate.EventMergeSucceeded, candidate.Identifier, candidate.ID, pr.Number, map[string]any{"pr_url": pr.URL, "head_ref": pr.HeadRefName})
	recordMergeEvent(w.store, orchstate.EventBranchDeletionAttempted, candidate.Identifier, candidate.ID, pr.Number, map[string]any{"pr_url": pr.URL, "head_ref": pr.HeadRefName})
	if err := w.github.DeleteBranch(ctx, pr.HeadRefName); err != nil {
		recordMergeEvent(w.store, orchstate.EventBranchDeletionFinished, candidate.Identifier, candidate.ID, pr.Number, map[string]any{"pr_url": pr.URL, "head_ref": pr.HeadRefName, "result": "failed", "error": err.Error()})
		recordMergeEvent(w.store, orchstate.EventMergeFailed, candidate.Identifier, candidate.ID, pr.Number, map[string]any{"pr_url": pr.URL, "phase": "branch_deletion", "error": err.Error()})
		recordMergeError(w.store, candidate.Identifier, candidate.ID, pr.Number, err)
		return fmt.Errorf("GitHub API branch deletion failed for %s after merged PR #%d: %w", pr.HeadRefName, pr.Number, err)
	}
	recordMergeEvent(w.store, orchstate.EventBranchDeletionFinished, candidate.Identifier, candidate.ID, pr.Number, map[string]any{"pr_url": pr.URL, "head_ref": pr.HeadRefName, "result": "success"})
	if id := stateID(states, w.config.DoneState); id != "" {
		recordMergeEvent(w.store, orchstate.EventLinearDoneTransitionAttempted, candidate.Identifier, candidate.ID, pr.Number, map[string]any{"pr_url": pr.URL, "done_state": w.config.DoneState})
		if err := w.client.updateIssueState(candidate.ID, id); err != nil {
			recordMergeEvent(w.store, orchstate.EventLinearDoneTransitionFinished, candidate.Identifier, candidate.ID, pr.Number, map[string]any{"pr_url": pr.URL, "done_state": w.config.DoneState, "result": "failed", "error": err.Error()})
			recordMergeEvent(w.store, orchstate.EventMergeFailed, candidate.Identifier, candidate.ID, pr.Number, map[string]any{"pr_url": pr.URL, "phase": "linear_done_transition", "error": err.Error()})
			recordMergeError(w.store, candidate.Identifier, candidate.ID, pr.Number, err)
			return err
		}
		recordMergeEvent(w.store, orchstate.EventLinearDoneTransitionFinished, candidate.Identifier, candidate.ID, pr.Number, map[string]any{"pr_url": pr.URL, "done_state": w.config.DoneState, "result": "success"})
	}
	if err := removeDoneWorkspace(w.config.WorkspaceRoot, candidate.Identifier); err != nil {
		recordMergeEvent(w.store, orchstate.EventMergeFailed, candidate.Identifier, candidate.ID, pr.Number, map[string]any{"pr_url": pr.URL, "phase": "workspace_cleanup", "error": err.Error()})
		recordMergeError(w.store, candidate.Identifier, candidate.ID, pr.Number, err)
		return err
	}
	recordMergeEvent(w.store, orchstate.EventMergeCompleted, candidate.Identifier, candidate.ID, pr.Number, map[string]any{"pr_url": pr.URL, "head_ref": pr.HeadRefName, "done_state": w.config.DoneState})
	_ = w.client.createComment(candidate.ID, fmt.Sprintf("Merged approved PR: %s", pr.URL))
	log("merged %s and moved %s to %s", pr.URL, candidate.Identifier, w.config.DoneState)
	return nil
}

func (w mergeWorker) handleChangesRequestedPR(candidate *issue, states []workflowState, pr pullRequestSummary) error {
	feedback, err := collectPRFeedback(pr.Number)
	if err != nil {
		return err
	}
	workspace := filepath.Join(w.config.WorkspaceRoot, candidate.Identifier)
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return err
	}
	if feedbackAlreadyAddressed(workspace, pr.URL, feedback) {
		log("%s has CHANGES_REQUESTED but feedback was already addressed; waiting for human approval", pr.URL)
		return nil
	}
	if err := writePRFeedback(workspace, pr.Number, feedback); err != nil {
		return err
	}
	if id := stateID(states, w.config.ReadyState); id != "" {
		if err := w.client.updateIssueState(candidate.ID, id); err != nil {
			return err
		}
	}
	_ = w.client.createComment(candidate.ID, fmt.Sprintf("PR changes requested; captured GitHub review feedback and moved back to %s for pickup: %s", w.config.ReadyState, pr.URL))
	log("moved %s back to %s after requested changes; feedback captured", candidate.Identifier, w.config.ReadyState)
	return nil
}
