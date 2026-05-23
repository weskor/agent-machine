package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/weskor/pi-symphony/internal/state"
)

func TestCompleteAttemptHandoffWritesPendingProgressBeforeSideEffects(t *testing.T) {
	t.Cleanup(resetHandoffWorkerHooks)
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-169")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	candidate := &issue{ID: "issue-169", Identifier: "CAG-169", Title: "Handoff pending", URL: "https://linear.app/acme/issue/CAG-169"}
	prURL := "https://github.com/acme/repo/pull/169"
	sawPending := false
	postOrUpdatePRHandoffCommentForWorker = func(handoffSummary) error {
		snapshot, err := readRunProgress(root, candidate.Identifier)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Phase != runProgressPhaseHandoffPending || snapshot.Status != runProgressPhaseHandoffPending || snapshot.NextAction != "complete_runner_handoff" || snapshot.PRURL != prURL || snapshot.ReviewStatus != "passed" {
			t.Fatalf("progress before handoff side effects = %+v; want handoff_pending", snapshot)
		}
		sawPending = true
		return nil
	}
	updateIssueStateForLinearStatusWorker = func(linearClient, string, string) error { return nil }
	createCommentForLinearStatusWorker = func(linearClient, string, string) error { return nil }

	didWork, err := completeAttemptHandoff(context.Background(), handoffCompletion{
		client:          linearClient{},
		config:          runnerConfig{WorkspaceRoot: root, PiCommand: "pi run", HandoffState: "Human Review"},
		stateStore:      store,
		candidate:       candidate,
		states:          []workflowState{{ID: "handoff-id", Name: "Human Review"}},
		workspace:       workspace,
		branch:          expectedWorkspaceBranch(candidate.Identifier),
		progressStarted: time.Now().Add(-time.Minute),
		startedAt:       time.Now().Add(-2 * time.Minute),
		review:          &reviewResult{Status: "passed"},
		prURL:           prURL,
		validation:      []string{"go test ./..."},
		githubAuth:      "github_app_installation",
	})
	if err != nil || !didWork {
		t.Fatalf("completeAttemptHandoff() didWork=%t err=%v; want work without error", didWork, err)
	}
	if !sawPending {
		t.Fatal("handoff side effect hook did not observe pending progress")
	}
	final, err := readRunProgress(root, candidate.Identifier)
	if err != nil {
		t.Fatal(err)
	}
	if final.Phase != "completed" || final.Outcome != "handoff_ready" || final.PRURL != prURL {
		t.Fatalf("final progress = %+v; want completed handoff_ready", final)
	}
}

func TestResumeReviewReadyRunUsesPendingProgressBeforeHandoff(t *testing.T) {
	t.Cleanup(resetReviewWorkerHooks)
	t.Cleanup(resetHandoffWorkerHooks)
	oldScopeGuard := checkScopeGuardForReviewResume
	t.Cleanup(func() { checkScopeGuardForReviewResume = oldScopeGuard })
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-170")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	candidate := &issue{ID: "issue-170", Identifier: "CAG-170", Title: "Resume handoff pending", URL: "https://linear.app/acme/issue/CAG-170"}
	pr := &pullRequestSummary{Number: 170, URL: "https://github.com/acme/repo/pull/170"}
	checkScopeGuardForReviewResume = func(string, string, string) (scopeGuardResult, error) {
		return scopeGuardResult{Checked: true}, nil
	}
	collectReviewEvidenceForWorker = func(runnerConfig, *issue, string, string, scopeGuardResult, []string) (reviewEvidence, error) {
		return reviewEvidence{ChecksStatus: "success", ChecksSummary: "go-ci=success"}, nil
	}
	runReviewForWorker = func(string, string, *issue, string, map[string]string, time.Duration, *reviewEvidence) (*reviewResult, error) {
		return &reviewResult{Status: "passed"}, nil
	}
	postOrUpdatePRHandoffCommentForWorker = func(handoffSummary) error {
		snapshot, err := readRunProgress(root, candidate.Identifier)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Phase != runProgressPhaseHandoffPending || snapshot.PRURL != pr.URL || snapshot.ReviewStatus != "passed" {
			t.Fatalf("resume progress before handoff side effects = %+v; want handoff_pending", snapshot)
		}
		return nil
	}
	updateIssueStateForLinearStatusWorker = func(linearClient, string, string) error { return nil }
	createCommentForLinearStatusWorker = func(linearClient, string, string) error { return nil }

	didWork, err := resumeReviewReadyRun(linearClient{}, store, runnerConfig{WorkspaceRoot: root, PiCommand: "pi run", ReviewCommand: "pi review", HandoffState: "Human Review"}, candidate, []workflowState{{ID: "handoff-id", Name: "Human Review"}}, workspace, expectedWorkspaceBranch(candidate.Identifier), map[string]string{"GITHUB_TOKEN": "token"}, "github_app_installation", time.Now().Add(-time.Minute), time.Now().Add(-2*time.Minute), pr)
	if err != nil || !didWork {
		t.Fatalf("resumeReviewReadyRun() didWork=%t err=%v; want work without error", didWork, err)
	}
}
