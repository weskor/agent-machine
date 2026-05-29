package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/weskor/agent-machine/internal/state"
	"github.com/weskor/agent-machine/internal/workertask"
)

const linearStatusTaskKindTransition = "transition"

type linearStatusTransitionPayload struct {
	Kind            string `json:"kind"`
	IssueID         string `json:"issue_id"`
	IssueIdentifier string `json:"issue_identifier"`
	IssueTitle      string `json:"issue_title,omitempty"`
	IssueURL        string `json:"issue_url,omitempty"`
	TeamID          string `json:"team_id"`
	TargetState     string `json:"target_state"`
}

func queueLinearStatusTransitionTask(ctx context.Context, store *state.Store, payload linearStatusTransitionPayload, priority int, availableAt time.Time) error {
	if store == nil {
		return fmt.Errorf("SQLite state store unavailable for Linear status task")
	}
	normalized, err := normalizeLinearStatusTransitionPayload(payload)
	if err != nil {
		return err
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return err
	}
	taskKey := linearStatusTransitionTaskKey(normalized)
	availableAt, err = workertask.AvailableAtAfterLatestFailure(ctx, store, taskKey, linearStatusWorkerRole, availableAt)
	if err != nil {
		return err
	}
	task := state.WorkerTask{
		TaskKey:     taskKey,
		Role:        linearStatusWorkerRole,
		IssueKey:    normalized.IssueIdentifier,
		IssueID:     normalized.IssueID,
		Status:      "queued",
		Priority:    priority,
		AvailableAt: availableAt,
		LeaseName:   "linear-status:" + normalized.IssueIdentifier,
		Payload:     data,
	}
	return store.UpsertWorkerTask(ctx, task)
}

func runLinearStatusTransitionTask(client linearClient, config runnerConfig, store *state.Store) (bool, error) {
	return runLinearStatusTransitionTaskContext(context.Background(), client, config, store)
}

func runLinearStatusTransitionTaskContext(ctx context.Context, client linearClient, config runnerConfig, store *state.Store) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if store == nil {
		return false, fmt.Errorf("SQLite state store unavailable for Linear status worker at %s", state.DefaultDBPath(config.WorkspaceRoot))
	}
	claimed, ok, err := claimNextLinearStatusTransitionTask(ctx, store)
	if err != nil || !ok {
		return false, err
	}
	return executeLinearStatusTransitionTask(ctx, client, store, claimed)
}

func claimNextLinearStatusTransitionTask(ctx context.Context, store *state.Store) (state.WorkerTask, bool, error) {
	tasks, err := store.WorkerTasks(ctx, linearStatusWorkerRole)
	if err != nil {
		return state.WorkerTask{}, false, err
	}
	now := time.Now().UTC()
	for _, task := range tasks {
		if task.Status != "queued" || strings.HasPrefix(task.TaskKey, "process:") {
			continue
		}
		claimed, ok, err := store.ClaimWorkerTask(ctx, task.TaskKey, now)
		if err != nil {
			return state.WorkerTask{}, false, err
		}
		if ok {
			return claimed, true, nil
		}
	}
	return state.WorkerTask{}, false, nil
}

func executeLinearStatusTransitionTask(ctx context.Context, client linearClient, store *state.Store, task state.WorkerTask) (bool, error) {
	payload, err := decodeLinearStatusTransitionPayload(task.Payload)
	if err != nil {
		return true, completeLinearStatusTask(ctx, store, task.TaskKey, "failed", err)
	}
	if err := ctx.Err(); err != nil {
		return true, completeLinearStatusTask(ctx, store, task.TaskKey, "failed", err)
	}
	states, err := workflowStatesForLinearStatusWorker(ctx, client, payload.TeamID)
	if err != nil {
		return true, completeLinearStatusTask(ctx, store, task.TaskKey, "failed", err)
	}
	candidate := &issue{ID: payload.IssueID, Identifier: payload.IssueIdentifier, Title: payload.IssueTitle, URL: payload.IssueURL}
	candidate.Team.ID = payload.TeamID
	moved, err := (linearStatusWorker{client: client, candidate: candidate, states: states}).MoveToContext(ctx, payload.TargetState)
	if err != nil {
		return true, completeLinearStatusTask(ctx, store, task.TaskKey, "failed", err)
	}
	if !moved {
		err = fmt.Errorf("linear status transition for %s to %q was not available", payload.IssueIdentifier, payload.TargetState)
		return true, completeLinearStatusTask(ctx, store, task.TaskKey, "failed", err)
	}
	return true, completeLinearStatusTask(ctx, store, task.TaskKey, "completed", nil)
}

func completeLinearStatusTask(ctx context.Context, store *state.Store, taskKey, status string, primaryErr error) error {
	completeCtx := ctx
	if ctx.Err() != nil {
		completeCtx = context.WithoutCancel(ctx)
	}
	completeErr := store.CompleteWorkerTask(completeCtx, taskKey, status, time.Now().UTC())
	if primaryErr != nil {
		return errors.Join(primaryErr, completeErr)
	}
	return completeErr
}

func decodeLinearStatusTransitionPayload(data []byte) (linearStatusTransitionPayload, error) {
	var payload linearStatusTransitionPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return linearStatusTransitionPayload{}, err
	}
	return normalizeLinearStatusTransitionPayload(payload)
}

func normalizeLinearStatusTransitionPayload(payload linearStatusTransitionPayload) (linearStatusTransitionPayload, error) {
	payload.Kind = strings.TrimSpace(payload.Kind)
	if payload.Kind == "" {
		payload.Kind = linearStatusTaskKindTransition
	}
	payload.IssueID = strings.TrimSpace(payload.IssueID)
	payload.IssueIdentifier = strings.TrimSpace(payload.IssueIdentifier)
	payload.IssueTitle = strings.TrimSpace(payload.IssueTitle)
	payload.IssueURL = strings.TrimSpace(payload.IssueURL)
	payload.TeamID = strings.TrimSpace(payload.TeamID)
	payload.TargetState = strings.TrimSpace(payload.TargetState)
	if payload.Kind != linearStatusTaskKindTransition {
		return linearStatusTransitionPayload{}, fmt.Errorf("unsupported linear status task kind %q", payload.Kind)
	}
	if payload.IssueID == "" {
		return linearStatusTransitionPayload{}, fmt.Errorf("linear status transition issue_id is required")
	}
	if payload.IssueIdentifier == "" {
		return linearStatusTransitionPayload{}, fmt.Errorf("linear status transition issue_identifier is required")
	}
	if payload.TeamID == "" {
		return linearStatusTransitionPayload{}, fmt.Errorf("linear status transition team_id is required")
	}
	if payload.TargetState == "" {
		return linearStatusTransitionPayload{}, fmt.Errorf("linear status transition target_state is required")
	}
	return payload, nil
}

func linearStatusTransitionTaskKey(payload linearStatusTransitionPayload) string {
	return "linear-status:" + payload.IssueIdentifier + ":transition:" + taskKeyToken(payload.TargetState)
}

func taskKeyToken(value string) string {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(value)))
	if len(fields) == 0 {
		return "unknown"
	}
	return strings.Join(fields, "-")
}

var workflowStatesForLinearStatusWorker = func(ctx context.Context, client linearClient, teamID string) ([]workflowState, error) {
	return client.workflowStatesContext(ctx, teamID)
}
