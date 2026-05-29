package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestGitHubContractFixtureCoversOpenPRMetadataAndOwnership(t *testing.T) {
	fixture := `[
		{"number":501,"url":"https://github.com/acme/widget/pull/501","baseRefName":"develop","headRefName":"am/CAG-39-workspace","author":{"login":"app/agent-machine-bot"},"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","reviewDecision":"APPROVED","statusCheckRollup":[{"__typename":"CheckRun","name":"build","status":"COMPLETED","conclusion":"SUCCESS"},{"__typename":"StatusContext","context":"Vercel","state":"SUCCESS"}]},
		{"number":502,"url":"https://github.com/acme/widget/pull/502","baseRefName":"main","headRefName":"am/CAG-39-workspace","author":{"login":"human"},"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","reviewDecision":"APPROVED","statusCheckRollup":[{"__typename":"CheckRun","name":"build","status":"COMPLETED","conclusion":"SUCCESS"}]},
		{"number":503,"url":"https://github.com/acme/widget/pull/503","baseRefName":"develop","headRefName":"am/CAG-39-workspace","author":{"login":"agent-machine-bot[bot]"},"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","reviewDecision":"APPROVED","statusCheckRollup":[{"__typename":"CheckRun","name":"build","status":"COMPLETED","conclusion":"SUCCESS"}]},
		{"number":504,"url":"https://github.com/acme/widget/pull/504","baseRefName":"develop","headRefName":"am/CAG-39-workspace","author":{"login":"unrelated-bot[bot]"},"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","reviewDecision":"APPROVED","statusCheckRollup":[{"__typename":"CheckRun","name":"build","status":"COMPLETED","conclusion":"SUCCESS"}]}
	]`
	var prs []pullRequestSummary
	if err := json.Unmarshal([]byte(fixture), &prs); err != nil {
		t.Fatal(err)
	}
	config := testRunnerConfig(t.TempDir())
	config.BaseBranch = "develop"

	if reason := prInvariantBlockReason(config, testIssue("CAG-39", "Human Review"), prs[0]); reason != "" {
		t.Fatalf("expected app-authored PR with configured base/head to pass invariants, got %q", reason)
	}
	if reason := prInvariantBlockReason(config, testIssue("CAG-39", "Human Review"), prs[1]); !strings.Contains(reason, "base branch") || !strings.Contains(reason, "PR author") {
		t.Fatalf("expected human-authored wrong-base PR rejection, got %q", reason)
	}
	if reason := prInvariantBlockReason(config, testIssue("CAG-39", "Human Review"), prs[2]); reason != "" {
		t.Fatalf("expected REST GitHub App bot PR author login to pass invariants, got %q", reason)
	}
	if reason := prInvariantBlockReason(config, testIssue("CAG-39", "Human Review"), prs[3]); !strings.Contains(reason, "PR author") {
		t.Fatalf("expected unrelated bot PR author rejection, got %q", reason)
	}
}

func TestGitHubContractFixtureCoversChecksConflictsAndChangesRequested(t *testing.T) {
	green := pullRequestSummary{HeadRefName: expectedWorkspaceBranch("CAG-39"), Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN", ReviewDecision: "APPROVED", StatusCheckRollup: []statusCheck{{Typename: "CheckRun", Name: "build", Status: "COMPLETED", Conclusion: "SUCCESS"}, {Typename: "StatusContext", Context: "Vercel", State: "SUCCESS"}}}
	if reason := mergeGateBlockReason(green); reason != "" {
		t.Fatalf("expected green checks to pass, got %q", reason)
	}
	for name, pr := range map[string]pullRequestSummary{
		"pending":  {Mergeable: "MERGEABLE", StatusCheckRollup: []statusCheck{{Typename: "CheckRun", Name: "build", Status: "IN_PROGRESS"}}},
		"failed":   {Mergeable: "MERGEABLE", StatusCheckRollup: []statusCheck{{Typename: "StatusContext", Context: "Vercel", State: "FAILURE"}}},
		"conflict": {HeadRefName: expectedWorkspaceBranch("CAG-39"), Mergeable: "CONFLICTING", MergeStateStatus: "DIRTY", StatusCheckRollup: green.StatusCheckRollup},
	} {
		if reason := mergeGateBlockReason(pr); reason == "" {
			t.Fatalf("expected %s PR to block merge", name)
		}
	}

	feedback := prFeedback{}
	feedback.Reviews = append(feedback.Reviews, struct {
		Author struct {
			Login string `json:"login"`
		} `json:"author"`
		State       string `json:"state"`
		Body        string `json:"body"`
		SubmittedAt string `json:"submittedAt"`
	}{State: "CHANGES_REQUESTED", Body: "Please add coverage."})
	feedback.Reviews[0].Author.Login = "reviewer"
	feedback.Comments = append(feedback.Comments, struct {
		Author struct {
			Login string `json:"login"`
		} `json:"author"`
		Body      string `json:"body"`
		CreatedAt string `json:"createdAt"`
	}{Body: "Issue comment with stable ID."})
	feedback.Comments[0].Author.Login = "operator"
	feedback.ReviewComments = append(feedback.ReviewComments, struct {
		User struct {
			Login string `json:"login"`
		} `json:"user"`
		Path      string `json:"path"`
		Line      int    `json:"line"`
		Body      string `json:"body"`
		CreatedAt string `json:"created_at"`
	}{Path: "tools/agent-machine/merge.go", Line: 42, Body: "Inline review comment."})
	feedback.ReviewComments[0].User.Login = "reviewer"
	rendered := renderPRFeedback(501, feedback)
	for _, want := range []string{"CHANGES_REQUESTED", "Please add coverage", "Issue comment with stable ID", "Inline review comment", "tools/agent-machine/merge.go:42"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("feedback fixture missing %q:\n%s", want, rendered)
		}
	}
}

func TestGitHubContractHandoffEvidenceUpdatesPRBodyOnly(t *testing.T) {
	updatedBodies := map[int]string{}
	withFakeGitHubAPI(t, fakeGitHubAPI{
		handoffDetailsByURL: map[string]prHandoffDetails{
			"https://github.com/acme/widget/pull/501": {Number: 501, URL: "https://github.com/acme/widget/pull/501", BaseRefName: "main", ChangedFiles: 4, Additions: 30, Deletions: 8},
		},
		updatedPRBodies: updatedBodies,
	})

	summary := handoffSummary{
		IssueIdentifier: "CAG-39",
		IssueTitle:      "GitHub behavior contract",
		IssueURL:        "https://linear.app/wessismore/issue/CAG-39/agent-machine-github-api-phase-1-behavior-contract-and-fixtures",
		PRURL:           "https://github.com/acme/widget/pull/501",
		Validation:      []string{"bun run am:pi:test"},
	}
	if err := updatePRHandoffBody(summary); err != nil {
		t.Fatal(err)
	}

	patchBody := updatedBodies[501]
	for _, want := range []string{"## Agent Machine handoff", "CAG-39", "GitHub behavior contract", "### Behavior Contract Evidence", "Files changed: 4"} {
		if !strings.Contains(patchBody, want) {
			t.Fatalf("updated PR body missing %q:\n%s", want, patchBody)
		}
	}
	if strings.Contains(patchBody, "<!-- am-summary -->") {
		t.Fatalf("updated PR body should not include retired comment marker:\n%s", patchBody)
	}
}

func TestGitHubContractInventoryDocumentsGhParityChecklist(t *testing.T) {
	data, err := os.ReadFile("github_contract.md")
	if err != nil {
		t.Fatal(err)
	}
	contract := string(data)
	for _, want := range []string{
		"former `gh pr list --state open --json number,url,baseRefName,headRefName,author,mergeable,mergeStateStatus,reviewDecision,statusCheckRollup` shape through the typed GitHub API client",
		"former `gh pr view <number> --json reviews,comments` plus `gh api repos/:owner/:repo/pulls/<number>/comments` shapes through the typed GitHub API client",
		"PR handoff evidence is written to the runner-owned PR body via the typed GitHub API client",
		"Approved PR merge uses the typed GitHub API client to squash merge",
		"Post-merge branch deletion uses the typed GitHub API client only after squash merge confirms `merged=true`",
		"app/agent-machine-bot",
		"agent-machine-bot[bot]",
		"CHANGES_REQUESTED",
		"GITHUB_REPOSITORY",
	} {
		if !strings.Contains(contract, want) {
			t.Fatalf("contract inventory missing %q", want)
		}
	}
}
