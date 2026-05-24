package domain

import (
	"encoding/json"
	"testing"
	"time"
)

func TestRunRecordJSONRoundTripPreservesArtifactFields(t *testing.T) {
	startedAt := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	endedAt := startedAt.Add(2 * time.Minute)
	record := RunRecord{
		IssueIdentifier:      "CAG-73",
		IssueID:              "issue-id",
		IssueTitle:           "Extract root domain types",
		IssueURL:             "https://linear.app/wessismore/issue/CAG-73/test",
		Workspace:            "/tmp/workspace",
		WorkspaceRoot:        "/tmp",
		Branch:               "symphony/CAG-73-workspace",
		ExpectedBranch:       "symphony/CAG-73-workspace",
		RuntimeCommand:       "codex exec",
		GitHubAuth:           "gh",
		StartedAt:            startedAt,
		EndedAt:              endedAt,
		DurationMS:           120000,
		RuntimeUsage:         &Usage{Input: 10, Output: 20, CacheRead: 3, CacheWrite: 4, TotalTokens: 37, Cost: &UsageCost{Input: 0.1, Output: 0.2, CacheRead: 0.03, CacheWrite: 0.04, Total: 0.37}},
		ReviewStatus:         "passed",
		ReviewClassification: "clean",
		ReviewFindings:       "none",
		ReviewUsage:          &Usage{TotalTokens: 11},
		PRURL:                "https://github.com/weskor/pi-symphony/pull/73",
		FeedbackHash:         "abc123",
		Status:               "handoff",
		OriginalStatus:       "In Progress",
		ManualRepair:         "none",
		Error:                "",
		Budget:               &Budget{WallClockText: "2h", MaxTokens: 1000, MaxCost: 1.5, CommandText: "10m", PiText: "90m", ReviewText: "30m", MergeText: "10m", GitHubText: "2m"},
		BudgetExceeded:       "",
		BehaviorContractEvidence: []string{
			"docs/specs/harness-behavior.md: run record remains an audit export",
		},
	}

	data, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal run record: %v", err)
	}

	var artifact map[string]any
	if err := json.Unmarshal(data, &artifact); err != nil {
		t.Fatalf("unmarshal artifact map: %v", err)
	}

	for _, key := range []string{
		"issue_identifier", "issue_id", "issue_title", "issue_url", "workspace", "workspace_root",
		"branch", "expected_branch", "runtime_command", "github_auth", "started_at", "ended_at",
		"duration_ms", "runtime_usage", "review_status", "review_classification", "review_findings",
		"review_usage", "pr_url", "feedback_hash", "status", "original_status", "manual_repair",
		"budget", "behavior_contract_evidence",
	} {
		if _, ok := artifact[key]; !ok {
			t.Fatalf("expected artifact field %q in %s", key, string(data))
		}
	}

	var decoded RunRecord
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal run record: %v", err)
	}
	if _, ok := artifact["pi_command"]; ok {
		t.Fatalf("legacy pi_command should not be written in %s", string(data))
	}
	if _, ok := artifact["pi_usage"]; ok {
		t.Fatalf("legacy pi_usage should not be written in %s", string(data))
	}

	if decoded.IssueIdentifier != record.IssueIdentifier || decoded.RuntimeUsage.TotalTokens != record.RuntimeUsage.TotalTokens || decoded.PiUsage.TotalTokens != record.RuntimeUsage.TotalTokens || decoded.Budget.MaxCost != record.Budget.MaxCost || len(decoded.BehaviorContractEvidence) != 1 {
		t.Fatalf("unexpected round trip: %+v", decoded)
	}
}

func TestRunRecordJSONReadsLegacyPiFields(t *testing.T) {
	data := []byte(`{"issue_identifier":"CAG-73","pi_command":"pi run","pi_usage":{"totalTokens":37},"status":"success"}`)

	var decoded RunRecord
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal legacy run record: %v", err)
	}
	if decoded.RuntimeCommand != "pi run" || decoded.PiCommand != "pi run" {
		t.Fatalf("legacy command aliases not normalized: %+v", decoded)
	}
	if decoded.RuntimeUsage == nil || decoded.RuntimeUsage.TotalTokens != 37 || decoded.PiUsage == nil || decoded.PiUsage.TotalTokens != 37 {
		t.Fatalf("legacy usage aliases not normalized: %+v", decoded)
	}
}
