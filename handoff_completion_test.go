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
		if snapshot.Phase != runProgressPhaseHandoffPending || snapshot.Status != runProgressPhaseHandoffPending || snapshot.NextAction != "complete_runner_handoff" || snapshot.PRURL != prURL || snapshot.ReviewStatus != "passed" || snapshot.HandoffPayloadPath == "" {
			t.Fatalf("progress before handoff side effects = %+v; want handoff_pending", snapshot)
		}
		payload, err := readHandoffPendingPayload(root, candidate.Identifier)
		if err != nil {
			t.Fatal(err)
		}
		if payload.IssueIdentifier != candidate.Identifier || payload.IssueID != candidate.ID || payload.PRURL != prURL || payload.Review.Status != "passed" || len(payload.Validation) != 1 || payload.GitHubAuth != "github_app_installation" {
			t.Fatalf("handoff payload = %+v; want complete handoff input", payload)
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

func TestCompleteAttemptHandoffExecutesPersistedPayloadBoundary(t *testing.T) {
	t.Cleanup(resetHandoffWorkerHooks)
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-176")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	candidate := &issue{ID: "issue-176", Identifier: "CAG-176", Title: "Payload boundary", URL: "https://linear.app/acme/issue/CAG-176"}
	prURL := "https://github.com/acme/repo/pull/176"
	readHandoffPendingPayloadForCompletion = func(workspaceRoot, issueIdentifier string) (handoffPendingPayload, error) {
		payload, err := readHandoffPendingPayload(workspaceRoot, issueIdentifier)
		if err != nil {
			return handoffPendingPayload{}, err
		}
		payload.Validation = append(payload.Validation, "payload-boundary-read")
		return payload, nil
	}
	var postedSummary handoffSummary
	postOrUpdatePRHandoffCommentForWorker = func(summary handoffSummary) error {
		postedSummary = summary
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
		validation:      []string{"input-validation"},
		githubAuth:      "github_app_installation",
	})
	if err != nil || !didWork {
		t.Fatalf("completeAttemptHandoff() didWork=%t err=%v; want work without error", didWork, err)
	}
	if len(postedSummary.Validation) != 2 || postedSummary.Validation[1] != "payload-boundary-read" {
		t.Fatalf("posted validation = %#v; want side effects from persisted payload read", postedSummary.Validation)
	}
}

func TestHandoffPendingPayloadRoundTripsCompletionInput(t *testing.T) {
	root := t.TempDir()
	candidate := &issue{ID: "issue-171", Identifier: "CAG-171", Title: "Payload round trip", URL: "https://linear.app/acme/issue/CAG-171"}
	candidate.Team.ID = "team-171"
	input := handoffCompletion{
		config:          runnerConfig{WorkspaceRoot: root, PiCommand: "pi run"},
		candidate:       candidate,
		workspace:       filepath.Join(root, candidate.Identifier),
		branch:          expectedWorkspaceBranch(candidate.Identifier),
		progressStarted: time.Now().Add(-time.Minute),
		startedAt:       time.Now().Add(-2 * time.Minute),
		runtimeUsage:    &usage{TotalTokens: 42},
		review:          &reviewResult{Status: "passed", Findings: "ship it"},
		prURL:           "https://github.com/acme/repo/pull/171",
		validation:      []string{"go test ./...", "git diff --check"},
		githubAuth:      "github_app_installation",
	}
	if err := writeHandoffPendingPayload(root, handoffPendingPayloadFromCompletion(input)); err != nil {
		t.Fatal(err)
	}
	payload, err := readHandoffPendingPayload(root, candidate.Identifier)
	if err != nil {
		t.Fatal(err)
	}
	completion := payload.Completion(linearClient{}, input.config, nil, []workflowState{{ID: "handoff-id", Name: "Human Review"}})
	if completion.candidate.Identifier != candidate.Identifier || completion.candidate.Team.ID != "team-171" || completion.workspace != input.workspace || completion.branch != input.branch || completion.prURL != input.prURL || completion.review.Findings != "ship it" || completion.runtimeUsage.TotalTokens != 42 || len(completion.validation) != 2 || completion.githubAuth != input.githubAuth {
		t.Fatalf("completion = %+v; want payload round trip", completion)
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
	runReviewForWorker = func(string, string, string, *issue, string, map[string]string, time.Duration, *reviewEvidence) (*reviewResult, error) {
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
