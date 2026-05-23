package main

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestClaimNextReviewReadyAttemptClaimsOnlyReviewNotReadySuccess(t *testing.T) {
	root := t.TempDir()
	store := openCandidateTestStateStore(t)
	reviewCandidate := testIssue("CAG-166", "In Progress")
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

	client := linearClientWithCandidates(t, []issue{freshCandidate, reviewCandidate})
	config := testRunnerConfig(root)
	config.BaseBranch = "develop"
	config.PiCommand = "true"
	config.ReviewCommand = "true"
	wf := workflow{YAML: "agent:\n  max_turns: 1\n"}

	claim, didWork, err := claimNextReviewReadyAttempt(client, wf, config, store)
	if err != nil {
		t.Fatal(err)
	}
	if !didWork || claim == nil {
		t.Fatalf("claim = %#v didWork=%t; want review-ready claim", claim, didWork)
	}
	defer claim.ReleaseLock()
	if claim.Candidate.Identifier != reviewCandidate.Identifier {
		t.Fatalf("claimed %s; want %s", claim.Candidate.Identifier, reviewCandidate.Identifier)
	}
	if claim.SelectedPR == nil || claim.SelectedPR.URL != pr.URL {
		t.Fatalf("selected PR = %#v; want %s", claim.SelectedPR, pr.URL)
	}
	if !hasRunLock(filepath.Join(root, reviewCandidate.Identifier)) {
		t.Fatalf("expected review claim to hold a run lock")
	}

	tasks, err := store.WorkerTasks(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Fatalf("claim helper should not create process worker tasks directly, got %+v", tasks)
	}
}
