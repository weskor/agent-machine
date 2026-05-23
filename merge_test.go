package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sh "github.com/weskor/pi-symphony/internal/shell"
	"github.com/weskor/pi-symphony/internal/state"
)

func TestIssueIdentifierFromBranch(t *testing.T) {
	tests := map[string]string{
		"cag-12-docs-note":     "CAG-12",
		"feature/CAG_34_scope": "CAG-34",
		"no-issue":             "",
	}
	for branch, want := range tests {
		if got := issueIdentifierFromBranch(branch); got != want {
			t.Fatalf("issueIdentifierFromBranch(%q) = %q, want %q", branch, got, want)
		}
	}
}

func TestSymphonyPRsFiltersUnrelatedBranches(t *testing.T) {
	prs := []pullRequestSummary{
		{Number: 361, HeadRefName: "develop"},
		{Number: 337, HeadRefName: "feat/org-obp-merchant-auto-registration"},
		{Number: 402, HeadRefName: "cag-12-pi-symphony-loop-tests"},
		{Number: 400, HeadRefName: "feature/CAG_11_workflow_parser"},
	}

	got := symphonyPRs(prs)
	if len(got) != 2 {
		t.Fatalf("symphonyPRs returned %d PRs, want 2", len(got))
	}
	if got[0].Number != 402 || got[1].Number != 400 {
		t.Fatalf("symphonyPRs returned PRs %#v, want only CAG-owned PRs", got)
	}
}

func TestChecksPassed(t *testing.T) {
	checks := []statusCheck{
		{Typename: "CheckRun", Status: "COMPLETED", Conclusion: "SUCCESS", Name: "lint"},
		{Typename: "StatusContext", State: "SUCCESS", Context: "Vercel"},
	}
	if !checksPassed(checks) {
		t.Fatal("expected checks to pass")
	}

	checks[0].Conclusion = "FAILURE"
	if checksPassed(checks) {
		t.Fatal("expected failed check run to block")
	}

	if checksPassed(nil) {
		t.Fatal("expected empty checks to block")
	}
}

func TestWorkspaceLockedOrModifiedIgnoresEvidenceArtifacts(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-24")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := sh.Run("git init && git config user.email test@example.com && git config user.name Test", workspace); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".pi-symphony-prompt.md"), []byte("active"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".pi-symphony-review-prompt.md"), []byte("review"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".pi-symphony-run.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".pi-symphony-evaluation.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	legacyDebugPath := filepath.Join(workspace, ".pi-symphony-debug", "implementation-raw.log")
	if err := os.MkdirAll(filepath.Dir(legacyDebugPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyDebugPath, []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}

	locked, reason := workspaceLockedOrModified(root, "CAG-24", "cag-24-test")
	if locked || reason != "" {
		t.Fatalf("expected evidence-only workspace to be mergeable, got locked=%v reason=%q", locked, reason)
	}

	if err := os.WriteFile(filepath.Join(workspace, "unexpected.txt"), []byte("dirty"), 0o600); err != nil {
		t.Fatal(err)
	}
	locked, reason = workspaceLockedOrModified(root, "CAG-24", "cag-24-test")
	if !locked || !strings.Contains(reason, "uncommitted") {
		t.Fatalf("expected unknown dirty file to block merge, got locked=%v reason=%q", locked, reason)
	}
}

func TestRunArtifactMergeBlockReason(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-28")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	record := testRunRecord("review_failed", "https://github.com/pennywise-investments/compound-web/pull/426")
	record.IssueIdentifier = "CAG-28"
	record.ReviewStatus = "failed"
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".pi-symphony-run.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	if got := runArtifactMergeBlockReason(root, "CAG-28", record.PRURL); !strings.Contains(got, "review_failed") {
		t.Fatalf("expected review_failed artifact to block merge, got %q", got)
	}

	record.Status = "success"
	record.ReviewStatus = "failed"
	record.ReviewClassification = reviewClassificationMissingEvidenceOnly
	data, err = json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".pi-symphony-run.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := runArtifactMergeBlockReason(root, "CAG-28", record.PRURL); got != "" {
		t.Fatalf("expected approved missing-evidence-only artifact to allow merge, got %q", got)
	}

	record.Status = "success"
	record.ReviewStatus = "passed"
	record.ReviewClassification = ""
	data, err = json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".pi-symphony-run.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := runArtifactMergeBlockReason(root, "CAG-28", record.PRURL); got != "" {
		t.Fatalf("expected passing artifact to allow merge, got %q", got)
	}
}

func TestChecksBlockReason(t *testing.T) {
	tests := []struct {
		name   string
		checks []statusCheck
		want   string
	}{
		{
			name: "all checks successful",
			checks: []statusCheck{
				{Typename: "CheckRun", Status: "COMPLETED", Conclusion: "SUCCESS", Name: "build"},
				{Typename: "StatusContext", State: "SUCCESS", Context: "Vercel"},
			},
		},
		{
			name:   "failed check blocks",
			checks: []statusCheck{{Typename: "CheckRun", Status: "COMPLETED", Conclusion: "FAILURE", Name: "build"}},
			want:   `check run "build" is status=COMPLETED conclusion=FAILURE`,
		},
		{
			name:   "pending check blocks",
			checks: []statusCheck{{Typename: "CheckRun", Status: "IN_PROGRESS", Conclusion: "", Name: "build"}},
			want:   `check run "build" is status=IN_PROGRESS conclusion=UNKNOWN`,
		},
		{
			name:   "canceled check blocks",
			checks: []statusCheck{{Typename: "CheckRun", Status: "COMPLETED", Conclusion: "CANCELLED", Name: "build"}},
			want:   `check run "build" is status=COMPLETED conclusion=CANCELLED`,
		},
		{
			name:   "timed out check blocks",
			checks: []statusCheck{{Typename: "CheckRun", Status: "COMPLETED", Conclusion: "TIMED_OUT", Name: "build"}},
			want:   `check run "build" is status=COMPLETED conclusion=TIMED_OUT`,
		},
		{
			name:   "action required check blocks",
			checks: []statusCheck{{Typename: "CheckRun", Status: "COMPLETED", Conclusion: "ACTION_REQUIRED", Name: "build"}},
			want:   `check run "build" is status=COMPLETED conclusion=ACTION_REQUIRED`,
		},
		{
			name:   "neutral check blocks",
			checks: []statusCheck{{Typename: "CheckRun", Status: "COMPLETED", Conclusion: "NEUTRAL", Name: "build"}},
			want:   `check run "build" is status=COMPLETED conclusion=NEUTRAL`,
		},
		{
			name:   "pending status context blocks",
			checks: []statusCheck{{Typename: "StatusContext", State: "PENDING", Context: "Vercel"}},
			want:   `status context "Vercel" is state=PENDING`,
		},
		{
			name:   "unknown check shape blocks",
			checks: []statusCheck{{Typename: "MysteryCheck", Name: "deploy"}},
			want:   `unknown status check shape "MysteryCheck" for "deploy"`,
		},
		{
			name: "no checks blocks",
			want: "no status checks were reported by GitHub",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checksBlockReason(tt.checks)
			if got != tt.want {
				t.Fatalf("checksBlockReason() = %q, want %q", got, tt.want)
			}
			if (got == "") != checksPassed(tt.checks) {
				t.Fatalf("checksPassed mismatch for reason %q", got)
			}
		})
	}
}

func TestMergeGateBlockReason(t *testing.T) {
	greenChecks := []statusCheck{{Typename: "CheckRun", Status: "COMPLETED", Conclusion: "SUCCESS", Name: "build"}}
	tests := []struct {
		name string
		pr   pullRequestSummary
		want string
	}{
		{name: "mergeable with green checks allowed", pr: pullRequestSummary{Mergeable: "MERGEABLE", StatusCheckRollup: greenChecks}},
		{name: "conflict blocks", pr: pullRequestSummary{HeadRefName: "symphony/CAG-18", Mergeable: "CONFLICTING", MergeStateStatus: "DIRTY", StatusCheckRollup: greenChecks}, want: "has conflicts with the base branch"},
		{name: "unknown mergeable blocks", pr: pullRequestSummary{HeadRefName: "symphony/CAG-18", Mergeable: "UNKNOWN", StatusCheckRollup: greenChecks}, want: "mergeable=UNKNOWN"},
		{name: "missing mergeable blocks", pr: pullRequestSummary{HeadRefName: "symphony/CAG-18", StatusCheckRollup: greenChecks}, want: "mergeable=UNKNOWN"},
		{name: "check blocks", pr: pullRequestSummary{Mergeable: "MERGEABLE", StatusCheckRollup: []statusCheck{{Typename: "StatusContext", State: "ERROR", Context: "Vercel"}}}, want: `status context "Vercel" is state=ERROR`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergeGateBlockReason(tt.pr)
			if tt.want == "" && got != "" {
				t.Fatalf("mergeGateBlockReason() = %q, want allowed", got)
			}
			if tt.want != "" && !strings.Contains(got, tt.want) {
				t.Fatalf("mergeGateBlockReason() = %q, want to contain %q", got, tt.want)
			}
		})
	}
}

func TestEvaluateMergeGateBlockers(t *testing.T) {
	greenChecks := []statusCheck{{Typename: "CheckRun", Status: "COMPLETED", Conclusion: "SUCCESS", Name: "build"}}
	basePR := pullRequestSummary{
		Number:            470,
		URL:               "https://github.com/weskor/pi-symphony/pull/470",
		BaseRefName:       "main",
		HeadRefName:       "symphony/CAG-70-workspace",
		Author:            prAuthor{Login: "app/compound-symphony-bot"},
		Mergeable:         "MERGEABLE",
		MergeStateStatus:  "CLEAN",
		ReviewDecision:    "APPROVED",
		StatusCheckRollup: greenChecks,
	}
	tests := []struct {
		name       string
		mutatePR   func(*pullRequestSummary)
		artifact   runRecord
		wantCode   string
		wantReason string
		wantOK     bool
	}{
		{name: "eligible", wantOK: true},
		{name: "author invariant", mutatePR: func(pr *pullRequestSummary) { pr.Author.Login = "octocat" }, wantCode: "pr_invariant", wantReason: "PR author"},
		{name: "check failure", mutatePR: func(pr *pullRequestSummary) {
			pr.StatusCheckRollup = []statusCheck{{Typename: "CheckRun", Status: "COMPLETED", Conclusion: "FAILURE", Name: "build"}}
		}, wantCode: "status_checks", wantReason: `check run "build"`},
		{name: "merge conflicts", mutatePR: func(pr *pullRequestSummary) { pr.Mergeable = "CONFLICTING"; pr.MergeStateStatus = "DIRTY" }, wantCode: "merge_conflict", wantReason: "has conflicts with the base branch"},
		{name: "branch mapping", mutatePR: func(pr *pullRequestSummary) { pr.HeadRefName = "symphony/CAG-71-workspace" }, wantCode: "pr_invariant", wantReason: "PR head branch"},
		{name: "review decision", mutatePR: func(pr *pullRequestSummary) { pr.ReviewDecision = "REVIEW_REQUIRED" }, wantCode: "review_decision", wantReason: "reviewDecision=REVIEW_REQUIRED"},
		{name: "artifact gate", artifact: func() runRecord {
			r := testRunRecord("review_failed", basePR.URL)
			r.IssueIdentifier = "CAG-70"
			r.ReviewStatus = "failed"
			return r
		}(), wantCode: "run_artifact", wantReason: "run status is review_failed"},
		{name: "approved missing evidence only artifact", artifact: func() runRecord {
			r := testRunRecord("success", basePR.URL)
			r.IssueIdentifier = "CAG-70"
			r.ReviewStatus = "failed"
			r.ReviewClassification = reviewClassificationMissingEvidenceOnly
			return r
		}(), wantOK: true},
		{name: "behavior review failure remains blocked", artifact: func() runRecord {
			r := testRunRecord("success", basePR.URL)
			r.IssueIdentifier = "CAG-70"
			r.ReviewStatus = "failed"
			r.ReviewClassification = reviewClassificationBehaviorSpecBlocker
			return r
		}(), wantCode: "run_artifact", wantReason: "review status is failed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			workspace := filepath.Join(root, "CAG-70")
			if err := os.MkdirAll(workspace, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := sh.Run("git init && git config user.email test@example.com && git config user.name Test", workspace); err != nil {
				t.Fatal(err)
			}
			pr := basePR
			if tt.mutatePR != nil {
				tt.mutatePR(&pr)
			}
			record := tt.artifact
			if record.Status == "" {
				record = testRunRecord("success", pr.URL)
				record.IssueIdentifier = "CAG-70"
				record.ReviewStatus = "passed"
			}
			data, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(workspace, ".pi-symphony-run.json"), data, 0o600); err != nil {
				t.Fatal(err)
			}

			decision := evaluateMergeGate(runnerConfig{WorkspaceRoot: root, BaseBranch: "main", GitHubAppSlug: "compound-symphony-bot"}, testIssue("CAG-70", "Human Review"), pr)
			if decision.Eligible != tt.wantOK {
				t.Fatalf("Eligible=%t, want %t; blockers=%v", decision.Eligible, tt.wantOK, decision.Blockers)
			}
			if tt.wantCode != "" && !hasString(decision.Codes(), tt.wantCode) {
				t.Fatalf("Codes()=%v, want %q", decision.Codes(), tt.wantCode)
			}
			if tt.wantReason != "" && !strings.Contains(decision.Reason(), tt.wantReason) {
				t.Fatalf("Reason()=%q, want to contain %q", decision.Reason(), tt.wantReason)
			}
		})
	}
}

func TestMergeApprovedPRsMovesConflictingPRBackToReady(t *testing.T) {
	root := t.TempDir()
	merged := map[int]bool{}
	withFakeGitHubAPI(t, fakeGitHubAPI{prs: []pullRequestSummary{{Number: 414, URL: "https://github.com/pennywise-investments/compound-web/pull/414", HeadRefName: "symphony/CAG-23-workspace-cleanup", Mergeable: "CONFLICTING", MergeStateStatus: "DIRTY", ReviewDecision: "APPROVED", StatusCheckRollup: []statusCheck{{Typename: "CheckRun", Status: "COMPLETED", Conclusion: "SUCCESS"}}}}, mergedPRs: merged})

	var updatedStates []string
	var comments []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch {
		case strings.Contains(request.Query, "issue(id"):
			candidate := testIssue("CAG-23", "Human Review")
			candidate.Team.ID = "team-id"
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"issue": candidate}})
		case strings.Contains(request.Query, "workflowStates"):
			states := []workflowState{{ID: "ready-id", Name: "Ready for Agent"}, {ID: "done-id", Name: "Done"}}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"workflowStates": map[string]any{"nodes": states}}})
		case strings.Contains(request.Query, "issueUpdate"):
			updatedStates = append(updatedStates, request.Variables["stateId"].(string))
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"issueUpdate": map[string]any{"success": true}}})
		case strings.Contains(request.Query, "commentCreate"):
			comments = append(comments, request.Variables["body"].(string))
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"commentCreate": map[string]any{"success": true}}})
		default:
			t.Fatalf("unexpected Linear query: %s", request.Query)
		}
	}))
	defer server.Close()

	config := testRunnerConfig(root)
	config.HandoffState = "Human Review"
	config.DoneState = "Done"
	client := linearClient{apiKey: "test-key", endpoint: server.URL}
	if err := mergeApprovedPRs(client, config); err != nil {
		t.Fatal(err)
	}

	if len(updatedStates) != 1 || updatedStates[0] != "ready-id" {
		t.Fatalf("updated states = %#v, want ready-id", updatedStates)
	}
	if len(comments) != 1 || !strings.Contains(comments[0], "merge blocked by conflicts") {
		t.Fatalf("unexpected comments: %#v", comments)
	}
	feedback, err := os.ReadFile(filepath.Join(root, "CAG-23", ".pi-symphony-feedback.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(feedback), "mergeable=CONFLICTING") || !strings.Contains(string(feedback), "Resolve merge conflicts") {
		t.Fatalf("feedback missing conflict context: %s", string(feedback))
	}
	if merged[414] {
		t.Fatal("conflicting PR should not be merged")
	}
}

func TestMergeApprovedPRsSquashMergesAndDeletesBranchViaGitHubAPI(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-41")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	writeMergeableRunArtifact(t, workspace, "https://github.com/pennywise-investments/compound-web/pull/441")
	merged := map[int]bool{}
	deleted := map[string]bool{}
	withFakeGitHubAPI(t, fakeGitHubAPI{prs: []pullRequestSummary{{Number: 441, URL: "https://github.com/pennywise-investments/compound-web/pull/441", BaseRefName: "develop", HeadRefName: "symphony/CAG-41-workspace", Author: prAuthor{Login: "app/compound-symphony-bot"}, Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN", ReviewDecision: "APPROVED", StatusCheckRollup: []statusCheck{{Typename: "CheckRun", Status: "COMPLETED", Conclusion: "SUCCESS"}}}}, mergedPRs: merged, deletedBranches: deleted})

	var updatedStates []string
	var comments []string
	client := mergeTestLinearClient(t, "CAG-41", &updatedStates, &comments)
	config := testRunnerConfig(root)
	config.HandoffState = "Human Review"
	config.DoneState = "Done"
	config.BaseBranch = "develop"

	if err := mergeApprovedPRs(client, config); err != nil {
		t.Fatal(err)
	}
	if !merged[441] || !deleted["symphony/CAG-41-workspace"] {
		t.Fatalf("expected typed API merge and branch delete, merged=%v deleted=%v", merged, deleted)
	}
	if len(updatedStates) != 1 || updatedStates[0] != "done-id" {
		t.Fatalf("updated states = %#v, want done-id", updatedStates)
	}
	if len(comments) != 1 || !strings.Contains(comments[0], "Merged approved PR") {
		t.Fatalf("unexpected comments: %#v", comments)
	}
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatalf("expected Done workspace removed, stat err=%v", err)
	}
}

func TestMergeApprovedPRsFailsClosedWhenSQLiteUnavailable(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "state"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	githubCalled := false
	previousGitHub := newGitHubAPI
	newGitHubAPI = func() (githubAPI, error) {
		githubCalled = true
		return fakeGitHubAPI{}, nil
	}
	t.Cleanup(func() { newGitHubAPI = previousGitHub })

	var updatedStates []string
	var comments []string
	config := testRunnerConfig(root)
	config.HandoffState = "Human Review"
	config.DoneState = "Done"

	err := mergeApprovedPRs(mergeTestLinearClient(t, "CAG-41", &updatedStates, &comments), config)
	if err == nil || !strings.Contains(err.Error(), "SQLite state store unavailable for merge-approved") {
		t.Fatalf("expected SQLite fail-closed error, got %v", err)
	}
	if githubCalled {
		t.Fatal("merge lane called GitHub after SQLite state store failure")
	}
	if len(updatedStates) != 0 || len(comments) != 0 {
		t.Fatalf("merge lane mutated Linear after SQLite failure: states=%#v comments=%#v", updatedStates, comments)
	}
}

func TestMergeApprovedPRsEmitsCompletedEvent(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-104")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	writeMergeableRunArtifact(t, workspace, "https://github.com/pennywise-investments/compound-web/pull/104")
	withFakeGitHubAPI(t, fakeGitHubAPI{prs: []pullRequestSummary{{Number: 104, URL: "https://github.com/pennywise-investments/compound-web/pull/104", BaseRefName: "develop", HeadRefName: "symphony/CAG-104-workspace", Author: prAuthor{Login: "app/compound-symphony-bot"}, Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN", ReviewDecision: "APPROVED", StatusCheckRollup: []statusCheck{{Typename: "CheckRun", Status: "COMPLETED", Conclusion: "SUCCESS"}}}}, mergedPRs: map[int]bool{}, deletedBranches: map[string]bool{}})

	var updatedStates []string
	var comments []string
	config := testRunnerConfig(root)
	config.HandoffState = "Human Review"
	config.DoneState = "Done"
	config.BaseBranch = "develop"
	if err := mergeApprovedPRs(mergeTestLinearClient(t, "CAG-104", &updatedStates, &comments), config); err != nil {
		t.Fatal(err)
	}

	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	events, err := store.Events(context.Background(), state.EventFilter{IssueKey: "CAG-104", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	types := eventTypeCounts(events)
	for _, eventType := range []string{
		state.EventMergeAttempted,
		state.EventMergeSucceeded,
		state.EventBranchDeletionAttempted,
		state.EventBranchDeletionFinished,
		state.EventLinearDoneTransitionAttempted,
		state.EventLinearDoneTransitionFinished,
		state.EventMergeCompleted,
	} {
		if types[eventType] != 1 {
			t.Fatalf("%s events = %d, want 1; all=%#v", eventType, types[eventType], types)
		}
	}
}

func TestMergeApprovedPRsEmitsBlockedEvent(t *testing.T) {
	root := t.TempDir()
	withFakeGitHubAPI(t, fakeGitHubAPI{prs: []pullRequestSummary{{Number: 105, URL: "https://github.com/pennywise-investments/compound-web/pull/105", BaseRefName: "develop", HeadRefName: "symphony/CAG-105-workspace", Author: prAuthor{Login: "app/compound-symphony-bot"}, Mergeable: "CONFLICTING", MergeStateStatus: "DIRTY", ReviewDecision: "APPROVED", StatusCheckRollup: []statusCheck{{Typename: "CheckRun", Status: "COMPLETED", Conclusion: "SUCCESS"}}}}})
	var updatedStates []string
	var comments []string
	config := testRunnerConfig(root)
	config.HandoffState = "Human Review"
	config.ReadyState = "Ready for Agent"
	if err := mergeApprovedPRs(mergeTestLinearClient(t, "CAG-105", &updatedStates, &comments), config); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	events, err := store.Events(context.Background(), state.EventFilter{IssueKey: "CAG-105", Type: state.EventMergeBlocked, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("merge_blocked events = %d, want 1", len(events))
	}
}

func TestMergeApprovedPRsStopsIfBranchDeletionFails(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-41")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	writeMergeableRunArtifact(t, workspace, "https://github.com/pennywise-investments/compound-web/pull/441")
	merged := map[int]bool{}
	withFakeGitHubAPI(t, fakeGitHubAPI{prs: []pullRequestSummary{{Number: 441, URL: "https://github.com/pennywise-investments/compound-web/pull/441", BaseRefName: "develop", HeadRefName: "symphony/CAG-41-workspace", Author: prAuthor{Login: "app/compound-symphony-bot"}, Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN", ReviewDecision: "APPROVED", StatusCheckRollup: []statusCheck{{Typename: "CheckRun", Status: "COMPLETED", Conclusion: "SUCCESS"}}}}, mergedPRs: merged, deleteErr: errors.New("delete failed")})

	var updatedStates []string
	var comments []string
	client := mergeTestLinearClient(t, "CAG-41", &updatedStates, &comments)
	config := testRunnerConfig(root)
	config.HandoffState = "Human Review"
	config.DoneState = "Done"
	config.BaseBranch = "develop"

	err := mergeApprovedPRs(client, config)
	if err == nil || !strings.Contains(err.Error(), "branch deletion failed") {
		t.Fatalf("expected branch deletion failure, got %v", err)
	}
	if !merged[441] {
		t.Fatal("expected merge attempted before branch deletion")
	}
	if len(updatedStates) != 0 || len(comments) != 0 {
		t.Fatalf("branch deletion failure should block Done transition/comment, states=%#v comments=%#v", updatedStates, comments)
	}
	if _, err := os.Stat(workspace); err != nil {
		t.Fatalf("workspace should remain for repair after branch deletion failure: %v", err)
	}
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	events, err := store.Events(context.Background(), state.EventFilter{IssueKey: "CAG-41", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	types := eventTypeCounts(events)
	for _, eventType := range []string{state.EventMergeAttempted, state.EventMergeSucceeded, state.EventBranchDeletionAttempted, state.EventBranchDeletionFinished, state.EventMergeFailed, state.EventErrorRecorded} {
		if types[eventType] != 1 {
			t.Fatalf("%s events = %d, want 1; all=%#v", eventType, types[eventType], types)
		}
	}
}

func eventTypeCounts(events []state.Event) map[string]int {
	types := map[string]int{}
	for _, event := range events {
		types[event.Type]++
	}
	return types
}

func mergeTestLinearClient(t *testing.T, identifier string, updatedStates *[]string, comments *[]string) linearClient {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch {
		case strings.Contains(request.Query, "issue(id"):
			candidate := testIssue(identifier, "Human Review")
			candidate.Team.ID = "team-id"
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"issue": candidate}})
		case strings.Contains(request.Query, "workflowStates"):
			states := []workflowState{{ID: "ready-id", Name: "Ready for Agent"}, {ID: "done-id", Name: "Done"}}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"workflowStates": map[string]any{"nodes": states}}})
		case strings.Contains(request.Query, "issueUpdate"):
			*updatedStates = append(*updatedStates, request.Variables["stateId"].(string))
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"issueUpdate": map[string]any{"success": true}}})
		case strings.Contains(request.Query, "commentCreate"):
			*comments = append(*comments, request.Variables["body"].(string))
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"commentCreate": map[string]any{"success": true}}})
		default:
			t.Fatalf("unexpected Linear query: %s", request.Query)
		}
	}))
	t.Cleanup(server.Close)
	return linearClient{apiKey: "test-key", endpoint: server.URL}
}

func writeMergeableRunArtifact(t *testing.T, workspace, prURL string) {
	t.Helper()
	writeCleanRunArtifact(t, workspace, "success")
	record := runRecord{IssueIdentifier: filepath.Base(workspace), IssueID: "issue-id", IssueTitle: "Title", Workspace: workspace, PRURL: prURL, Status: "success", ReviewStatus: "passed"}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".pi-symphony-run.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRenderPRConflictFeedbackIncludesRepairInstructions(t *testing.T) {
	pr := pullRequestSummary{Number: 414, URL: "https://github.com/example/repo/pull/414", HeadRefName: "symphony/CAG-23", Mergeable: "CONFLICTING", MergeStateStatus: "DIRTY"}
	feedback := renderPRConflictFeedback(pr, mergeConflictReason(pr))
	for _, want := range []string{"mergeable=CONFLICTING", "Update this PR branch", "Rerun the validation", "symphony/CAG-23"} {
		if !strings.Contains(feedback, want) {
			t.Fatalf("feedback missing %q: %s", want, feedback)
		}
	}
}

func TestFeedbackAlreadyAddressedWhenRunPassedReview(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-30")
	feedback := "# PR #429 review feedback\n\n## Review: CHANGES_REQUESTED by reviewer\n\nTest should be unit test.\n"
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".pi-symphony-feedback.md"), []byte(feedback), 0o600); err != nil {
		t.Fatal(err)
	}
	record := runRecord{Status: "success", ReviewStatus: "passed", PRURL: "https://github.com/pennywise-investments/compound-web/pull/429", FeedbackHash: feedbackHash(feedback)}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".pi-symphony-run.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}

	if !feedbackAlreadyAddressed(workspace, record.PRURL, feedback) {
		t.Fatal("expected addressed feedback to avoid another retry")
	}
	if feedbackAlreadyAddressed(workspace, record.PRURL, feedback+"\nNew comment") {
		t.Fatal("new feedback should remain actionable")
	}
}

func TestFeedbackAlreadyAddressedFallsBackToFeedbackFileForOldArtifacts(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-30")
	feedback := "# PR #429 review feedback\n\n## Review: CHANGES_REQUESTED by reviewer\n\nTest should be unit test.\n"
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".pi-symphony-feedback.md"), []byte(feedback), 0o600); err != nil {
		t.Fatal(err)
	}
	record := runRecord{Status: "success", ReviewStatus: "passed", PRURL: "https://github.com/pennywise-investments/compound-web/pull/429"}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".pi-symphony-run.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}

	if !feedbackAlreadyAddressed(workspace, record.PRURL, feedback) {
		t.Fatal("expected old artifact plus matching feedback file to avoid another retry")
	}
}
