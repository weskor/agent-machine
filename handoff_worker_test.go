package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weskor/pi-symphony/internal/state"
)

func TestHandoffWorkerPostsCommentsAndMovesToHandoffState(t *testing.T) {
	t.Cleanup(resetHandoffWorkerHooks)
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-157")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	candidate := &issue{ID: "issue-157", Identifier: "CAG-157", Title: "Extract handoff worker", URL: "https://linear.app/acme/issue/CAG-157"}
	prURL := "https://github.com/acme/repo/pull/157"
	var postedSummary handoffSummary
	var updatedIssueID, updatedStateID string
	var commentIssueID, commentBody string
	postOrUpdatePRHandoffCommentForWorker = func(summary handoffSummary) error {
		postedSummary = summary
		return nil
	}
	updateIssueStateForHandoffWorker = func(client linearClient, issueID, stateID string) error {
		updatedIssueID = issueID
		updatedStateID = stateID
		return nil
	}
	createCommentForHandoffWorker = func(client linearClient, issueID, body string) error {
		commentIssueID = issueID
		commentBody = body
		return nil
	}

	started := time.Now().Add(-2 * time.Minute)
	result, err := handoffWorker{
		client:     linearClient{},
		config:     runnerConfig{PiCommand: "pi run", HandoffState: "Human Review"},
		candidate:  candidate,
		states:     []workflowState{{ID: "human-review-id", Name: "Human Review"}},
		workspace:  workspace,
		startedAt:  started,
		piUsage:    &usage{TotalTokens: 42},
		review:     &reviewResult{Status: "passed"},
		prURL:      prURL,
		validation: []string{"go test ./..."},
		githubAuth: "github_app_installation",
	}.Execute(context.Background())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Terminal || result.Summary == nil {
		t.Fatalf("result = %+v; want non-terminal summary", result)
	}
	if postedSummary.PRURL != prURL || postedSummary.IssueIdentifier != candidate.Identifier {
		t.Fatalf("posted summary = %+v; want candidate and PR details", postedSummary)
	}
	if updatedIssueID != candidate.ID || updatedStateID != "human-review-id" {
		t.Fatalf("state update = issue %q state %q; want issue/state handoff", updatedIssueID, updatedStateID)
	}
	if commentIssueID != candidate.ID || !strings.Contains(commentBody, prURL) || !strings.Contains(commentBody, "passed") {
		t.Fatalf("Linear comment issue=%q body=%q; want handoff comment", commentIssueID, commentBody)
	}
	if result.Summary.Classification == nil || result.Summary.Classification.Outcome == "" {
		t.Fatalf("summary classification = %+v; want populated classification", result.Summary.Classification)
	}
}

func TestHandoffWorkerRecordsFailedRunWhenHandoffTransitionFails(t *testing.T) {
	t.Cleanup(resetHandoffWorkerHooks)
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-157")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	transitionErr := errors.New("linear transition failed")
	postOrUpdatePRHandoffCommentForWorker = func(handoffSummary) error { return nil }
	updateIssueStateForHandoffWorker = func(linearClient, string, string) error { return transitionErr }
	createCommentForHandoffWorker = func(linearClient, string, string) error {
		t.Fatal("Linear handoff comment should not be created after transition failure")
		return nil
	}

	candidate := &issue{ID: "issue-157", Identifier: "CAG-157", Title: "Extract handoff worker"}
	prURL := "https://github.com/acme/repo/pull/157"
	result, err := handoffWorker{
		client:     linearClient{},
		config:     runnerConfig{PiCommand: "pi run", HandoffState: "Human Review"},
		stateStore: store,
		candidate:  candidate,
		states:     []workflowState{{ID: "human-review-id", Name: "Human Review"}},
		workspace:  workspace,
		startedAt:  time.Now().Add(-time.Minute),
		review:     &reviewResult{Status: "passed"},
		prURL:      prURL,
	}.Execute(context.Background())
	if !errors.Is(err, transitionErr) {
		t.Fatalf("Execute() error = %v; want transition error", err)
	}
	if !result.Terminal {
		t.Fatalf("result = %+v; want terminal transition failure", result)
	}

	data, err := os.ReadFile(filepath.Join(workspace, ".pi-symphony-run.json"))
	if err != nil {
		t.Fatal(err)
	}
	var record runRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if record.Status != runAttemptStatusFailed || record.Error != transitionErr.Error() || record.PRURL != prURL {
		t.Fatalf("run record = %+v; want failed transition record", record)
	}
}
