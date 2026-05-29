package artifacts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weskor/agent-machine/internal/domain"
	"github.com/weskor/agent-machine/internal/state"
)

func testManager() Manager {
	return Manager{
		Evaluate: func(_ string, record domain.RunRecord) EvaluationArtifact {
			return EvaluationArtifact{IssueIdentifier: record.IssueIdentifier, FinalStatus: record.Status, Outcome: "fallback"}
		},
		PRStateForURL:  func(string) (string, bool, error) { return "", false, nil },
		TerminalStatus: func(string) bool { return true },
	}
}

func TestManagerWriteReadBackfillArtifacts(t *testing.T) {
	workspace := t.TempDir()
	started := time.Date(2026, 5, 20, 1, 0, 0, 0, time.UTC)
	record := domain.RunRecord{IssueIdentifier: "CAG-75", Workspace: workspace, Status: "success", StartedAt: started}

	runPath, err := testManager().WriteRunRecord(workspace, record)
	if err != nil {
		t.Fatal(err)
	}
	evalPath, evaluation, err := testManager().WriteEvaluation(workspace, record)
	if err != nil {
		t.Fatal(err)
	}
	if runPath != RunRecordPath(workspace) || evalPath != EvaluationPath(workspace) || evaluation.Outcome != "fallback" {
		t.Fatalf("unexpected write result: %q %q %#v", runPath, evalPath, evaluation)
	}
	var writtenRun map[string]any
	if data, err := os.ReadFile(runPath); err != nil {
		t.Fatal(err)
	} else if err := json.Unmarshal(data, &writtenRun); err != nil {
		t.Fatal(err)
	}
	if writtenRun["schema_version"] != float64(CurrentArtifactSchemaVersion) || writtenRun["schema_source"] != ArtifactSchemaSourceCurrent {
		t.Fatalf("run record missing schema metadata: %#v", writtenRun)
	}
	if evaluation.SchemaVersion != CurrentArtifactSchemaVersion || evaluation.SchemaSource != ArtifactSchemaSourceCurrent {
		t.Fatalf("evaluation missing schema metadata: %#v", evaluation)
	}

	readRecord, readEvaluation, artifactTime, err := testManager().ReadBackfill(workspace, filepath.Dir(workspace))
	if err != nil {
		t.Fatal(err)
	}
	if readRecord.IssueIdentifier != "CAG-75" || readEvaluation.Outcome != "fallback" || !artifactTime.Equal(started) {
		t.Fatalf("unexpected backfill read: %#v %#v %s", readRecord, readEvaluation, artifactTime)
	}
}

func TestManagerReadBackfillTreatsUnversionedArtifactsAsLegacy(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(RunRecordPath(workspace), []byte(`{"issue_identifier":"CAG-75","workspace":"`+workspace+`","status":"success"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(EvaluationPath(workspace), []byte(`{"issue_identifier":"CAG-75","final_status":"success","outcome":"legacy"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, evaluation, _, err := testManager().ReadBackfill(workspace, filepath.Dir(workspace))
	if err != nil {
		t.Fatal(err)
	}
	if evaluation.SchemaVersion != CurrentArtifactSchemaVersion || evaluation.SchemaSource != ArtifactSchemaSourceLegacy || evaluation.Outcome != "legacy" {
		t.Fatalf("unexpected legacy evaluation metadata: %#v", evaluation)
	}
}

func TestManagerReadBackfillRejectsUnsupportedSchemaVersion(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(RunRecordPath(workspace), []byte(`{"schema_version":99,"issue_identifier":"CAG-75","status":"success"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := testManager().ReadBackfill(workspace, filepath.Dir(workspace)); err == nil || err.Error() != "unsupported .am-run.json schema_version 99" {
		t.Fatalf("expected unsupported schema version error, got %v", err)
	}
}

func TestManagerReadBackfillRejectsCorruptedSchemaVersion(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(RunRecordPath(workspace), []byte(`{"schema_version":"bad","issue_identifier":"CAG-75","status":"success"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := testManager().ReadBackfill(workspace, filepath.Dir(workspace)); err == nil {
		t.Fatal("expected corrupted schema version error")
	}
}

func TestRunArtifactSnapshotMapsArtifactFields(t *testing.T) {
	workspace := t.TempDir()
	started := time.Date(2026, 5, 20, 1, 0, 0, 0, time.UTC)
	ended := started.Add(time.Minute)
	record := domain.RunRecord{IssueIdentifier: "CAG-75", IssueID: "issue-id", Workspace: workspace, Status: "success", StartedAt: started, EndedAt: ended, PRURL: "https://github.com/acme/repo/pull/75", ReviewStatus: "passed", FeedbackHash: "feedback", BudgetExceeded: "ok"}
	evaluation := EvaluationArtifact{Outcome: "handoff_ready", MergeEligible: true, NextAction: "await_approval", FeedbackRetryCount: 2, RootCause: "root"}

	snapshot := RunArtifactSnapshot(workspace, record, evaluation, SnapshotOptions{BranchName: "am/CAG-75-workspace", BaseBranch: "main", Repository: "acme/repo", PRNumber: 75, ReviewOutputHash: "review", TerminalStatus: true})

	if snapshot.IssueKey != "CAG-75" || snapshot.Repository != "acme/repo" || snapshot.PRNumber != 75 || !snapshot.MergeEligible {
		t.Fatalf("unexpected snapshot identity: %#v", snapshot)
	}
	if snapshot.SchemaVersion != state.CurrentSchemaVersion || snapshot.ArtifactSchemaVersion != CurrentArtifactSchemaVersion || snapshot.ArtifactSchemaSource != ArtifactSchemaSourceCurrent {
		t.Fatalf("unexpected snapshot schema metadata: %#v", snapshot)
	}
	if snapshot.RunArtifactRef != RunRecordPath(workspace) || snapshot.EvaluationRef != EvaluationPath(workspace) || snapshot.ReviewOutputRef != EvaluationPath(workspace) {
		t.Fatalf("unexpected artifact refs: %#v", snapshot)
	}
	if snapshot.TerminalOutcome != "handoff_ready" || snapshot.TerminalReason != "root" || snapshot.FeedbackNextAction != "await_approval" || snapshot.RetryCount != 2 {
		t.Fatalf("unexpected snapshot classification: %#v", snapshot)
	}
}

func TestManagerReadBackfillMissingAndMalformedArtifacts(t *testing.T) {
	workspace := t.TempDir()
	if _, _, _, err := testManager().ReadBackfill(workspace, filepath.Dir(workspace)); err == nil || err.Error() != "missing .am-run.json" {
		t.Fatalf("expected missing run record error, got %v", err)
	}
	if err := os.WriteFile(RunRecordPath(workspace), []byte(`{"issue_identifier":"CAG-75","status":"success"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(EvaluationPath(workspace), []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := testManager().ReadBackfill(workspace, filepath.Dir(workspace)); err == nil {
		t.Fatal("expected malformed evaluation error")
	}
}

func TestManagerRepairPreservesUsageAndMarksManualMerge(t *testing.T) {
	workspace := t.TempDir()
	path := RunRecordPath(workspace)
	record := domain.RunRecord{IssueIdentifier: "CAG-75", Status: "success", PRURL: "https://github.com/acme/repo/pull/75", RuntimeUsage: &domain.Usage{TotalTokens: 42}}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	manager := testManager()
	manager.PRStateForURL = func(string) (string, bool, error) { return "MERGED", true, nil }

	changed, repaired, err := manager.Repair(path)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || repaired.Status != "merged" || repaired.OriginalStatus != "success" || repaired.ManualRepair != "pr_manually_merged" || repaired.RuntimeUsage.TotalTokens != 42 {
		t.Fatalf("unexpected repaired record: %#v", repaired)
	}
}

func TestFeedbackHashAndUsageParsingCompatibility(t *testing.T) {
	if FeedbackHash(" feedback\n") != FeedbackHash("feedback") {
		t.Fatal("expected feedback hash to trim surrounding whitespace")
	}
	usage := ParseUsage("noise\n" + `{"message":{"usage":{"totalTokens":0}}}` + "\n" + `{"message":{"usage":{"totalTokens":7,"cost":{"total":0.01}}}}`)
	if usage == nil || usage.TotalTokens != 7 || usage.TotalCost() != 0.01 {
		t.Fatalf("unexpected usage: %#v", usage)
	}
}

func TestParseUsageReturnsLastTokenUsageEvent(t *testing.T) {
	output := `plain log line
{"message":{"usage":{"input":10,"output":2,"cacheRead":3,"cacheWrite":0,"totalTokens":15,"cost":{"input":0.1,"output":0.2,"cacheRead":0.03,"cacheWrite":0,"total":0.33}}}}
{"message":{"usage":{"input":20,"output":4,"cacheRead":6,"cacheWrite":1,"totalTokens":31,"cost":{"total":0.44}}}}
`

	got := ParseUsage(output)
	if got == nil {
		t.Fatal("expected usage")
	}
	if got.TotalTokens != 31 || got.Input != 20 || got.Output != 4 || got.CacheRead != 6 || got.CacheWrite != 1 {
		t.Fatalf("unexpected usage: %+v", got)
	}
	if got.TotalCost() != 0.44 {
		t.Fatalf("unexpected cost: %v", got.TotalCost())
	}
}

func TestParseUsageIgnoresEmptyUsageEvents(t *testing.T) {
	output := `{"message":{"usage":{"input":10,"totalTokens":0}}}`
	if got := ParseUsage(output); got != nil {
		t.Fatalf("expected nil usage, got %+v", got)
	}
}

func TestParseUsageContinuesAfterLongJSONLLine(t *testing.T) {
	longLine := `{"message":{"content":"` + strings.Repeat("x", 70*1024) + `"}}`
	output := "noise\n" + longLine + "\n" + `{"message":{"usage":{"totalTokens":9,"cost":{"total":0.02}}}}`

	usage := ParseUsage(output)

	if usage == nil || usage.TotalTokens != 9 || usage.TotalCost() != 0.02 {
		t.Fatalf("unexpected usage after long line: %#v", usage)
	}
}
