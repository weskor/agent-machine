package main

import (
	"context"
	"fmt"
	"strings"
)

func runOne(client linearClient, proj project, config runnerConfig) (bool, error) {
	log("mode=test-once; project=%s; states=%s", config.ProjectSlug, strings.Join(config.ActiveStates, ", "))
	stateStore, stateDBPath := commandScopedStateStore(context.Background(), config.WorkspaceRoot, "test-run-one")
	if stateStore == nil {
		return false, fmt.Errorf("SQLite state store unavailable for test run-one at %s", stateDBPath)
	}
	defer stateStore.Close()
	claim, didWork, err := claimNextRunAttempt(client, proj, config, stateStore)
	if err != nil || claim == nil {
		return didWork, err
	}
	return executeClaimedRunAttempt(context.Background(), client, proj, config, stateStore, *claim)
}
