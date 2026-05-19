package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
