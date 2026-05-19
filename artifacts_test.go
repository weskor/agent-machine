package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCorrectedPRURLFromReviewFindings(t *testing.T) {
	got := correctedPRURL(
		"https://github.com/pennywise-investments/compound-web/pull/2",
		"REVIEW_PASS\n\nFindings:\n- Actual CAG-11 branch PR is #400, not prompt-listed #2.",
	)
	want := "https://github.com/pennywise-investments/compound-web/pull/400"
	if got != want {
		t.Fatalf("correctedPRURL() = %q, want %q", got, want)
	}
}

func TestCorrectedPRURLNoFinding(t *testing.T) {
	if got := correctedPRURL("https://github.com/example/repo/pull/2", "REVIEW_PASS"); got != "" {
		t.Fatalf("correctedPRURL() = %q, want empty", got)
	}
}

func TestWriteRunRecordPersistsBudgetTerminalStatus(t *testing.T) {
	workspace := t.TempDir()
	now := time.Now()
	record := runRecordFor(&issue{ID: "issue-id", Identifier: "CAG-1", Title: "title"}, workspace, "pi", "", now, now, nil, nil, "", "timeout", "command timed out", (&runBudget{PiText: "1s", PiTimeout: time.Second}).Active(), "command timed out")
	writeRunRecord(workspace, record)

	data, err := os.ReadFile(filepath.Join(workspace, ".pi-symphony-run.json"))
	if err != nil {
		t.Fatal(err)
	}
	var persisted runRecord
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Status != "timeout" || persisted.BudgetExceeded == "" || persisted.Budget == nil || persisted.Budget.PiText != "1s" {
		t.Fatalf("unexpected persisted run record: %#v", persisted)
	}
	if !terminalRunStatus(persisted.Status) {
		t.Fatalf("expected timeout to be terminal")
	}
}

func TestRepairArtifactMarksManuallyMergedPRWithoutDroppingUsage(t *testing.T) {
	old := prStateForURL
	prStateForURL = func(prURL string) (string, bool, error) { return "MERGED", true, nil }
	t.Cleanup(func() { prStateForURL = old })

	workspace := t.TempDir()
	path := filepath.Join(workspace, ".pi-symphony-run.json")
	record := runRecord{IssueIdentifier: "CAG-1", IssueURL: "https://linear.app/acme/issue/CAG-1/title", Workspace: workspace, Status: "success", PRURL: "https://github.com/acme/repo/pull/1", PiUsage: &usage{TotalTokens: 123, Cost: &usageCost{Total: 0.45}}}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	changed, err := repairArtifact(path)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected repair")
	}
	var repaired runRecord
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &repaired); err != nil {
		t.Fatal(err)
	}
	if repaired.Status != "merged" || repaired.OriginalStatus != "success" || repaired.ManualRepair != "pr_manually_merged" || repaired.PiUsage.TotalTokens != 123 {
		t.Fatalf("unexpected repaired record: %#v", repaired)
	}
}

func TestRepairArtifactMarksClosedPRSuperseded(t *testing.T) {
	old := prStateForURL
	prStateForURL = func(prURL string) (string, bool, error) { return "CLOSED", false, nil }
	t.Cleanup(func() { prStateForURL = old })

	workspace := t.TempDir()
	path := filepath.Join(workspace, ".pi-symphony-run.json")
	record := runRecord{IssueIdentifier: "CAG-2", IssueURL: "https://linear.app/acme/issue/CAG-2/title", Workspace: workspace, Status: "review_failed", PRURL: "https://github.com/acme/repo/pull/2"}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	changed, err := repairArtifact(path)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected repair")
	}
	var repaired runRecord
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &repaired); err != nil {
		t.Fatal(err)
	}
	if repaired.Status != "superseded" || repaired.OriginalStatus != "review_failed" || repaired.ManualRepair != "pr_closed_unmerged" {
		t.Fatalf("unexpected repaired record: %#v", repaired)
	}
}

func TestParseGitHubPRStateUsesSupportedMergedAtField(t *testing.T) {
	state, merged, err := parseGitHubPRState(`{"state":"MERGED","mergedAt":"2026-05-18T12:00:00Z"}`)
	if err != nil {
		t.Fatal(err)
	}
	if state != "MERGED" || !merged {
		t.Fatalf("parseGitHubPRState() = %q, %v", state, merged)
	}

	state, merged, err = parseGitHubPRState(`{"state":"CLOSED","mergedAt":null}`)
	if err != nil {
		t.Fatal(err)
	}
	if state != "CLOSED" || merged {
		t.Fatalf("parseGitHubPRState() = %q, %v", state, merged)
	}
}
