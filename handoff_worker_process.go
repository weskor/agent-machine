package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/weskor/pi-symphony/internal/state"
)

type claimedHandoffPendingAttempt struct {
	Payload     handoffPendingPayload
	ReleaseLock func()
}

func runHandoffPendingAttempt(client linearClient, config runnerConfig, stateStore *state.Store) (bool, error) {
	if stateStore == nil {
		return false, fmt.Errorf("SQLite state store unavailable for handoff worker at %s", state.DefaultDBPath(config.WorkspaceRoot))
	}
	if removed, err := cleanupStaleRunLocksWithState(stateStore, config.WorkspaceRoot, time.Now()); err != nil {
		return false, err
	} else if removed > 0 {
		log("removed %d stale/dead run lock(s) before handoff selection", removed)
	}
	claim, didWork, err := claimNextHandoffPendingAttempt(config, stateStore)
	if err != nil || claim == nil {
		return didWork, err
	}
	return executeHandoffPendingAttempt(context.Background(), client, config, stateStore, *claim)
}

func claimNextHandoffPendingAttempt(config runnerConfig, stateStore *state.Store) (*claimedHandoffPendingAttempt, bool, error) {
	root := runProgressRoot(config.WorkspaceRoot)
	if strings.TrimSpace(root) == "" {
		return nil, false, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		identifier := entry.Name()
		snapshot, err := readRunProgress(config.WorkspaceRoot, identifier)
		if err != nil {
			return nil, true, err
		}
		if snapshot.Phase != runProgressPhaseHandoffPending {
			continue
		}
		payload, err := readHandoffPendingPayload(config.WorkspaceRoot, identifier)
		if err != nil {
			return nil, true, err
		}
		normalizeHandoffPendingPayload(&payload, snapshot)
		if strings.TrimSpace(payload.Workspace) == "" {
			workspace, err := safeWorkspacePath(config.WorkspaceRoot, identifier)
			if err != nil {
				return nil, true, err
			}
			payload.Workspace = workspace
		}
		if strings.TrimSpace(payload.Branch) == "" {
			payload.Branch = expectedWorkspaceBranch(identifier)
		}
		completion := payload.Completion(linearClient{}, config, stateStore, nil)
		if strings.TrimSpace(completion.prURL) == "" {
			return nil, true, fmt.Errorf("handoff pending payload for %s has no PR URL", identifier)
		}
		lock, releaseLock, err := acquireRunLockWithState(stateStore, completion.workspace, completion.candidate, completion.branch, time.Now())
		if err != nil {
			if errors.Is(err, errRunLocked) {
				log("%v", err)
				continue
			}
			return nil, true, err
		}
		payload.Branch = lock.Branch
		return &claimedHandoffPendingAttempt{Payload: payload, ReleaseLock: releaseLock}, true, nil
	}
	log("no handoff-pending issues")
	return nil, false, nil
}

func normalizeHandoffPendingPayload(payload *handoffPendingPayload, snapshot runProgressSnapshot) {
	if payload == nil {
		return
	}
	if strings.TrimSpace(payload.IssueIdentifier) == "" {
		payload.IssueIdentifier = snapshot.IssueIdentifier
	}
	if strings.TrimSpace(payload.IssueTitle) == "" {
		payload.IssueTitle = snapshot.IssueTitle
	}
	if strings.TrimSpace(payload.Workspace) == "" {
		payload.Workspace = snapshot.Workspace
	}
	if strings.TrimSpace(payload.Branch) == "" {
		payload.Branch = snapshot.Branch
	}
	if strings.TrimSpace(payload.PRURL) == "" {
		payload.PRURL = snapshot.PRURL
	}
	if payload.ProgressStarted.IsZero() {
		payload.ProgressStarted = snapshot.StartedAt
	}
	if payload.StartedAt.IsZero() {
		payload.StartedAt = snapshot.StartedAt
	}
}

func executeHandoffPendingAttempt(ctx context.Context, client linearClient, config runnerConfig, stateStore *state.Store, claimed claimedHandoffPendingAttempt) (bool, error) {
	if claimed.ReleaseLock != nil {
		defer claimed.ReleaseLock()
	}
	payload := claimed.Payload
	var states []workflowState
	if strings.TrimSpace(config.HandoffState) != "" {
		if strings.TrimSpace(payload.TeamID) == "" {
			return true, fmt.Errorf("handoff pending payload for %s has no team ID", payload.IssueIdentifier)
		}
		var err error
		states, err = client.workflowStates(payload.TeamID)
		if err != nil {
			return true, err
		}
	}
	return completeAttemptHandoff(ctx, payload.Completion(client, config, stateStore, states))
}
