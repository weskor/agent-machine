package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var reviewPRNumberPattern = regexp.MustCompile(`(?i)actual .*PR is #([0-9]+)|PR is #([0-9]+)`)

var prStateForURL = githubPRStateForURL

func repairArtifacts(workspaceRoot string) error {
	log("mode=repair-artifacts; workspace_root=%s", workspaceRoot)
	removedLocks, err := cleanupStaleRunLocks(workspaceRoot, time.Now())
	if err != nil {
		return err
	}
	paths, err := filepath.Glob(filepath.Join(workspaceRoot, "*", ".pi-symphony-run.json"))
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
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	var record runRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return false, err
	}
	changed := false
	if record.PRURL != "" && record.ReviewFindings != "" {
		corrected := correctedPRURL(record.PRURL, record.ReviewFindings)
		if corrected != "" && corrected != record.PRURL {
			record.PRURL = corrected
			record.ManualRepair = appendRepairNote(record.ManualRepair, "corrected_pr_url")
			changed = true
		}
	}
	if record.PRURL != "" && terminalRunStatus(record.Status) && record.Status != "merged" && record.Status != "superseded" {
		state, merged, err := prStateForURL(record.PRURL)
		if err != nil {
			return false, err
		}
		if strings.EqualFold(state, "MERGED") || merged {
			markManualRepairStatus(&record, "merged", "pr_manually_merged")
			changed = true
		} else if strings.EqualFold(state, "CLOSED") {
			markManualRepairStatus(&record, "superseded", "pr_closed_unmerged")
			changed = true
		}
	}
	if !changed {
		return false, nil
	}
	updated, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return false, err
	}
	updated = append(updated, '\n')
	if err := os.WriteFile(path, updated, 0o600); err != nil {
		return false, err
	}
	log("repaired %s status=%s pr_url=%s manual_repair=%s", path, record.Status, record.PRURL, record.ManualRepair)
	return true, nil
}

func markManualRepairStatus(record *runRecord, status, note string) {
	if record.OriginalStatus == "" {
		record.OriginalStatus = record.Status
	}
	record.Status = status
	record.ManualRepair = appendRepairNote(record.ManualRepair, note)
}

func appendRepairNote(existing, note string) string {
	if existing == "" {
		return note
	}
	for _, part := range strings.Split(existing, ",") {
		if part == note {
			return existing
		}
	}
	return existing + "," + note
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
	matches := reviewPRNumberPattern.FindStringSubmatch(findings)
	if len(matches) == 0 {
		return ""
	}
	number := ""
	for _, match := range matches[1:] {
		if match != "" {
			number = match
			break
		}
	}
	if number == "" {
		return ""
	}
	return regexp.MustCompile(`/pull/[0-9]+`).ReplaceAllString(currentURL, fmt.Sprintf("/pull/%s", number))
}
