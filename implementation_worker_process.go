package main

import (
	"fmt"

	"github.com/weskor/pi-symphony/internal/state"
)

func runImplementationAttempt(client linearClient, wf workflow, config runnerConfig, stateStore *state.Store) (bool, error) {
	return runImplementationAttemptBatch(client, wf, config, stateStore, 1)
}

func runImplementationAttemptBatch(client linearClient, wf workflow, config runnerConfig, stateStore *state.Store, capacity int) (bool, error) {
	if stateStore == nil {
		return false, fmt.Errorf("SQLite state store unavailable for implementation worker at %s", state.DefaultDBPath(config.WorkspaceRoot))
	}
	return runClaimedAttemptBatchWithClaimer(client, wf, config, stateStore, capacity, claimNextImplementationAttempt)
}

func claimNextImplementationAttempt(client linearClient, wf workflow, config runnerConfig, stateStore *state.Store) (*claimedRunAttempt, bool, error) {
	return claimNextRunAttemptWithOptions(client, wf, config, stateStore, candidateSelectionOptions{SkipReviewReadyResumes: true})
}
