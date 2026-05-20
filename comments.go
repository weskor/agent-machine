package main

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const prSummaryMarker = "<!-- pi-symphony-summary -->"

var prNumberFromURLPattern = regexp.MustCompile(`/pull/([0-9]+)(?:$|[/?#])`)

type handoffSummary struct {
	IssueIdentifier string
	IssueTitle      string
	IssueURL        string
	PRURL           string
	PiUsage         *usage
	Review          *reviewResult
	Duration        time.Duration
	Validation      []string
	FollowUps       []string
	Classification  *runClassification
}

func renderPRHandoffComment(summary handoffSummary) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "%s\n## Pi Symphony handoff\n\n", prSummaryMarker)
	fmt.Fprintf(&builder, "- Issue: %s — %s\n", markdownLink(summary.IssueIdentifier, summary.IssueURL), sanitizeMarkdownLine(summary.IssueTitle))
	fmt.Fprintf(&builder, "- PR: %s\n", markdownLink(summary.PRURL, summary.PRURL))
	fmt.Fprintf(&builder, "- Review: %s\n", sanitizeMarkdownLine(reviewSummary(summary.Review)))
	fmt.Fprintf(&builder, "- Usage: %s\n", sanitizeMarkdownLine(usageSummary(summary.PiUsage)))
	fmt.Fprintf(&builder, "- Duration: %s\n\n", summary.Duration.Round(time.Second))

	builder.WriteString("### Validation\n")
	writeBoundedBullets(&builder, summary.Validation, "No validation commands detected in runner output.", 5)

	builder.WriteString("\n### Behavior Contract Evidence\n")
	writeBoundedBullets(&builder, behaviorContractEvidenceNotes(summary), "No behavior-contract evidence recorded.", 5)

	builder.WriteString("\n### Remaining follow-up\n")
	writeBoundedBullets(&builder, summary.FollowUps, "No follow-up recorded.", 4)

	return truncateMarkdown(builder.String(), 3800)
}

func behaviorContractEvidenceNotes(summary handoffSummary) []string {
	notes := []string{
		"References: docs/specs/harness-behavior.md and docs/agents/review-policy.md.",
		"Specs: preserved unless explicitly changed in this PR.",
		"Review classification: " + reviewClassificationSummary(summary.Review),
	}
	if summary.Classification != nil {
		notes = append(notes, "Run classification: outcome="+summary.Classification.Outcome+", root="+emptyAsNA(summary.Classification.RootCause)+", next="+emptyAsNA(summary.Classification.NextAction)+".")
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
	return truncateMarkdown(fmt.Sprintf("Go/Pi run completed.\n\nPR: %s\nUsage: %s\nReview: %s\nDuration: %s", summary.PRURL, usageSummary(summary.PiUsage), reviewSummary(summary.Review), summary.Duration.Round(time.Second)), 1000)
}

func postOrUpdatePRHandoffComment(summary handoffSummary) error {
	prNumber := prNumberFromURL(summary.PRURL)
	if prNumber == "" {
		return nil
	}
	number, err := strconv.Atoi(prNumber)
	if err != nil {
		return fmt.Errorf("invalid GitHub PR number %q: %w", prNumber, err)
	}
	github, ctx, cancel, err := githubClientWithTimeout(defaultGitHubCommandTimeout)
	if err != nil {
		return err
	}
	defer cancel()
	body := renderPRHandoffComment(summary)
	existingID, err := findExistingPRSummaryComment(github, ctx, prNumber)
	if err != nil {
		return err
	}
	if existingID != 0 {
		return github.UpdateIssueComment(ctx, existingID, body)
	}
	return github.CreateIssueComment(ctx, number, body)
}

func findExistingPRSummaryComment(github githubAPI, ctx context.Context, prNumber string) (int64, error) {
	comments, err := github.IssueComments(ctx, prNumber)
	if err != nil {
		return 0, fmt.Errorf("GitHub API issue comment lookup failed for PR #%s: %w", prNumber, err)
	}
	for _, comment := range comments {
		if strings.Contains(comment.Body, prSummaryMarker) {
			return comment.ID, nil
		}
	}
	return 0, nil
}

func prNumberFromURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host != "github.com" || parsed.Path == "" {
		return ""
	}
	matches := prNumberFromURLPattern.FindStringSubmatch(parsed.Path)
	if len(matches) < 2 {
		return ""
	}
	return matches[1]
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
