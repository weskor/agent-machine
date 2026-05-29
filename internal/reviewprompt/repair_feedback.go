package reviewprompt

import (
	"context"
	"fmt"
	"strings"

	"github.com/weskor/agent-machine/internal/attemptoutcome"
	"github.com/weskor/agent-machine/internal/state"
)

type ReviewStateReader interface {
	ReconciliationFacts(context.Context, string) (state.ReconciliationFacts, bool, error)
}

func RepairReviewFailedFeedback(reader ReviewStateReader, issueKey, existingFeedback string) string {
	if reader == nil {
		return existingFeedback
	}
	facts, ok, err := reader.ReconciliationFacts(context.Background(), issueKey)
	if err != nil || !ok || facts.Status != attemptoutcome.StatusReviewFailed {
		return existingFeedback
	}
	var builder strings.Builder
	if strings.TrimSpace(existingFeedback) != "" {
		builder.WriteString(strings.TrimSpace(existingFeedback))
		fmt.Fprintln(&builder)
		fmt.Fprintln(&builder)
	}
	fmt.Fprintln(&builder, "# Prior review state")
	fmt.Fprintln(&builder)
	if strings.TrimSpace(facts.ReviewStatus) != "" {
		fmt.Fprintf(&builder, "Review status: %s\n", facts.ReviewStatus)
	}
	if strings.TrimSpace(facts.ReviewClassification) != "" {
		fmt.Fprintf(&builder, "Review classification: %s\n", facts.ReviewClassification)
	}
	if strings.TrimSpace(facts.ReviewOutputRef) != "" {
		fmt.Fprintf(&builder, "Review output ref: %s\n", facts.ReviewOutputRef)
	}
	if strings.TrimSpace(facts.ReviewOutputHash) != "" {
		fmt.Fprintf(&builder, "Review output hash: %s\n", facts.ReviewOutputHash)
	}
	fmt.Fprintln(&builder)
	fmt.Fprintln(&builder, "The prior semantic review failed. Repair the failed behavior/spec findings before handoff.")
	return builder.String()
}
