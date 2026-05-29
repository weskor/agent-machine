package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	orchstate "github.com/weskor/agent-machine/internal/state"
	"github.com/weskor/agent-machine/internal/terminalpr"
)

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
	linearStatus := linearStatusWorker{client: client, candidate: candidate, states: states}
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
