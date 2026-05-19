package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func hasUnresolvedReviewFailure(workspaceRoot, identifier string) bool {
	data, err := os.ReadFile(filepath.Join(workspaceRoot, identifier, ".pi-symphony-run.json"))
	if err != nil {
		return false
	}
	var record runRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return false
	}
	return record.Status == "review_failed" && record.PRURL != ""
}

type needsInfoResult struct {
	NeedsInfo bool
	Questions []string
}

func parseNeedsInfo(output string) needsInfoResult {
	text := assistantText(output)
	if text == "" {
		text = output
	}
	lines := strings.Split(text, "\n")
	found := false
	var questions []string
	for _, line := range lines {
		clean := strings.TrimSpace(line)
		if strings.Contains(clean, "NEEDS_INFO") {
			found = true
			continue
		}
		if !found || clean == "" {
			continue
		}
		trimmed := strings.TrimLeft(clean, "-• \t")
		if isNumberedQuestion(trimmed) {
			questions = append(questions, sanitizeMarkdownLine(trimmed))
		}
	}
	return needsInfoResult{NeedsInfo: found, Questions: questions}
}

func isNumberedQuestion(line string) bool {
	dot := strings.Index(line, ".")
	paren := strings.Index(line, ")")
	idx := dot
	if idx == -1 || (paren != -1 && paren < idx) {
		idx = paren
	}
	if idx <= 0 || idx > 3 || idx >= len(line)-1 {
		return false
	}
	for _, r := range line[:idx] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return strings.TrimSpace(line[idx+1:]) != ""
}

func renderNeedsInfoComment(questions []string) string {
	var builder strings.Builder
	builder.WriteString("Go/Pi run stopped because the ticket needs additional information. Please answer the questions below, then move the issue back to Ready for Agent.\n\n")
	if len(questions) == 0 {
		builder.WriteString("1. Please clarify the missing requirements so the agent can proceed safely.")
		return builder.String()
	}
	for i, question := range questions {
		fmt.Fprintf(&builder, "%d. %s\n", i+1, strings.TrimSpace(stripQuestionNumber(question)))
	}
	return truncateMarkdown(strings.TrimSpace(builder.String()), 2000)
}

func stripQuestionNumber(question string) string {
	trimmed := strings.TrimSpace(question)
	for i, r := range trimmed {
		if (r == '.' || r == ')') && i > 0 {
			return strings.TrimSpace(trimmed[i+1:])
		}
	}
	return trimmed
}

func runRecordFor(candidate *issue, workspace, piCommand, githubAuth string, startedAt, endedAt time.Time, piUsage *usage, review *reviewResult, prURL, status, errorMessage string, budget *runBudget, budgetExceeded string) runRecord {
	reviewStatus := ""
	reviewClassification := ""
	reviewFindings := ""
	var reviewUsage *usage
	if review != nil {
		reviewStatus = review.Status
		reviewClassification = review.Classification
		reviewFindings = review.Findings
		reviewUsage = review.Usage
	}
	branch, _ := currentGitBranch(workspace)
	root := filepath.Dir(workspace)
	feedback, _ := readPRFeedback(workspace)
	record := runRecord{IssueIdentifier: candidate.Identifier, IssueID: candidate.ID, IssueTitle: candidate.Title, IssueURL: candidate.URL, Workspace: workspace, WorkspaceRoot: root, Branch: branch, ExpectedBranch: expectedWorkspaceBranch(candidate.Identifier), PiCommand: piCommand, GitHubAuth: githubAuth, StartedAt: startedAt, EndedAt: endedAt, DurationMS: endedAt.Sub(startedAt).Milliseconds(), PiUsage: piUsage, ReviewStatus: reviewStatus, ReviewClassification: reviewClassification, ReviewFindings: reviewFindings, ReviewUsage: reviewUsage, PRURL: prURL, FeedbackHash: feedbackHash(feedback), Status: status, Error: errorMessage, Budget: budget, BudgetExceeded: budgetExceeded}
	record.BehaviorContractEvidence = behaviorContractEvidenceForRun(record)
	record.BehaviorContractEvidence = append(record.BehaviorContractEvidence, ticketContractEvidenceForRun(record)...)
	return record
}

func behaviorContractPreflightPrompt() string {
	return `Behavior-contract preflight for refactors, replacements, and rewrites:
- Read the relevant agent docs before planning broad runner work: CONTEXT.md for domain language, LANGUAGE.md for architecture vocabulary, docs/adr/ for durable decisions, docs/specs/ for observable contracts, and docs/agents/review-policy.md for evidence expectations.
- Before changing code, commands, dependencies, integrations, workflows, or state-machine logic, inventory the existing observable contract: inputs/outputs, side effects, cleanup, error handling, security/ownership assumptions, state transitions, and hidden operational contracts.
- Add a Behavior Contract Evidence section to the PR body or tracker handoff: cite relevant specs/ADRs, list behavior preserved, behavior intentionally changed with justification, and unknown behavior that needs clarification.
- Update docs/specs/ when observable behavior intentionally changes; for mechanical refactors, state that no spec changes were needed.
- Use TDD or characterization tests for old observable behavior before proving the new abstraction; tests only around the new design are not enough.
- State a complexity/LOC budget before implementation: expected files touched, expected LOC direction, why any net growth is acceptable, what bespoke code is removed, and when the work must split.
- If the existing contract cannot be determined safely, output NEEDS_INFO instead of guessing.`
}

func behaviorContractEvidenceForRun(record runRecord) []string {
	evidence := []string{"implementation_prompt_required_behavior_contract_preflight"}
	if record.ReviewStatus != "" {
		evidence = append(evidence, "review_prompt_required_behavior_contract_parity_check")
	}
	if record.ReviewStatus == "passed" {
		evidence = append(evidence, "review_passed_behavior_contract_gate")
	}
	if record.ReviewStatus == "failed" {
		evidence = append(evidence, "review_failed_behavior_contract_or_scope_gate")
	}
	if record.ReviewClassification != "" {
		evidence = append(evidence, "review_classification_"+record.ReviewClassification)
	}
	if strings.HasPrefix(record.Status, "needs_info") {
		evidence = append(evidence, "needs_info_used_for_unknown_behavior_contract")
	}
	if strings.TrimSpace(record.Error) != "" || strings.TrimSpace(record.ReviewFindings) != "" {
		evidence = append(evidence, "findings_recorded_for_behavior_contract_audit")
	}
	return evidence
}

func expectedWorkspaceBranch(identifier string) string {
	return "symphony/" + strings.TrimSpace(identifier) + "-workspace"
}

func validatePRForHandoff(config runnerConfig, candidate *issue, prURL string) (string, error) {
	github, ctx, cancel, err := githubClientWithTimeout(config.Budget.GitHubTimeout)
	if err != nil {
		return "", err
	}
	defer cancel()
	details, err := github.PullRequestHandoffDetails(ctx, prURL)
	if err != nil {
		return "", fmt.Errorf("GitHub API PR handoff lookup failed for %s: %w", prURL, err)
	}
	return prHandoffBlockReason(config, candidate, details), nil
}

func prHandoffBlockReason(config runnerConfig, candidate *issue, details prHandoffDetails) string {
	var reasons []string
	baseBranch := strings.TrimSpace(config.BaseBranch)
	if baseBranch == "" {
		baseBranch = "develop"
	}
	if !strings.EqualFold(details.BaseRefName, baseBranch) {
		reasons = append(reasons, fmt.Sprintf("PR base branch is %q; expected %q", emptyAsUnknown(details.BaseRefName), baseBranch))
	}
	expectedBranch := expectedWorkspaceBranch(candidate.Identifier)
	if details.HeadRefName != expectedBranch {
		reasons = append(reasons, fmt.Sprintf("PR head branch is %q; expected %q", emptyAsUnknown(details.HeadRefName), expectedBranch))
	}
	if details.ChangedFiles > 80 {
		reasons = append(reasons, fmt.Sprintf("PR changes %d files, exceeding the scoped-run limit of 80", details.ChangedFiles))
	}
	if details.Additions > 5000 {
		reasons = append(reasons, fmt.Sprintf("PR adds %d lines, exceeding the scoped-run limit of 5000", details.Additions))
	}
	return strings.Join(reasons, "; ")
}
