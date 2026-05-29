package linearstatus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/weskor/agent-machine/internal/domain"
	"github.com/weskor/agent-machine/internal/state"
	"github.com/weskor/agent-machine/internal/workertask"
)

const TaskKindTransition = "transition"

type TransitionClient interface {
	Client
	WorkflowStatesContext(ctx context.Context, teamID string) ([]domain.WorkflowState, error)
}

type TransitionPayload struct {
	Kind            string `json:"kind"`
	IssueID         string `json:"issue_id"`
	IssueIdentifier string `json:"issue_identifier"`
	IssueTitle      string `json:"issue_title,omitempty"`
	IssueURL        string `json:"issue_url,omitempty"`
	TeamID          string `json:"team_id"`
	TargetState     string `json:"target_state"`
}

func QueueTransitionTask(ctx context.Context, store *state.Store, payload TransitionPayload, priority int, availableAt time.Time) error {
	if store == nil {
		return fmt.Errorf("SQLite state store unavailable for Linear status task")
	}
	normalized, err := NormalizeTransitionPayload(payload)
	if err != nil {
		return err
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return err
	}
	taskKey := TransitionTaskKey(normalized)
	availableAt, err = workertask.AvailableAtAfterLatestFailure(ctx, store, taskKey, workertask.RoleLinearStatus, availableAt)
	if err != nil {
		return err
	}
	task := state.WorkerTask{
		TaskKey:     taskKey,
		Role:        workertask.RoleLinearStatus,
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

func RunTransitionTask(client TransitionClient, workspaceRoot string, store *state.Store, logf func(string, ...any)) (bool, error) {
	return RunTransitionTaskContext(context.Background(), client, workspaceRoot, store, logf)
}

func RunTransitionTaskContext(ctx context.Context, client TransitionClient, workspaceRoot string, store *state.Store, logf func(string, ...any)) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if store == nil {
		return false, fmt.Errorf("SQLite state store unavailable for Linear status worker at %s", state.DefaultDBPath(workspaceRoot))
	}
	claimed, ok, err := ClaimNextTransitionTask(ctx, store)
	if err != nil || !ok {
		return false, err
	}
	return ExecuteTransitionTask(ctx, client, store, claimed, logf)
}

func ClaimNextTransitionTask(ctx context.Context, store *state.Store) (state.WorkerTask, bool, error) {
	tasks, err := store.WorkerTasks(ctx, workertask.RoleLinearStatus)
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

func ExecuteTransitionTask(ctx context.Context, client TransitionClient, store *state.Store, task state.WorkerTask, logf func(string, ...any)) (bool, error) {
	payload, err := DecodeTransitionPayload(task.Payload)
	if err != nil {
		return true, CompleteTransitionTask(ctx, store, task.TaskKey, "failed", err)
	}
	if err := ctx.Err(); err != nil {
		return true, CompleteTransitionTask(ctx, store, task.TaskKey, "failed", err)
	}
	states, err := client.WorkflowStatesContext(ctx, payload.TeamID)
	if err != nil {
		return true, CompleteTransitionTask(ctx, store, task.TaskKey, "failed", err)
	}
	candidate := &domain.Issue{ID: payload.IssueID, Identifier: payload.IssueIdentifier, Title: payload.IssueTitle, URL: payload.IssueURL}
	candidate.Team.ID = payload.TeamID
	moved, err := (Worker{Client: client, Candidate: candidate, States: states, Logf: logf}).MoveToContext(ctx, payload.TargetState)
	if err != nil {
		return true, CompleteTransitionTask(ctx, store, task.TaskKey, "failed", err)
	}
	if !moved {
		err = fmt.Errorf("linear status transition for %s to %q was not available", payload.IssueIdentifier, payload.TargetState)
		return true, CompleteTransitionTask(ctx, store, task.TaskKey, "failed", err)
	}
	return true, CompleteTransitionTask(ctx, store, task.TaskKey, "completed", nil)
}

func CompleteTransitionTask(ctx context.Context, store *state.Store, taskKey, status string, primaryErr error) error {
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

func DecodeTransitionPayload(data []byte) (TransitionPayload, error) {
	var payload TransitionPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return TransitionPayload{}, err
	}
	return NormalizeTransitionPayload(payload)
}

func NormalizeTransitionPayload(payload TransitionPayload) (TransitionPayload, error) {
	payload.Kind = strings.TrimSpace(payload.Kind)
	if payload.Kind == "" {
		payload.Kind = TaskKindTransition
	}
	payload.IssueID = strings.TrimSpace(payload.IssueID)
	payload.IssueIdentifier = strings.TrimSpace(payload.IssueIdentifier)
	payload.IssueTitle = strings.TrimSpace(payload.IssueTitle)
	payload.IssueURL = strings.TrimSpace(payload.IssueURL)
	payload.TeamID = strings.TrimSpace(payload.TeamID)
	payload.TargetState = strings.TrimSpace(payload.TargetState)
	if payload.Kind != TaskKindTransition {
		return TransitionPayload{}, fmt.Errorf("unsupported linear status task kind %q", payload.Kind)
	}
	if payload.IssueID == "" {
		return TransitionPayload{}, fmt.Errorf("linear status transition issue_id is required")
	}
	if payload.IssueIdentifier == "" {
		return TransitionPayload{}, fmt.Errorf("linear status transition issue_identifier is required")
	}
	if payload.TeamID == "" {
		return TransitionPayload{}, fmt.Errorf("linear status transition team_id is required")
	}
	if payload.TargetState == "" {
		return TransitionPayload{}, fmt.Errorf("linear status transition target_state is required")
	}
	return payload, nil
}

func TransitionTaskKey(payload TransitionPayload) string {
	return "linear-status:" + payload.IssueIdentifier + ":transition:" + taskKeyToken(payload.TargetState)
}

func taskKeyToken(value string) string {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(value)))
	if len(fields) == 0 {
		return "unknown"
	}
	return strings.Join(fields, "-")
}
