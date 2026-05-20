package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// scenarioHarness is a lightweight fake-adapter harness for cross-module
// characterization tests. It intentionally lives in tests only: production
// orchestration still owns all Linear, GitHub, AgentRuntime, workspace, and
// SQLite side effects.
type scenarioHarness struct {
	t          *testing.T
	config     runnerConfig
	candidates []issue
	prs        map[string]*pullRequestSummary
	artifacts  map[string]artifactSummary
	doneIssues map[string]bool
	reads      []string
	mutations  []string
}

func newScenarioHarness(t *testing.T) *scenarioHarness {
	t.Helper()
	root := t.TempDir()
	return &scenarioHarness{
		t:          t,
		config:     testRunnerConfig(root),
		prs:        map[string]*pullRequestSummary{},
		artifacts:  map[string]artifactSummary{},
		doneIssues: map[string]bool{},
	}
}

func (h *scenarioHarness) withCandidate(identifier, state string, labels ...string) *scenarioHarness {
	candidate := testIssue(identifier, state)
	addLabels(&candidate, labels...)
	h.candidates = append(h.candidates, candidate)
	return h
}

func (h *scenarioHarness) withPR(identifier string, pr pullRequestSummary) *scenarioHarness {
	copy := pr
	h.prs[identifier] = &copy
	return h
}

func (h *scenarioHarness) withArtifact(identifier string, artifact artifactSummary) *scenarioHarness {
	h.artifacts[identifier] = artifact
	return h
}

func (h *scenarioHarness) withDoneWorkspace(identifier, status string) *scenarioHarness {
	workspace := filepath.Join(h.config.WorkspaceRoot, identifier)
	writeCleanRunArtifact(h.t, workspace, status)
	h.doneIssues[identifier] = true
	return h
}

func (h *scenarioHarness) read(adapter, operation string) {
	h.reads = append(h.reads, adapter+"."+operation)
}

func (h *scenarioHarness) mutate(adapter, operation string) {
	h.mutations = append(h.mutations, adapter+"."+operation)
}

func (h *scenarioHarness) explainDryRun() explainReport {
	h.read("linear", "candidates")
	h.read("github", "open_pull_requests")
	h.read("linear", "done_issues")
	h.read("workspace", "cleanup_scan")
	report, err := explain(h.config, h.candidates, h.prs, h.doneIssues)
	if err != nil {
		h.t.Fatal(err)
	}
	return report
}

func (h *scenarioHarness) reconcile() []reconciliationDecision {
	h.read("linear", "active_issues")
	h.read("github", "open_pull_requests")
	h.read("state", "artifact_summaries")
	return reconcileIssues(h.config, h.candidates, h.prs, h.artifacts)
}

func (h *scenarioHarness) simulateSuccessfulRun(identifier string) string {
	selection := explainCandidateSelection(h.config, h.candidates, h.prs)
	if selection.Selected != identifier {
		h.t.Fatalf("selected candidate = %q, want %q", selection.Selected, identifier)
	}
	h.mutate("linear", "move_to_running:"+identifier)
	h.mutate("agentruntime", "run:"+identifier)
	prURL := "https://github.com/weskor/pi-symphony/pull/900"
	h.mutate("github", "open_pr:"+identifier)
	h.mutate("state", "record_attempt:"+identifier)
	h.mutate("workspace", "export_run_artifact:"+identifier)
	return prURL
}

func (h *scenarioHarness) assertNoMutations() {
	h.t.Helper()
	if len(h.mutations) > 0 {
		h.t.Fatalf("unexpected mutations: %v", h.mutations)
	}
}

func TestScenarioHarnessExplainsCandidateMergeAndCleanupWithoutMutation(t *testing.T) {
	h := newScenarioHarness(t).
		withCandidate("CAG-90", "Ready for Agent").
		withCandidate("CAG-91", "Ready for Agent", "blocked").
		withCandidate("CAG-92", "Human Review").
		withPR("CAG-92", pullRequestSummary{URL: "https://github.com/weskor/pi-symphony/pull/92", HeadRefName: "symphony/CAG-92-workspace", ReviewDecision: "APPROVED", Mergeable: "CONFLICTING", MergeStateStatus: "DIRTY"}).
		withDoneWorkspace("CAG-93", "success")

	report := h.explainDryRun()
	h.assertNoMutations()

	if report.Next.Selected != "CAG-90" {
		t.Fatalf("selected candidate = %q, want CAG-90", report.Next.Selected)
	}
	if len(report.Next.Candidates) != 3 || report.Next.Candidates[1].Runnable || !strings.Contains(report.Next.Candidates[1].Reason, "blocked label") {
		t.Fatalf("candidate explanation = %+v", report.Next.Candidates)
	}
	if len(report.Merge) != 1 || report.Merge[0].CanMerge || !strings.Contains(report.Merge[0].Reason, "conflicts") {
		t.Fatalf("merge explanation = %+v", report.Merge)
	}
	if len(report.Cleanup) != 1 || !report.Cleanup[0].Eligible || report.Cleanup[0].Issue != "CAG-93" {
		t.Fatalf("cleanup explanation = %+v", report.Cleanup)
	}
	assertScenarioCalls(t, h.reads, "linear.candidates", "github.open_pull_requests", "linear.done_issues", "workspace.cleanup_scan")
}

func TestScenarioHarnessCharacterizesReconciliationSkipsBlockedAndOpenPRCandidates(t *testing.T) {
	h := newScenarioHarness(t).
		withCandidate("CAG-94", "Ready for Agent", "blocked").
		withCandidate("CAG-95", "Ready for Agent").
		withPR("CAG-95", pullRequestSummary{URL: "https://github.com/weskor/pi-symphony/pull/95", HeadRefName: "symphony/CAG-95-workspace", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN"}).
		withArtifact("CAG-96", artifactSummary{Issue: "CAG-96", HasArtifact: true, Status: "failed", NextAction: "inspect_run_log"})

	decisions := h.reconcile()
	h.assertNoMutations()

	if len(decisions) != 2 {
		t.Fatalf("decisions = %+v", decisions)
	}
	if decisions[0].CanRun || !containsString(decisions[0].Blockers, "issue has blocked label") {
		t.Fatalf("blocked decision = %+v", decisions[0])
	}
	if decisions[1].CanRun || len(decisions[1].Blockers) == 0 || decisions[1].PR == nil {
		t.Fatalf("open PR decision = %+v", decisions[1])
	}
	assertScenarioCalls(t, h.reads, "linear.active_issues", "github.open_pull_requests", "state.artifact_summaries")
}

func TestScenarioHarnessRecordsSingleDispatchForSuccessfulRunScenario(t *testing.T) {
	h := newScenarioHarness(t).withCandidate("CAG-97", "Ready for Agent")

	prURL := h.simulateSuccessfulRun("CAG-97")

	if prURL == "" {
		t.Fatal("successful run scenario did not return a PR URL")
	}
	assertScenarioCalls(t, h.mutations,
		"linear.move_to_running:CAG-97",
		"agentruntime.run:CAG-97",
		"github.open_pr:CAG-97",
		"state.record_attempt:CAG-97",
		"workspace.export_run_artifact:CAG-97",
	)
	if countScenarioCall(h.mutations, "agentruntime.run:CAG-97") != 1 {
		t.Fatalf("agent runtime dispatch count = %d, want 1; calls=%v", countScenarioCall(h.mutations, "agentruntime.run:CAG-97"), h.mutations)
	}
}

func assertScenarioCalls(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("calls = %v, want %v", got, want)
		}
	}
}

func countScenarioCall(calls []string, want string) int {
	count := 0
	for _, call := range calls {
		if call == want {
			count++
		}
	}
	return count
}
