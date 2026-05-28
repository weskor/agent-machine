package main

import (
	"strings"
	"testing"
	"time"
)

func TestRenderPRHandoffBodyIsReadableAndBounded(t *testing.T) {
	summary := handoffSummary{
		IssueIdentifier: "CAG-15",
		IssueTitle:      "Readable handoff comments",
		IssueURL:        "https://linear.app/wessismore/issue/CAG-15/example",
		PRURL:           "https://github.com/weskor/agent-machine/pull/407",
		RuntimeUsage:    &usage{Input: 10, Output: 5, TotalTokens: 15, Cost: &usageCost{Total: 0.25}},
		Review:          &reviewResult{Status: "passed"},
		Duration:        90 * time.Second,
		Validation:      []string{"bun run am:pi:test", "git diff --check"},
		Classification:  &runClassification{Outcome: "handoff_ready", NextAction: "await_approval_and_green_checks"},
		PRDetails:       &prHandoffDetails{ChangedFiles: 3, Additions: 20, Deletions: 4},
		Progress:        &runProgressSnapshot{Phase: "handoff_pending", Status: "handoff_pending", NextAction: "move_to_human_review", ProgressPath: "/tmp/progress.json"},
	}

	markdown := renderPRHandoffBody(summary)
	for _, expected := range []string{"## Agent Machine handoff", "### Validation", "### Behavior Contract Evidence", "docs/specs/harness-behavior.md", "docs/agents/review-policy.md", "Specs: preserved", "Run classification: outcome=handoff_ready", "### Changed files", "Files changed: 3", "### Risks and out of scope", "### Progress status", "handoff_pending", "### Remaining follow-up", "bun run am:pi:test", "passed"} {
		if !strings.Contains(markdown, expected) {
			t.Fatalf("expected %q in markdown:\n%s", expected, markdown)
		}
	}
	if strings.Contains(markdown, "<!-- am-summary -->") {
		t.Fatalf("PR body should not contain the retired PR comment marker:\n%s", markdown)
	}
	if len(markdown) > 3800 {
		t.Fatalf("expected bounded markdown, got %d bytes", len(markdown))
	}
}

func TestRenderPRHandoffBodySanitizesAndTruncates(t *testing.T) {
	long := strings.Repeat("x", 5000)
	markdown := renderPRHandoffBody(handoffSummary{
		IssueIdentifier: "CAG-15",
		IssueTitle:      "Title with\nnewline and `ticks`",
		PRURL:           "https://github.com/weskor/agent-machine/pull/407",
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

func TestValidationLinesExtractsSafeCommandSummaries(t *testing.T) {
	output := `noise
{"message":{"role":"assistant","content":[{"type":"text","text":"Validation:\n- bun run am:pi:test\n- git diff --check\n- unrelated prose"}]}}
`
	got := validationLines(output)
	if len(got) != 3 || got[1] != "bun run am:pi:test" || got[2] != "git diff --check" {
		t.Fatalf("unexpected validation lines: %#v", got)
	}
}
