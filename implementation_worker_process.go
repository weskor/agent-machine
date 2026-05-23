package main

import (
	"fmt"

	"github.com/weskor/pi-symphony/internal/state"
)

func runImplementationAttempt(client linearClient, wf workflow, config runnerConfig, stateStore *state.Store) (bool, error) {
	if stateStore == nil {
		return false, fmt.Errorf("SQLite state store unavailable for implementation worker at %s", state.DefaultDBPath(config.WorkspaceRoot))
	}
	claim, didWork, err := claimNextImplementationAttempt(client, wf, config, stateStore)
	if err != nil || claim == nil {
		return didWork, err
	}
	return executeClaimedRunAttempt(client, wf, config, stateStore, *claim)
}

func claimNextImplementationAttempt(client linearClient, wf workflow, config runnerConfig, stateStore *state.Store) (*claimedRunAttempt, bool, error) {
	return claimNextRunAttemptWithOptions(client, wf, config, stateStore, candidateSelectionOptions{SkipReviewReadyResumes: true})
}
