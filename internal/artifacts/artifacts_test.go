package artifacts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/weskor/pi-symphony/internal/domain"
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

	runPath, evalPath, evaluation, err := testManager().WriteRunRecord(workspace, record)
	if err != nil {
		t.Fatal(err)
	}
	if runPath != RunRecordPath(workspace) || evalPath != EvaluationPath(workspace) || evaluation.Outcome != "fallback" {
		t.Fatalf("unexpected write result: %q %q %#v", runPath, evalPath, evaluation)
	}

	readRecord, readEvaluation, artifactTime, err := testManager().ReadBackfill(workspace, filepath.Dir(workspace))
	if err != nil {
		t.Fatal(err)
	}
	if readRecord.IssueIdentifier != "CAG-75" || readEvaluation.Outcome != "fallback" || !artifactTime.Equal(started) {
		t.Fatalf("unexpected backfill read: %#v %#v %s", readRecord, readEvaluation, artifactTime)
	}
}

func TestManagerReadBackfillMissingAndMalformedArtifacts(t *testing.T) {
	workspace := t.TempDir()
	if _, _, _, err := testManager().ReadBackfill(workspace, filepath.Dir(workspace)); err == nil || err.Error() != "missing .pi-symphony-run.json" {
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
	record := domain.RunRecord{IssueIdentifier: "CAG-75", Status: "success", PRURL: "https://github.com/acme/repo/pull/75", PiUsage: &domain.Usage{TotalTokens: 42}}
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
	if !changed || repaired.Status != "merged" || repaired.OriginalStatus != "success" || repaired.ManualRepair != "pr_manually_merged" || repaired.PiUsage.TotalTokens != 42 {
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
