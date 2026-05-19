package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	sh "github.com/weskor/pi-symphony/internal/shell"
)

func TestReconcileIssueAllowsFeedbackRetryToSupersedeTerminalArtifact(t *testing.T) {
	root := t.TempDir()
	writeRunRecordFixture(t, root, "CAG-34", `{"status":"success","pr_url":"https://github.com/pennywise-investments/compound-web/pull/434"}`)
	workspace := filepath.Join(root, "CAG-34")
	if err := os.WriteFile(filepath.Join(workspace, ".pi-symphony-feedback.md"), []byte("unresolved feedback"), 0o600); err != nil {
		t.Fatal(err)
	}

	decision := reconcileIssue(testRunnerConfig(root), testIssue("CAG-34", "Ready for Agent"), nil)

	if !decision.CanRun || !decision.ShouldRetry || decision.Lifecycle != lifecycleFeedbackRetry {
		t.Fatalf("expected feedback retry runnable decision, got %#v", decision)
	}
}

func TestReconcileIssueBlocksTerminalArtifactWithoutNewFeedback(t *testing.T) {
	root := t.TempDir()
	writeRunRecordFixture(t, root, "CAG-34", `{"status":"success","pr_url":"https://github.com/pennywise-investments/compound-web/pull/434"}`)

	decision := reconcileIssue(testRunnerConfig(root), testIssue("CAG-34", "Ready for Agent"), nil)

	if decision.CanRun || decision.Lifecycle != lifecycleHandoffReady || !strings.Contains(strings.Join(decision.Blockers, "; "), "terminal success artifact") {
		t.Fatalf("expected terminal artifact block, got %#v", decision)
	}
}

func TestReconcileIssueAllowsChangesRequestedToSupersedeReviewFailedArtifact(t *testing.T) {
	root := t.TempDir()
	writeRunRecordFixture(t, root, "CAG-35", `{"status":"review_failed","review_status":"failed","pr_url":"https://github.com/pennywise-investments/compound-web/pull/440"}`)
	config := testRunnerConfig(root)
	config.BaseBranch = "develop"
	pr := pullRequestSummary{Number: 440, URL: "https://github.com/pennywise-investments/compound-web/pull/440", BaseRefName: "develop", HeadRefName: expectedWorkspaceBranch("CAG-35"), Author: prAuthor{Login: githubAppPRAuthorLogin}, ReviewDecision: "CHANGES_REQUESTED"}

	decision := reconcileIssue(config, testIssue("CAG-35", "Ready for Agent"), &pr)

	if !decision.CanRun || !decision.ShouldRetry || decision.Lifecycle != lifecycleFeedbackRetry || decision.NextAction != "capture_feedback_and_retry" {
		t.Fatalf("expected CHANGES_REQUESTED to be runnable feedback retry, got %#v", decision)
	}
}

func TestReconcileIssueQuarantinesWrongBaseAuthorAndHead(t *testing.T) {
	root := t.TempDir()
	config := testRunnerConfig(root)
	config.HandoffState = "Human Review"
	config.BaseBranch = "develop"
	writeRunRecordFixture(t, root, "CAG-34", `{"status":"success","review_status":"passed","pr_url":"https://github.com/pennywise-investments/compound-web/pull/434"}`)
	pr := pullRequestSummary{
		Number:            434,
		URL:               "https://github.com/pennywise-investments/compound-web/pull/434",
		BaseRefName:       "main",
		HeadRefName:       "feature/CAG-34",
		Author:            prAuthor{Login: "human"},
		ReviewDecision:    "APPROVED",
		Mergeable:         "MERGEABLE",
		StatusCheckRollup: []statusCheck{{Typename: "CheckRun", Status: "COMPLETED", Conclusion: "SUCCESS", Name: "build"}},
	}

	decision := reconcileIssue(config, testIssue("CAG-34", "Human Review"), &pr)

	joined := strings.Join(decision.Blockers, "; ")
	for _, expected := range []string{"base branch", "head branch", "PR author"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("expected %q in blockers %q", expected, joined)
		}
	}
	if !decision.ShouldQuarantine || decision.CanMerge || decision.Lifecycle != lifecycleQuarantined {
		t.Fatalf("expected quarantined non-mergeable decision, got %#v", decision)
	}
}

func TestReconcileIssueApprovesMergeOnlyWithCleanInvariants(t *testing.T) {
	root := t.TempDir()
	config := testRunnerConfig(root)
	config.HandoffState = "Human Review"
	config.BaseBranch = "develop"
	workspace := filepath.Join(root, "CAG-34")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := sh.Run("git init -q", workspace); err != nil {
		t.Fatal(err)
	}
	writeRunRecordFixture(t, root, "CAG-34", `{"status":"success","review_status":"passed","pr_url":"https://github.com/pennywise-investments/compound-web/pull/434"}`)
	pr := pullRequestSummary{
		Number:            434,
		URL:               "https://github.com/pennywise-investments/compound-web/pull/434",
		BaseRefName:       "develop",
		HeadRefName:       expectedWorkspaceBranch("CAG-34"),
		Author:            prAuthor{Login: githubAppPRAuthorLogin},
		ReviewDecision:    "APPROVED",
		Mergeable:         "MERGEABLE",
		StatusCheckRollup: []statusCheck{{Typename: "CheckRun", Status: "COMPLETED", Conclusion: "SUCCESS", Name: "build"}},
	}

	decision := reconcileIssue(config, testIssue("CAG-34", "Human Review"), &pr)

	if !decision.CanMerge || decision.Lifecycle != lifecycleMergeReady || len(decision.Blockers) != 0 {
		t.Fatalf("expected merge-ready decision, got %#v", decision)
	}
}

func TestReconcileIssueDistinguishesPRAuthorFromCommitAuthor(t *testing.T) {
	root := t.TempDir()
	config := testRunnerConfig(root)
	config.HandoffState = "Human Review"
	config.BaseBranch = "develop"
	workspace := filepath.Join(root, "CAG-34")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := sh.Run("git init -q", workspace); err != nil {
		t.Fatal(err)
	}
	writeRunRecordFixture(t, root, "CAG-34", `{"status":"success","review_status":"passed","pr_url":"https://github.com/pennywise-investments/compound-web/pull/434"}`)

	validPR := pullRequestSummary{
		Number:            434,
		URL:               "https://github.com/pennywise-investments/compound-web/pull/434",
		BaseRefName:       "develop",
		HeadRefName:       expectedWorkspaceBranch("CAG-34"),
		Author:            prAuthor{Login: githubAppPRAuthorLogin},
		Commits:           []prCommit{{Author: prCommitAuthor{Name: githubAppBotName, Email: githubAppBotEmail}}},
		ReviewDecision:    "APPROVED",
		Mergeable:         "MERGEABLE",
		StatusCheckRollup: []statusCheck{{Typename: "CheckRun", Status: "COMPLETED", Conclusion: "SUCCESS", Name: "build"}},
	}

	if decision := reconcileIssue(config, testIssue("CAG-34", "Human Review"), &validPR); !decision.CanMerge {
		t.Fatalf("expected GitHub App PR author login to be merge-ready, got %#v", decision)
	}

	restPR := validPR
	restPR.Author = prAuthor{Login: githubAppRESTPRAuthorLogin}
	if decision := reconcileIssue(config, testIssue("CAG-34", "Human Review"), &restPR); !decision.CanMerge {
		t.Fatalf("expected REST GitHub App bot PR author login to be merge-ready, got %#v", decision)
	}

	humanPR := validPR
	humanPR.Author = prAuthor{Login: "weskor"}
	decision := reconcileIssue(config, testIssue("CAG-34", "Human Review"), &humanPR)
	if !decision.ShouldQuarantine || !strings.Contains(strings.Join(decision.Blockers, "; "), "PR author") {
		t.Fatalf("expected human PR author to be quarantined, got %#v", decision)
	}

	unrelatedBotPR := validPR
	unrelatedBotPR.Author = prAuthor{Login: "unrelated-bot[bot]"}
	decision = reconcileIssue(config, testIssue("CAG-34", "Human Review"), &unrelatedBotPR)
	if !decision.ShouldQuarantine || !strings.Contains(strings.Join(decision.Blockers, "; "), "PR author") {
		t.Fatalf("expected unrelated bot PR author to be rejected, got %#v", decision)
	}

	wrongCommitAuthorPR := validPR
	wrongCommitAuthorPR.Commits = []prCommit{{Author: prCommitAuthor{Name: "Wes", Email: "wes@example.com"}}}
	decision = reconcileIssue(config, testIssue("CAG-34", "Human Review"), &wrongCommitAuthorPR)
	if !decision.ShouldQuarantine || !strings.Contains(strings.Join(decision.Blockers, "; "), "commit author") {
		t.Fatalf("expected wrong commit author to be rejected, got %#v", decision)
	}
}
