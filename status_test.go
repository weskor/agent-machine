package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weskor/pi-symphony/internal/state"
)

func TestSummarizeArtifactsReportsUsageAndTerminalStatus(t *testing.T) {
	lines := summarizeArtifacts([]artifactSummary{{
		Issue:             "CAG-12",
		Status:            "success",
		Review:            "passed",
		PRURL:             "https://github.com/pennywise-investments/compound-web/pull/402",
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

func TestSummarizeStateStoreReportsHealthyDB(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	workspaceRoot := filepath.Join(root, ".symphony", "workspaces")
	if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(ctx, state.DefaultDBPath(workspaceRoot))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := store.UpsertRunArtifact(ctx, state.RunArtifactSnapshot{IssueKey: "CAG-62", Attempt: 1, BranchName: "symphony/CAG-62-workspace", BaseBranch: "main", Status: "success", Repository: "weskor/pi-symphony", PRNumber: 62, PRURL: "https://github.com/weskor/pi-symphony/pull/62", ReviewStatus: "passed", TerminalOutcome: "handoff_ready"}); err != nil {
		t.Fatalf("UpsertRunArtifact() error = %v", err)
	}
	if err := store.UpsertDaemonHeartbeat(ctx, state.DaemonHeartbeat{ProcessID: "host:123", LaneName: "merge", WorkflowPath: "/repo/WORKFLOW.md", CycleNumber: 1}); err != nil {
		t.Fatalf("UpsertDaemonHeartbeat() error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	joined := strings.Join(summarizeStateStore(workspaceRoot), "\n")
	for _, expected := range []string{"SQLite state path: " + state.DefaultDBPath(workspaceRoot), "SQLite state health: healthy", "schema_version=2", "journal_mode=wal", "busy_timeout_ms=5000", "issue_attempts=1", "pr_mappings=1", "review_states=1", "terminal_outcomes=1", "daemon_heartbeats=1", "cleanup_states=0", "events=1"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("expected %q in %q", expected, joined)
		}
	}
}

func TestSummarizeStateStoreReportsMissingDB(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), ".symphony", "workspaces")
	joined := strings.Join(summarizeStateStore(workspaceRoot), "\n")
	for _, expected := range []string{"SQLite state path: " + state.DefaultDBPath(workspaceRoot), "SQLite state health: missing", "action=run pi-symphony once"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("expected %q in %q", expected, joined)
		}
	}
}

func TestSummarizeStateStoreReportsUnopenableDB(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), ".symphony", "workspaces")
	if err := os.MkdirAll(state.DefaultDBPath(workspaceRoot), 0o755); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(summarizeStateStore(workspaceRoot), "\n")
	for _, expected := range []string{"SQLite state path: " + state.DefaultDBPath(workspaceRoot), "SQLite state health: degraded", "action=check state DB path and permissions"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("expected %q in %q", expected, joined)
		}
	}
}

func TestSummarizePRAnnotatesArtifactGate(t *testing.T) {
	artifact := artifactSummary{HasEvaluation: true, Outcome: "review_failed", NextAction: "repair_review_findings_before_handoff", ShouldRetry: true, OperatorAttention: true}
	line := summarizePR(pullRequestSummary{Number: 402, URL: "https://github.com/pennywise-investments/compound-web/pull/402", HeadRefName: "symphony/CAG-12-workspace", Mergeable: "MERGEABLE", ReviewDecision: "CHANGES_REQUESTED"}, &artifact)
	for _, expected := range []string{"#402", "artifact_gate=outcome:review_failed", "merge_eligible:false", "retry:true", "attention:true", "next:repair_review_findings_before_handoff"} {
		if !strings.Contains(line, expected) {
			t.Fatalf("expected %q in %q", expected, line)
		}
	}
}

func TestSummarizeReadyReconciliationReportsTerminalReadyArtifacts(t *testing.T) {
	var ready issue
	ready.Identifier = "CAG-12"
	ready.State.Name = "Ready for Agent"
	lines := summarizeReadyReconciliation([]issue{ready}, map[string]artifactSummary{"CAG-12": {Issue: "CAG-12", Status: "success", Outcome: "handoff_ready", NextAction: "await_approval_and_green_checks", Cleanable: true}}, "Ready for Agent")
	if len(lines) != 2 {
		t.Fatalf("expected header and reconciliation line, got %#v", lines)
	}
	for _, expected := range []string{"Actionable reconciliation:", "Reconcile Ready issue", "CAG-12", "status=success", "outcome=handoff_ready", "next=await_approval_and_green_checks"} {
		if !strings.Contains(strings.Join(lines, "\n"), expected) {
			t.Fatalf("expected %q in %#v", expected, lines)
		}
	}
}

func TestSummarizeRunningReconciliationReportsInProgressWithoutActiveLock(t *testing.T) {
	var running issue
	running.Identifier = "CAG-38"
	running.State.Name = "In Progress"
	config := runnerConfig{WorkspaceRoot: t.TempDir(), RunningState: "In Progress"}

	lines := summarizeRunningReconciliation([]issue{running}, map[string]artifactSummary{"CAG-38": {Issue: "CAG-38", Status: "success", Outcome: "handoff_ready", NextAction: "await_approval_and_green_checks", Cleanable: true}}, config)
	joined := strings.Join(lines, "\n")
	for _, expected := range []string{"Actionable In Progress reconciliation:", "Reconcile In Progress issue with no active run lock", "CAG-38", "artifact_status=success", "outcome=handoff_ready", "next=await_approval_and_green_checks"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("expected %q in %#v", expected, lines)
		}
	}
}

func TestSummarizeRunningReconciliationIgnoresActiveLock(t *testing.T) {
	root := t.TempDir()
	var running issue
	running.ID = "issue-id"
	running.Identifier = "CAG-38"
	running.State.Name = "In Progress"
	workspace := filepath.Join(root, running.Identifier)
	if _, release, err := acquireRunLock(workspace, &running, expectedWorkspaceBranch(running.Identifier), nowFixture()); err != nil {
		t.Fatal(err)
	} else {
		defer release()
	}

	if lines := summarizeRunningReconciliation([]issue{running}, nil, runnerConfig{WorkspaceRoot: root, RunningState: "In Progress"}); len(lines) != 0 {
		t.Fatalf("expected active lock to suppress reconciliation, got %#v", lines)
	}
}

func TestReconcileIssuesUsesOpenPRMapping(t *testing.T) {
	config := testRunnerConfig(t.TempDir())
	config.HandoffState = "Human Review"
	config.BaseBranch = "develop"
	candidate := testIssue("CAG-44", "Human Review")
	pr := pullRequestSummary{Number: 444, URL: "https://github.com/pennywise-investments/compound-web/pull/444", BaseRefName: "develop", HeadRefName: expectedWorkspaceBranch("CAG-44"), Author: prAuthor{Login: githubAppPRAuthorLogin}, ReviewDecision: "COMMENTED"}

	decisions := reconcileIssues(config, []issue{candidate}, indexPRsByIssue([]pullRequestSummary{pr}), nil)

	if len(decisions) != 1 || decisions[0].PR == nil || decisions[0].PR.Number != 444 || decisions[0].Lifecycle != lifecycleHandoffReady {
		t.Fatalf("expected open PR mapping to drive handoff reconciliation, got %#v", decisions)
	}
}

func TestRunningReconciliationReportsDeletedWorkspace(t *testing.T) {
	candidate := testIssue("CAG-45", "In Progress")
	config := runnerConfig{WorkspaceRoot: t.TempDir(), RunningState: "In Progress"}

	lines := summarizeRunningReconciliation([]issue{candidate}, nil, config)

	joined := strings.Join(lines, "\n")
	for _, expected := range []string{"Actionable In Progress reconciliation:", "CAG-45", "artifact_status=missing", "next=restart_runner_or_move_issue_ready"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("expected %q in %#v", expected, lines)
		}
	}
}

func TestReadyReconciliationReportsStaleTerminalArtifactWithoutWorkspaceRead(t *testing.T) {
	candidate := testIssue("CAG-46", "Ready for Agent")
	config := runnerConfig{WorkspaceRoot: t.TempDir(), ReadyState: "Ready for Agent"}
	artifacts := map[string]artifactSummary{"CAG-46": {Issue: "CAG-46", Status: "success", Outcome: "handoff_ready", NextAction: "await_approval_and_green_checks", Cleanable: true, HasArtifact: true, PRURL: "https://github.com/pennywise-investments/compound-web/pull/446"}}

	decisions := reconcileIssues(config, []issue{candidate}, nil, artifacts)
	lines := summarizeReadyReconciliationDecisions(decisions, config.ReadyState)

	joined := strings.Join(lines, "\n")
	for _, expected := range []string{"Reconcile Ready issue with terminal artifact", "CAG-46", "status=success", "next=await_approval_and_green_checks"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("expected %q in %#v", expected, lines)
		}
	}
}

func TestReadyReconciliationReportsSQLiteArtifactConflict(t *testing.T) {
	root := t.TempDir()
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertRunArtifact(context.Background(), state.RunArtifactSnapshot{IssueKey: "CAG-116", Attempt: 1, BranchName: expectedWorkspaceBranch("CAG-116"), Status: "success", TerminalOutcome: "handoff_ready"}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	candidate := testIssue("CAG-116", "Ready for Agent")
	config := runnerConfig{WorkspaceRoot: root, ReadyState: "Ready for Agent"}
	artifacts := map[string]artifactSummary{"CAG-116": {Issue: "CAG-116", Status: "review_failed", Outcome: "review_failed", Cleanable: true, HasArtifact: true}}

	lines := summarizeReadyReconciliationDecisions(reconcileIssues(config, []issue{candidate}, nil, artifacts), config.ReadyState)

	if !strings.Contains(strings.Join(lines, "\n"), "reconciliation_needed=true") {
		t.Fatalf("expected status reconciliation-needed marker, got %#v", lines)
	}
}

func TestReadyReconciliationReportsDurableStateWithoutWorkspaceArtifact(t *testing.T) {
	root := t.TempDir()
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertRunArtifact(context.Background(), state.RunArtifactSnapshot{IssueKey: "CAG-117", Attempt: 1, BranchName: expectedWorkspaceBranch("CAG-117"), Status: "success", Repository: "weskor/pi-symphony", PRNumber: 117, PRURL: "https://github.com/weskor/pi-symphony/pull/117", TerminalOutcome: "handoff_ready"}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	candidate := testIssue("CAG-117", "Ready for Agent")
	config := runnerConfig{WorkspaceRoot: root, ReadyState: "Ready for Agent"}

	lines := summarizeReadyReconciliationDecisions(reconcileIssues(config, []issue{candidate}, nil, nil), config.ReadyState)
	joined := strings.Join(lines, "\n")

	for _, expected := range []string{"Reconcile Ready issue from durable state", "CAG-117", "status=success", "pull/117", "next=reconcile_missing_or_closed_pr_mapping", "reconciliation_needed=true"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("expected %q in %#v", expected, lines)
		}
	}
}

func TestSummarizeArtifactsReportsMissingArtifact(t *testing.T) {
	lines := summarizeArtifacts([]artifactSummary{{Issue: "CAG-3"}})
	if len(lines) != 1 || lines[0] != "CAG-3 missing artifact" {
		t.Fatalf("unexpected summary: %#v", lines)
	}
}

func TestWorkspaceArtifactSummariesSkipsHiddenLockDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".pi-symphony-locks"), 0o755); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "CAG-1")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".pi-symphony-run.json"), []byte(`{"issue_identifier":"CAG-1","issue_url":"https://linear.app/wessismore/issue/CAG-1/test","status":"success"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	summaries, err := workspaceArtifactSummaries(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[0].Issue != "CAG-1" {
		t.Fatalf("expected only CAG workspace summary, got %#v", summaries)
	}
}

func TestSummarizeArtifactsReportsNone(t *testing.T) {
	lines := summarizeArtifacts(nil)
	if len(lines) != 1 || lines[0] != "none" {
		t.Fatalf("unexpected empty summary: %#v", lines)
	}
}

func TestSummarizeArtifactsReportsRecurringFrictionWithLimit(t *testing.T) {
	lines := summarizeRecurringFriction([]artifactSummary{
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
