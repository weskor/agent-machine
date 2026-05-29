package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/weskor/agent-machine/internal/commenttext"
	"github.com/weskor/agent-machine/internal/handoffcomment"
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
	return handoffcomment.RenderPRBody(handoffCommentSummary(summary))
}

func renderLinearHandoffComment(summary handoffSummary) string {
	return handoffcomment.RenderLinearComment(handoffCommentSummary(summary))
}

func handoffCommentSummary(summary handoffSummary) handoffcomment.Summary {
	out := handoffcomment.Summary{
		IssueIdentifier:   summary.IssueIdentifier,
		IssueTitle:        summary.IssueTitle,
		IssueURL:          summary.IssueURL,
		IssueDescription:  summary.IssueDescription,
		PRURL:             summary.PRURL,
		RuntimeUsage:      usageSummary(summary.RuntimeUsage),
		Review:            reviewSummary(summary.Review),
		Duration:          summary.Duration,
		Validation:        summary.Validation,
		ScopeGuardSummary: strings.TrimSpace(summary.ScopeResult.Summary()),
		ScopeGuardChecked: summary.ScopeResult.Checked,
		FollowUps:         summary.FollowUps,
		Classification:    summary.Classification,
	}
	if summary.Review != nil {
		out.ReviewStatus = summary.Review.Status
		out.ReviewClassification = summary.Review.Classification
	}
	if summary.PRDetails != nil {
		out.PRDetails = &handoffcomment.PRDetails{ChangedFiles: summary.PRDetails.ChangedFiles, Additions: summary.PRDetails.Additions, Deletions: summary.PRDetails.Deletions}
	}
	if summary.Progress != nil {
		out.Progress = &handoffcomment.Progress{Phase: summary.Progress.Phase, Status: summary.Progress.Status, NextAction: summary.Progress.NextAction, PRURL: summary.Progress.PRURL, ProgressPath: summary.Progress.ProgressPath}
	}
	return out
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
	return commenttext.SectionLines(description, names...)
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
	commenttext.WriteBoundedBullets(builder, values, empty, limit)
}

func markdownLink(label, target string) string {
	return commenttext.MarkdownLink(label, target)
}

func sanitizeMarkdownLine(value string) string {
	return commenttext.SanitizeLine(value)
}

func truncateMarkdown(value string, limit int) string {
	return commenttext.Truncate(value, limit)
}

func uniqueStrings(values []string) []string {
	return commenttext.Unique(values)
}
