package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	artifactio "github.com/weskor/agent-machine/internal/artifacts"
)

var prStateForURL = githubPRStateForURL

func repairArtifacts(workspaceRoot string) error {
	log("mode=repair-artifacts; workspace_root=%s", workspaceRoot)
	removedLocks, err := cleanupStaleRunLocks(workspaceRoot, time.Now())
	if err != nil {
		return err
	}
	paths, err := filepath.Glob(filepath.Join(workspaceRoot, "*", artifactio.RunRecordName))
	if err != nil {
		return err
	}
	repaired := 0
	for _, path := range paths {
		changed, err := repairArtifact(path)
		if err != nil {
			return err
		}
		if changed {
			repaired++
		}
	}
	log("repaired %d artifact(s); removed %d stale lock(s)", repaired, removedLocks)
	return nil
}

func repairArtifact(path string) (bool, error) {
	changed, record, err := artifactManager().Repair(path)
	if err != nil || !changed {
		return changed, err
	}
	log("repaired %s status=%s pr_url=%s manual_repair=%s", path, record.Status, record.PRURL, record.ManualRepair)
	return true, nil
}

func githubPRStateForURL(prURL string) (string, bool, error) {
	if strings.TrimSpace(prURL) == "" {
		return "", false, nil
	}
	github, ctx, cancel, err := githubClientWithTimeout(defaultGitHubCommandTimeout)
	if err != nil {
		return "", false, err
	}
	defer cancel()
	state, merged, err := github.PullRequestState(ctx, prURL)
	if err != nil {
		return "", false, fmt.Errorf("GitHub API PR state lookup failed for %s: %w", prURL, err)
	}
	return state, merged, nil
}

func parseGitHubPRState(output string) (string, bool, error) {
	var view struct {
		State    string `json:"state"`
		MergedAt string `json:"mergedAt"`
	}
	if err := json.Unmarshal([]byte(output), &view); err != nil {
		return "", false, err
	}
	return view.State, strings.TrimSpace(view.MergedAt) != "", nil
}

func correctedPRURL(currentURL, findings string) string {
	return artifactio.CorrectedPRURL(currentURL, findings)
}
