package main

import (
	"strings"
	"testing"
	"time"
)

func TestRenderPRHandoffCommentIsReadableAndBounded(t *testing.T) {
	summary := handoffSummary{
		IssueIdentifier: "CAG-15",
		IssueTitle:      "Readable handoff comments",
		IssueURL:        "https://linear.app/wessismore/issue/CAG-15/example",
		PRURL:           "https://github.com/pennywise-investments/compound-web/pull/407",
		RuntimeUsage:    &usage{Input: 10, Output: 5, TotalTokens: 15, Cost: &usageCost{Total: 0.25}},
		Review:          &reviewResult{Status: "passed"},
		Duration:        90 * time.Second,
		Validation:      []string{"bun run symphony:pi:test", "git diff --check"},
		Classification:  &runClassification{Outcome: "handoff_ready", NextAction: "await_approval_and_green_checks"},
	}

	markdown := renderPRHandoffComment(summary)
	for _, expected := range []string{prSummaryMarker, "## Pi Symphony handoff", "### Validation", "### Behavior Contract Evidence", "docs/specs/harness-behavior.md", "docs/agents/review-policy.md", "Specs: preserved", "Run classification: outcome=handoff_ready", "### Remaining follow-up", "bun run symphony:pi:test", "passed"} {
		if !strings.Contains(markdown, expected) {
			t.Fatalf("expected %q in markdown:\n%s", expected, markdown)
		}
	}
	if len(markdown) > 3800 {
		t.Fatalf("expected bounded markdown, got %d bytes", len(markdown))
	}
}

func TestRenderPRHandoffCommentSanitizesAndTruncates(t *testing.T) {
	long := strings.Repeat("x", 5000)
	markdown := renderPRHandoffComment(handoffSummary{
		IssueIdentifier: "CAG-15",
		IssueTitle:      "Title with\nnewline and `ticks`",
		PRURL:           "https://github.com/pennywise-investments/compound-web/pull/407",
		Validation:      []string{"bun run check\nsecond line", "raw `code` marker", long},
		FollowUps:       []string{long},
	})

	if len(markdown) > 3800 {
		t.Fatalf("expected truncated markdown, got %d bytes", len(markdown))
	}
	if strings.Contains(markdown, "Title with\nnewline") || strings.Contains(markdown, "`code`") {
		t.Fatalf("expected line sanitization, got:\n%s", markdown)
	}
	if !strings.Contains(markdown, "…") {
		t.Fatalf("expected truncation marker, got:\n%s", markdown)
	}
}

func TestPRNumberFromURL(t *testing.T) {
	if got := prNumberFromURL("https://github.com/pennywise-investments/compound-web/pull/407"); got != "407" {
		t.Fatalf("unexpected PR number: %q", got)
	}
	if got := prNumberFromURL("https://example.com/pull/407"); got != "" {
		t.Fatalf("expected non-GitHub URL to be ignored, got %q", got)
	}
}

func TestValidationLinesExtractsSafeCommandSummaries(t *testing.T) {
	output := `noise
{"message":{"role":"assistant","content":[{"type":"text","text":"Validation:\n- bun run symphony:pi:test\n- git diff --check\n- unrelated prose"}]}}
`
	got := validationLines(output)
	if len(got) != 3 || got[1] != "bun run symphony:pi:test" || got[2] != "git diff --check" {
		t.Fatalf("unexpected validation lines: %#v", got)
	}
}
