package main

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestClaimNextImplementationAttemptSkipsReviewReadyResume(t *testing.T) {
	root := t.TempDir()
	store := openCandidateTestStateStore(t)
	reviewCandidate := testIssue("CAG-166", "Ready for Agent")
	freshCandidate := testIssue("CAG-167", "Ready for Agent")
	pr := pullRequestSummary{
		Number:            166,
		URL:               "https://github.com/weskor/pi-symphony/pull/166",
		BaseRefName:       "develop",
		HeadRefName:       expectedWorkspaceBranch(reviewCandidate.Identifier),
		Author:            prAuthor{Login: githubAppPRAuthorLogin},
		ReviewDecision:    "COMMENTED",
		Mergeable:         "MERGEABLE",
		MergeStateStatus:  "CLEAN",
		StatusCheckRollup: []statusCheck{{Typename: "CheckRun", Status: "COMPLETED", Conclusion: "SUCCESS"}},
	}
	writeRunRecordFixture(t, root, reviewCandidate.Identifier, fmt.Sprintf(`{"status":%q,"pr_url":%q}`, runAttemptStatusReviewNotReady, pr.URL))
	writeRunProgress(root, runProgressSnapshot{IssueIdentifier: reviewCandidate.Identifier, Phase: "review_not_ready", PRURL: pr.URL, StartedAt: time.Now().Add(-time.Minute)})
	original := openPRsByIssueForSelection
	openPRsByIssueForSelection = func(runnerConfig) (map[string]*pullRequestSummary, error) {
		return map[string]*pullRequestSummary{reviewCandidate.Identifier: &pr}, nil
	}
	t.Cleanup(func() { openPRsByIssueForSelection = original })

	client := linearClientWithCandidates(t, []issue{reviewCandidate, freshCandidate})
	config := testRunnerConfig(root)
	config.BaseBranch = "develop"
	config.PiCommand = "true"
	config.ReviewCommand = "true"
	wf := workflow{YAML: "agent:\n  max_turns: 1\n"}

	claim, didWork, err := claimNextImplementationAttempt(client, wf, config, store)
	if err != nil {
		t.Fatal(err)
	}
	if !didWork || claim == nil {
		t.Fatalf("claim = %#v didWork=%t; want fresh implementation claim", claim, didWork)
	}
	defer claim.ReleaseLock()
	if claim.Candidate.Identifier != freshCandidate.Identifier {
		t.Fatalf("claimed %s; want fresh %s", claim.Candidate.Identifier, freshCandidate.Identifier)
	}
	if claim.SelectedPR != nil {
		t.Fatalf("selected PR = %#v; want no PR for fresh implementation", claim.SelectedPR)
	}
	if !hasRunLock(filepath.Join(root, freshCandidate.Identifier)) {
		t.Fatalf("expected implementation claim to hold a run lock")
	}
	if hasRunLock(filepath.Join(root, reviewCandidate.Identifier)) {
		t.Fatalf("review-ready candidate should remain unclaimed by implementation worker")
	}
}
