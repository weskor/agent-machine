package reviewprompt

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weskor/agent-machine/internal/attemptoutcome"
	"github.com/weskor/agent-machine/internal/reviewpolicy"
	"github.com/weskor/agent-machine/internal/state"
)

func TestRepairReviewFailedFeedbackUsesSQLiteReviewState(t *testing.T) {
	store := testStateStore(t)
	writeReviewFailedAttempt(t, store, t.TempDir())

	feedback := RepairReviewFailedFeedback(store, "CAG-141", "")

	for _, expected := range []string{"Prior review state", "Review status: failed", reviewpolicy.BehaviorSpecBlocker, "/tmp/review-output.txt", "review-output-hash", "prior semantic review failed"} {
		if !strings.Contains(feedback, expected) {
			t.Fatalf("repair feedback missing %q: %q", expected, feedback)
		}
	}
}

func TestRepairReviewFailedFeedbackAppendsSQLiteReviewStateToExistingFeedback(t *testing.T) {
	store := testStateStore(t)
	writeReviewFailedAttempt(t, store, t.TempDir())

	feedback := RepairReviewFailedFeedback(store, "CAG-141", "# Existing PR feedback\n\nResolve prior feedback.")

	for _, expected := range []string{"Existing PR feedback", "Resolve prior feedback.", "Prior review state", "Review status: failed", reviewpolicy.BehaviorSpecBlocker, "/tmp/review-output.txt", "review-output-hash"} {
		if !strings.Contains(feedback, expected) {
			t.Fatalf("repair feedback missing %q: %q", expected, feedback)
		}
	}
}

func testStateStore(t *testing.T) *state.Store {
	t.Helper()
	root := t.TempDir()
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func writeReviewFailedAttempt(t *testing.T, store *state.Store, root string) {
	t.Helper()
	if err := store.UpsertAttemptResult(context.Background(), state.AttemptResult{
		IssueKey:             "CAG-141",
		Attempt:              1,
		WorkspacePath:        filepath.Join(root, "CAG-141"),
		BranchName:           attemptoutcome.ExpectedWorkspaceBranch("CAG-141"),
		Status:               attemptoutcome.StatusReviewFailed,
		PRURL:                "https://github.com/weskor/agent-machine/pull/93",
		ReviewStatus:         "failed",
		ReviewClassification: reviewpolicy.BehaviorSpecBlocker,
		ReviewOutputRef:      "/tmp/review-output.txt",
		ReviewOutputHash:     "review-output-hash",
		UpdatedAt:            time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
}
