package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weskor/agent-machine/internal/state"
)

func TestRepairReviewFailedPromptFeedbackUsesSQLiteReviewState(t *testing.T) {
	root := t.TempDir()
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.UpsertAttemptResult(context.Background(), state.AttemptResult{
		IssueKey:             "CAG-141",
		Attempt:              1,
		WorkspacePath:        filepath.Join(root, "CAG-141"),
		BranchName:           expectedWorkspaceBranch("CAG-141"),
		Status:               runAttemptStatusReviewFailed,
		PRURL:                "https://github.com/weskor/agent-machine/pull/93",
		ReviewStatus:         "failed",
		ReviewClassification: reviewClassificationBehaviorSpecBlocker,
		ReviewOutputRef:      "/tmp/review-output.txt",
		ReviewOutputHash:     "review-output-hash",
		UpdatedAt:            time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	feedback := repairReviewFailedPromptFeedback(store, "CAG-141", "")

	for _, expected := range []string{"Prior review state", "Review status: failed", reviewClassificationBehaviorSpecBlocker, "/tmp/review-output.txt", "review-output-hash", "prior semantic review failed"} {
		if !strings.Contains(feedback, expected) {
			t.Fatalf("repair feedback missing %q: %q", expected, feedback)
		}
	}
}

func TestRepairReviewFailedPromptFeedbackAppendsSQLiteReviewStateToExistingFeedback(t *testing.T) {
	root := t.TempDir()
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.UpsertAttemptResult(context.Background(), state.AttemptResult{
		IssueKey:             "CAG-141",
		Attempt:              1,
		WorkspacePath:        filepath.Join(root, "CAG-141"),
		BranchName:           expectedWorkspaceBranch("CAG-141"),
		Status:               runAttemptStatusReviewFailed,
		PRURL:                "https://github.com/weskor/agent-machine/pull/93",
		ReviewStatus:         "failed",
		ReviewClassification: reviewClassificationBehaviorSpecBlocker,
		ReviewOutputRef:      "/tmp/review-output.txt",
		ReviewOutputHash:     "review-output-hash",
		UpdatedAt:            time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	feedback := repairReviewFailedPromptFeedback(store, "CAG-141", "# Existing PR feedback\n\nResolve prior feedback.")

	for _, expected := range []string{"Existing PR feedback", "Resolve prior feedback.", "Prior review state", "Review status: failed", reviewClassificationBehaviorSpecBlocker, "/tmp/review-output.txt", "review-output-hash"} {
		if !strings.Contains(feedback, expected) {
			t.Fatalf("repair feedback missing %q: %q", expected, feedback)
		}
	}
}

func TestFeedbackHashStableForWhitespace(t *testing.T) {
	first := feedbackHash("\n# PR feedback\n\nPlease add tests.\n")
	second := feedbackHash("# PR feedback\n\nPlease add tests.")
	if first == "" || first != second {
		t.Fatalf("feedback hash should be stable for surrounding whitespace: %q %q", first, second)
	}
}
