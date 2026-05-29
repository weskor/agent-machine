package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	artifactio "github.com/weskor/agent-machine/internal/artifacts"
	"github.com/weskor/agent-machine/internal/prfeedback"
	"github.com/weskor/agent-machine/internal/state"
)

const repairReviewFindingsNextAction = "repair_review_findings_before_handoff"

func collectPRFeedback(prURL string, prNumber int) (string, error) {
	github, ctx, cancel, err := codeHostClientForPRURLWithTimeout(prURL, defaultGitHubCommandTimeout)
	if err != nil {
		return "", err
	}
	defer cancel()
	feedback, err := github.PullRequestFeedback(ctx, prNumber)
	if err != nil {
		return "", fmt.Errorf("code-host API PR feedback lookup failed for #%d: %w", prNumber, err)
	}
	return renderPRFeedback(prNumber, feedback), nil
}

func feedbackHash(feedback string) string {
	return artifactio.FeedbackHash(feedback)
}

func writePRFeedback(workspace string, prNumber int, feedback string) error {
	path, err := artifactio.WritePRFeedback(workspace, prNumber, feedback)
	if err != nil {
		return err
	}
	log("wrote PR feedback: %s", path)
	return nil
}

func readPRFeedback(workspace string) (string, error) {
	return artifactio.ReadPRFeedback(workspace)
}

func decisionWithRepairableReviewFailedPR(config runnerConfig, candidate issue, pr *pullRequestSummary, decision reconciliationDecision) reconciliationDecision {
	if !repairableReviewFailedPR(config, candidate, pr, decision) {
		return decision
	}
	decision.Lifecycle = lifecycleReviewFailed
	decision.CanRun = true
	decision.CanMerge = false
	decision.ShouldRetry = true
	decision.Blockers = nil
	decision.NextAction = repairReviewFindingsNextAction
	return decision
}

func repairableReviewFailedPR(config runnerConfig, candidate issue, pr *pullRequestSummary, decision reconciliationDecision) bool {
	if pr == nil || !stateIsRunnable(candidate.State.Name, config) {
		return false
	}
	if decision.NextAction != "reconcile_linear_state" && decision.NextAction != repairReviewFindingsNextAction {
		return false
	}
	if decision.ReconciliationNeeded {
		return false
	}
	if prInvariantBlockReason(config, candidate, *pr) != "" {
		return false
	}
	if decision.DBFacts != nil {
		if strings.TrimSpace(decision.DBFacts.Status) != "" && decision.DBFacts.Status != runAttemptStatusReviewFailed {
			return false
		}
		if strings.TrimSpace(decision.DBFacts.PRURL) != "" && decision.DBFacts.PRURL != pr.URL {
			return false
		}
		if decision.DBFacts.Status == runAttemptStatusReviewFailed {
			return decision.DBFacts.ReviewStatus != "passed" && decision.DBFacts.ReviewClassification == reviewClassificationBehaviorSpecBlocker
		}
		return false
	}
	workspace := filepath.Join(config.WorkspaceRoot, candidate.Identifier)
	record, ok := readRunArtifact(workspace)
	if !ok || record.Status != runAttemptStatusReviewFailed || record.ReviewStatus == "passed" || record.ReviewClassification != reviewClassificationBehaviorSpecBlocker {
		return false
	}
	if strings.TrimSpace(record.PRURL) != "" && record.PRURL != pr.URL {
		return false
	}
	evaluation, ok := readEvaluationArtifact(workspace)
	if !ok {
		return false
	}
	if evaluation.Outcome != runAttemptStatusReviewFailed || !evaluation.ShouldRetry || evaluation.NextAction != repairReviewFindingsNextAction {
		return false
	}
	if evaluation.ReviewClassification != "" && evaluation.ReviewClassification != reviewClassificationBehaviorSpecBlocker {
		return false
	}
	if strings.TrimSpace(evaluation.PRURL) != "" && evaluation.PRURL != pr.URL {
		return false
	}
	return true
}

func readEvaluationArtifact(workspace string) (evaluationArtifact, bool) {
	data, err := os.ReadFile(filepath.Join(workspace, evaluationArtifactName))
	if err != nil {
		return evaluationArtifact{}, false
	}
	if _, _, err := artifactio.ValidateArtifactSchema(data, evaluationArtifactName); err != nil {
		return evaluationArtifact{}, false
	}
	var evaluation evaluationArtifact
	if err := json.Unmarshal(data, &evaluation); err != nil {
		return evaluationArtifact{}, false
	}
	return evaluation, true
}

type reviewPromptStateReader interface {
	ReconciliationFacts(context.Context, string) (state.ReconciliationFacts, bool, error)
}

func repairReviewFailedPromptFeedback(reader reviewPromptStateReader, issueKey, existingFeedback string) string {
	if reader == nil {
		return existingFeedback
	}
	facts, ok, err := reader.ReconciliationFacts(context.Background(), issueKey)
	if err != nil || !ok || facts.Status != runAttemptStatusReviewFailed {
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

func renderPRFeedback(prNumber int, feedback prFeedback) string {
	return prfeedback.Render(prNumber, feedback)
}
