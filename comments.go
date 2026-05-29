package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/weskor/agent-machine/internal/runclassification"
)

type handoffSummary struct {
	IssueIdentifier  string
	IssueTitle       string
	IssueURL         string
	IssueDescription string
	PRURL            string
	RuntimeUsage     *usage
	Review           *reviewResult
	Duration         time.Duration
	Validation       []string
	ScopeResult      scopeGuardResult
	FollowUps        []string
	Classification   *runclassification.Classification
	PRDetails        *prHandoffDetails
	Progress         *runProgressSnapshot
}

func renderPRHandoffBody(summary handoffSummary) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "## Agent Machine handoff\n\n")
	fmt.Fprintf(&builder, "- Issue: %s — %s\n", markdownLink(summary.IssueIdentifier, summary.IssueURL), sanitizeMarkdownLine(summary.IssueTitle))
	fmt.Fprintf(&builder, "- PR: %s\n", markdownLink(summary.PRURL, summary.PRURL))
	fmt.Fprintf(&builder, "- Review: %s\n", sanitizeMarkdownLine(reviewSummary(summary.Review)))
	fmt.Fprintf(&builder, "- Usage: %s\n", sanitizeMarkdownLine(usageSummary(summary.RuntimeUsage)))
	fmt.Fprintf(&builder, "- Duration: %s\n\n", summary.Duration.Round(time.Second))

	builder.WriteString("### Issue scope\n")
	writeBoundedBullets(&builder, issueScopeNotes(summary), "Issue scope summary not recorded.", 6)

	builder.WriteString("### Validation\n")
	writeBoundedBullets(&builder, summary.Validation, "No validation commands detected in runner output.", 5)

	builder.WriteString("\n### Tests and characterization\n")
	writeBoundedBullets(&builder, testEvidenceNotes(summary), "No test or characterization evidence detected in runner output.", 5)

	builder.WriteString("\n### Behavior Contract Evidence\n")
	writeBoundedBullets(&builder, behaviorContractEvidenceNotes(summary), "No behavior-contract evidence recorded.", 8)

	builder.WriteString("\n### Changed files\n")
	writeBoundedBullets(&builder, changedFilesNotes(summary), "Changed file summary not recorded.", 3)

	builder.WriteString("\n### Risks and out of scope\n")
	writeBoundedBullets(&builder, riskAndScopeNotes(summary), "No known risks or out-of-scope follow-up recorded.", 5)

	builder.WriteString("\n### Progress status\n")
	writeBoundedBullets(&builder, progressStatusNotes(summary), "Progress snapshot not recorded.", 4)

	builder.WriteString("\n### Remaining follow-up\n")
	writeBoundedBullets(&builder, summary.FollowUps, "No follow-up recorded.", 4)

	return truncateMarkdown(builder.String(), 12000)
}

func behaviorContractEvidenceNotes(summary handoffSummary) []string {
	notes := []string{
		"References: docs/specs/end-to-end-orchestration.md, docs/specs/harness-behavior.md, and docs/agents/review-policy.md.",
		"Behavior inventory: runner-owned PR identity, branch/base validation, review classification, Linear handoff comments/state movement, and run/evaluation artifacts.",
		"Preserved behavior: implementation agents still do not create, update, push, or comment on code-host PRs directly.",
		"Handoff evidence source: runner-owned PR body; separate code-host PR summary comments are not used.",
		"Spec compatibility: observable behavior is preserved unless the issue and docs/specs explicitly describe a change.",
		"Complexity/LOC budget: changed-file counts and additions/deletions are recorded below when reported by the code host.",
		"Review classification: " + reviewClassificationSummary(summary.Review),
	}
	if summary.Classification != nil {
		notes = append(notes, "Run classification: outcome="+summary.Classification.Outcome+", root="+emptyAsNA(summary.Classification.RootCause)+", next="+emptyAsNA(summary.Classification.NextAction)+".")
	}
	return notes
}

func issueScopeNotes(summary handoffSummary) []string {
	var notes []string
	for _, line := range issueDescriptionSectionLines(summary.IssueDescription, "scope") {
		notes = append(notes, "Scope: "+line)
	}
	scopeSummary := strings.TrimSpace(summary.ScopeResult.Summary())
	if scopeSummary != "" {
		notes = append(notes, "Scope guard: "+scopeSummary)
	} else if summary.ScopeResult.Checked {
		notes = append(notes, "Scope guard: changed files matched the Linear ticket path contract.")
	}
	if len(notes) == 0 && strings.TrimSpace(summary.IssueIdentifier) != "" {
		notes = append(notes, "Issue identifier: "+summary.IssueIdentifier+".")
	}
	return uniqueStrings(notes)
}

func testEvidenceNotes(summary handoffSummary) []string {
	var notes []string
	for _, line := range summary.Validation {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "test") || strings.Contains(lower, "characterization") || strings.Contains(lower, "make ci") || strings.Contains(lower, "go test") {
			notes = append(notes, line)
		}
	}
	return uniqueStrings(notes)
}

func changedFilesNotes(summary handoffSummary) []string {
	if summary.PRDetails == nil {
		return nil
	}
	details := summary.PRDetails
	return []string{
		fmt.Sprintf("Files changed: %d; additions: %d; deletions: %d.", details.ChangedFiles, details.Additions, details.Deletions),
	}
}

func riskAndScopeNotes(summary handoffSummary) []string {
	var notes []string
	for _, line := range issueDescriptionSectionLines(summary.IssueDescription, "out of scope", "out-of-scope", "out of scope paths", "out-of-scope paths") {
		notes = append(notes, "Out of scope: "+line)
	}
	if summary.PRDetails != nil && summary.PRDetails.ChangedFiles > 80 {
		notes = append(notes, fmt.Sprintf("Risk: PR changes %d files, above the scoped-run warning threshold.", summary.PRDetails.ChangedFiles))
	}
	if summary.Review != nil && strings.TrimSpace(summary.Review.Status) != "" && summary.Review.Status != "passed" {
		notes = append(notes, "Risk: review status is "+summary.Review.Status+"; see follow-up and review artifacts.")
	}
	return uniqueStrings(notes)
}

func progressStatusNotes(summary handoffSummary) []string {
	if summary.Progress == nil {
		return nil
	}
	progress := summary.Progress
	notes := []string{
		"Phase: " + emptyAsNA(progress.Phase) + "; status: " + emptyAsNA(progress.Status) + "; next: " + emptyAsNA(progress.NextAction) + ".",
	}
	if progress.PRURL != "" {
		notes = append(notes, "Progress PR: "+progress.PRURL+".")
	}
	if progress.ProgressPath != "" {
		notes = append(notes, "Progress artifact: "+progress.ProgressPath+".")
	}
	return notes
}

func reviewClassificationSummary(review *reviewResult) string {
	if review == nil || strings.TrimSpace(review.Classification) == "" {
		return "not recorded"
	}
	return review.Classification
}

func renderLinearHandoffComment(summary handoffSummary) string {
	return truncateMarkdown(fmt.Sprintf("Runtime run completed.\n\nPR: %s\nUsage: %s\nReview: %s\nDuration: %s", summary.PRURL, usageSummary(summary.RuntimeUsage), reviewSummary(summary.Review), summary.Duration.Round(time.Second)), 1000)
}

func updatePRHandoffBody(summary handoffSummary) error {
	github, ctx, cancel, err := codeHostClientForPRURLWithTimeout(summary.PRURL, defaultGitHubCommandTimeout)
	if err != nil {
		return err
	}
	defer cancel()

	details, err := github.PullRequestHandoffDetails(ctx, summary.PRURL)
	if err != nil {
		return fmt.Errorf("code-host PR handoff body lookup failed for %s: %w", summary.PRURL, err)
	}
	if details.URL != "" {
		summary.PRURL = details.URL
	}
	summary.PRDetails = &details
	title, _ := handoffPRTitleBody(&issue{Identifier: summary.IssueIdentifier, Title: summary.IssueTitle})
	base := details.BaseRefName
	if strings.TrimSpace(base) == "" {
		base = "main"
	}
	if _, err := github.UpdatePullRequest(ctx, details.Number, title, renderPRHandoffBody(summary), base); err != nil {
		return fmt.Errorf("code-host PR handoff body update failed for %s: %w", summary.PRURL, err)
	}
	return nil
}

func validationLines(output string) []string {
	text := assistantText(output)
	if text == "" {
		text = output
	}
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		clean := sanitizeMarkdownLine(strings.Trim(line, " -•\t`"))
		lower := strings.ToLower(clean)
		if clean == "" || len(clean) > 180 {
			continue
		}
		if strings.Contains(lower, "bun run ") || strings.Contains(lower, "git diff --check") || strings.Contains(lower, "go test") || strings.Contains(lower, "validation") {
			lines = append(lines, clean)
		}
	}
	return uniqueStrings(lines)
}

func issueDescriptionSectionLines(description string, names ...string) []string {
	if strings.TrimSpace(description) == "" {
		return nil
	}
	wanted := map[string]bool{}
	for _, name := range names {
		wanted[normalizeIssueSectionName(name)] = true
	}
	inSection := false
	var lines []string
	for _, line := range strings.Split(description, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if inSection && len(lines) > 0 {
				break
			}
			continue
		}
		if name, ok := issueSectionHeading(trimmed); ok {
			if inSection && len(lines) > 0 {
				break
			}
			inSection = wanted[normalizeIssueSectionName(name)]
			continue
		}
		if !inSection {
			continue
		}
		if strings.HasPrefix(trimmed, "-") || strings.HasPrefix(trimmed, "*") {
			lines = append(lines, sanitizeMarkdownLine(strings.TrimSpace(strings.TrimLeft(trimmed, "-* "))))
			continue
		}
		lines = append(lines, sanitizeMarkdownLine(trimmed))
	}
	return uniqueStrings(lines)
}

func issueSectionHeading(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	trimmed = strings.TrimLeft(trimmed, "#")
	trimmed = strings.TrimSpace(strings.TrimSuffix(trimmed, ":"))
	if trimmed == "" || len(trimmed) > 80 {
		return "", false
	}
	lower := strings.ToLower(trimmed)
	switch lower {
	case "goal", "scope", "requirements", "acceptance criteria", "validation", "out of scope", "out-of-scope", "out of scope paths", "out-of-scope paths", "risks":
		return lower, true
	default:
		return "", false
	}
}

func normalizeIssueSectionName(name string) string {
	return strings.ToLower(strings.TrimSpace(strings.Trim(name, "#: ")))
}

func followUpLines(review *reviewResult) []string {
	if review == nil || strings.TrimSpace(review.Findings) == "" || review.Status == "passed" {
		return nil
	}
	return []string{sanitizeMarkdownLine(review.Status + ": " + firstNonMarkerLine(review.Findings))}
}

func firstNonMarkerLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		clean := strings.TrimSpace(line)
		if clean != "" && clean != reviewPassMarker && clean != reviewFailMarker {
			return clean
		}
	}
	return "Review findings recorded in runner artifacts."
}

func writeBoundedBullets(builder *strings.Builder, values []string, empty string, limit int) {
	if len(values) == 0 {
		fmt.Fprintf(builder, "- %s\n", empty)
		return
	}
	for i, value := range values {
		if i >= limit {
			fmt.Fprintf(builder, "- …and %d more.\n", len(values)-limit)
			return
		}
		fmt.Fprintf(builder, "- %s\n", sanitizeMarkdownLine(value))
	}
}

func markdownLink(label, target string) string {
	label = sanitizeMarkdownLine(label)
	target = strings.TrimSpace(target)
	if target == "" {
		return label
	}
	return fmt.Sprintf("[%s](%s)", label, target)
}

func sanitizeMarkdownLine(value string) string {
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "`", "'")
	value = strings.Join(strings.Fields(value), " ")
	return truncateMarkdown(value, 240)
}

func truncateMarkdown(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	if limit <= 1 {
		return "…"
	}
	return strings.TrimSpace(value[:limit-1]) + "…"
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
