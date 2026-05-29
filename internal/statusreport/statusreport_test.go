package statusreport

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weskor/agent-machine/internal/state"
)

func TestSummarizeStateStoreReportsHealthyDB(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	workspaceRoot := filepath.Join(root, ".am", "workspaces")
	if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(ctx, state.DefaultDBPath(workspaceRoot))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := store.UpsertRunArtifact(ctx, state.RunArtifactSnapshot{IssueKey: "CAG-62", Attempt: 1, BranchName: "am/CAG-62-workspace", BaseBranch: "main", Status: "success", Repository: "weskor/agent-machine", PRNumber: 62, PRURL: "https://github.com/weskor/agent-machine/pull/62", ReviewStatus: "passed", TerminalOutcome: "handoff_ready"}); err != nil {
		t.Fatalf("UpsertRunArtifact() error = %v", err)
	}
	if err := store.UpsertDaemonHeartbeat(ctx, state.DaemonHeartbeat{ProcessID: "host:123", LaneName: "merge", WorkflowPath: "/repo/am.yaml", CycleNumber: 1}); err != nil {
		t.Fatalf("UpsertDaemonHeartbeat() error = %v", err)
	}
	if err := store.UpsertWorkerTask(ctx, state.WorkerTask{TaskKey: "review:CAG-62:1", Role: "review", IssueKey: "CAG-62", Attempt: 1, Status: "queued", Priority: 7}); err != nil {
		t.Fatalf("UpsertWorkerTask() error = %v", err)
	}
	if err := store.UpsertWorkerResult(ctx, state.WorkerResult{TaskKey: "review:CAG-62:1", Role: "review", LaneName: "review", IssueKey: "CAG-62", Attempt: 1, Status: "failed", Reason: "worker_error", Error: "checks unavailable"}); err != nil {
		t.Fatalf("UpsertWorkerResult() error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	joined := strings.Join(SummarizeStateStore(workspaceRoot), "\n")
	for _, expected := range []string{"SQLite state path: " + state.DefaultDBPath(workspaceRoot), "SQLite state health: healthy", "schema_version=8", "journal_mode=wal", "busy_timeout_ms=5000", "issue_attempts=1", "pr_mappings=1", "review_states=1", "terminal_outcomes=1", "daemon_heartbeats=1", "cleanup_states=0", "worker_tasks=1", "worker_results=1", "worker_payload_refs=0", "pr_handoff_intents=0", "events=3", "SQLite active lanes: total=1", "lane=merge", "SQLite worker tasks: total=1 review:queued=1", "SQLite worker results: total=1 review:failed=1", "task=review:CAG-62:1", "role=review", "status=queued", "reason=worker_error"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("expected %q in %q", expected, joined)
		}
	}
}

func TestSummarizeArtifactsReportsUsageAndTerminalStatus(t *testing.T) {
	lines := SummarizeArtifacts([]ArtifactSummary{{
		Issue:             "CAG-12",
		Status:            "success",
		Review:            "passed",
		PRURL:             "https://github.com/weskor/agent-machine/pull/402",
		Outcome:           "handoff_ready",
		RootCause:         "none",
		NextAction:        "await_approval_and_green_checks",
		ChecksStatus:      "unknown_post_run",
		TotalTokens:       79398,
		TotalCost:         0.081882,
		HasArtifact:       true,
		HasEvaluation:     true,
		Cleanable:         true,
		MergeEligible:     true,
		ShouldRetry:       false,
		OperatorAttention: false,
		TicketContract:    []string{"implementation_prompt_required_five_section_ticket_contract"},
	}})
	if len(lines) < 1 {
		t.Fatalf("expected at least one summary line, got %d", len(lines))
	}
	line := lines[0]
	for _, expected := range []string{"CAG-12", "class=completed", "status=success", "review=passed", "tokens=79398", "historical", "pull/402", "outcome=handoff_ready", "root=none", "next=await_approval_and_green_checks", "retry=false", "attention=false", "merge_eligible=true", "checks=unknown_post_run", "ticket_contract=implementation_prompt_required_five_section_ticket_contract"} {
		if !strings.Contains(line, expected) {
			t.Fatalf("expected %q in %q", expected, line)
		}
	}
}

func TestSummarizeArtifactsReportsMissingArtifact(t *testing.T) {
	lines := SummarizeArtifacts([]ArtifactSummary{{Issue: "CAG-3"}})
	if len(lines) != 1 || lines[0] != "CAG-3 missing artifact" {
		t.Fatalf("unexpected summary: %#v", lines)
	}
}

func TestSummarizeArtifactsReportsNone(t *testing.T) {
	lines := SummarizeArtifacts(nil)
	if len(lines) != 1 || lines[0] != "none" {
		t.Fatalf("unexpected empty summary: %#v", lines)
	}
}

func TestSummarizeArtifactsReportsRecurringFrictionWithLimit(t *testing.T) {
	lines := SummarizeRecurringFriction([]ArtifactSummary{
		{Issue: "CAG-1", Outcome: "needs_info", RootCause: "missing_requirements", Frictions: []string{"needs_info", "review_failed"}},
		{Issue: "CAG-2", Outcome: "needs_info", RootCause: "missing_requirements", Frictions: []string{"needs_info", "missing_pr_url"}},
		{Issue: "CAG-3", Outcome: "blocked", Frictions: []string{"check_failure_or_pending"}},
	}, 2)
	if len(lines) != 1 {
		t.Fatalf("expected one recurring friction line, got %#v", lines)
	}
	line := lines[0]
	for _, expected := range []string{"Recurring friction:", "needs_info=2", "outcome:needs_info=2"} {
		if !strings.Contains(line, expected) {
			t.Fatalf("expected %q in %q", expected, line)
		}
	}
	if strings.Contains(line, "review_failed") || strings.Contains(line, "missing_pr_url") || strings.Contains(line, "check_failure_or_pending") {
		t.Fatalf("expected truncation to two signals, got %q", line)
	}
}

func TestSummarizePullRequestAnnotatesArtifactGate(t *testing.T) {
	artifact := ArtifactSummary{HasEvaluation: true, Outcome: "review_failed", NextAction: "repair_review_findings_before_handoff", ShouldRetry: true, OperatorAttention: true}
	line := SummarizePullRequest(PullRequestSummary{Number: 402, URL: "https://github.com/weskor/agent-machine/pull/402", HeadRefName: "am/CAG-12-workspace", Mergeable: "MERGEABLE", ReviewDecision: "CHANGES_REQUESTED"}, &artifact)
	for _, expected := range []string{"#402", "artifact_gate=outcome:review_failed", "merge_eligible:false", "retry:true", "attention:true", "next:repair_review_findings_before_handoff"} {
		if !strings.Contains(line, expected) {
			t.Fatalf("expected %q in %q", expected, line)
		}
	}
}

func TestSummarizePullRequestReportsChecksAndConflicts(t *testing.T) {
	line := SummarizePullRequest(PullRequestSummary{Number: 403, URL: "https://github.com/weskor/agent-machine/pull/403", HeadRefName: "am/CAG-13-workspace", Mergeable: "CONFLICTING", MergeStateStatus: "DIRTY", ReviewDecision: "APPROVED"}, nil)
	for _, expected := range []string{"#403", "checks=pending/failing", "merge=conflicting", "has conflicts with the base branch", "artifact_gate=unknown"} {
		if !strings.Contains(line, expected) {
			t.Fatalf("expected %q in %q", expected, line)
		}
	}
}

func TestSummarizeStateStoreReportsRecentEvents(t *testing.T) {
	ctx := context.Background()
	workspaceRoot := filepath.Join(t.TempDir(), ".am", "workspaces")
	store, err := state.Open(ctx, state.DefaultDBPath(workspaceRoot))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(ctx, state.EventInput{IssueKey: "CAG-104", Source: "test", Type: state.EventMergeBlocked, Payload: map[string]any{"reason": "checks"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	joined := strings.Join(SummarizeStateStore(workspaceRoot), "\n")
	for _, expected := range []string{"SQLite recent events:", "merge_blocked", "issue=CAG-104", "source=test"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("expected %q in %q", expected, joined)
		}
	}
}

func TestSummarizeStateStoreReportsMissingDB(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), ".am", "workspaces")
	joined := strings.Join(SummarizeStateStore(workspaceRoot), "\n")
	for _, expected := range []string{"SQLite state path: " + state.DefaultDBPath(workspaceRoot), "SQLite state health: missing", "action=run am start"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("expected %q in %q", expected, joined)
		}
	}
}

func TestSummarizeStateStoreReportsUnopenableDB(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), ".am", "workspaces")
	if err := os.MkdirAll(state.DefaultDBPath(workspaceRoot), 0o755); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(SummarizeStateStore(workspaceRoot), "\n")
	for _, expected := range []string{"SQLite state path: " + state.DefaultDBPath(workspaceRoot), "SQLite state health: degraded", "action=check state DB path and permissions"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("expected %q in %q", expected, joined)
		}
	}
}
