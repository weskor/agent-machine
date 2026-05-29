package handoffcomment

import (
	"strings"
	"testing"
	"time"

	"github.com/weskor/agent-machine/internal/runclassification"
)

func TestRenderPRBodyIsReadableAndBounded(t *testing.T) {
	summary := Summary{
		IssueIdentifier:      "CAG-15",
		IssueTitle:           "Readable handoff body",
		IssueURL:             "https://linear.app/wessismore/issue/CAG-15/example",
		IssueDescription:     "## Goal\n\nShip handoff evidence.\n\n## Scope\n\n* Runner handoff code and tests.\n\n## Out of Scope\n\n* Merge policy changes.\n",
		PRURL:                "https://github.com/weskor/agent-machine/pull/407",
		RuntimeUsage:         "15 tokens ($0.25)",
		Review:               "passed",
		ReviewStatus:         "passed",
		Duration:             90 * time.Second,
		Validation:           []string{"mise exec go -- go test ./...", "git diff --check"},
		ScopeGuardChecked:    true,
		ReviewClassification: "not recorded",
		Classification:       &runclassification.Classification{Outcome: "handoff_ready", NextAction: "await_approval_and_green_checks"},
		PRDetails:            &PRDetails{ChangedFiles: 3, Additions: 20, Deletions: 4},
		Progress:             &Progress{Phase: "handoff_pending", Status: "handoff_pending", NextAction: "move_to_human_review", ProgressPath: "/tmp/progress.json"},
	}

	markdown := RenderPRBody(summary)
	for _, expected := range []string{"## Agent Machine handoff", "### Issue scope", "Scope: Runner handoff code and tests.", "Scope guard: changed files matched the Linear ticket path contract.", "### Validation", "### Tests and characterization", "mise exec go -- go test ./...", "### Behavior Contract Evidence", "docs/specs/end-to-end-orchestration.md", "docs/specs/harness-behavior.md", "docs/agents/review-policy.md", "Behavior inventory", "Preserved behavior", "Handoff evidence source", "Complexity/LOC budget", "Run classification: outcome=handoff_ready", "### Changed files", "Files changed: 3", "### Risks and out of scope", "Out of scope: Merge policy changes.", "### Progress status", "handoff_pending", "### Remaining follow-up", "No follow-up recorded."} {
		if !strings.Contains(markdown, expected) {
			t.Fatalf("expected %q in markdown:\n%s", expected, markdown)
		}
	}
	if strings.Contains(markdown, "implementation-agent code-host ownership") {
		t.Fatalf("PR body should not include CAG-196-specific hard-coded out-of-scope text:\n%s", markdown)
	}
	if strings.Contains(markdown, "<!-- am-summary -->") {
		t.Fatalf("PR body should not contain the retired PR comment marker:\n%s", markdown)
	}
	if len(markdown) > 12000 {
		t.Fatalf("expected bounded markdown, got %d bytes", len(markdown))
	}
}

func TestRenderPRBodySanitizesAndTruncates(t *testing.T) {
	long := strings.Repeat("x", 5000)
	markdown := RenderPRBody(Summary{
		IssueIdentifier: "CAG-15",
		IssueTitle:      "Title with\nnewline and `ticks`",
		PRURL:           "https://github.com/weskor/agent-machine/pull/407",
		Validation:      []string{"bun run check\nsecond line", "raw `code` marker", long},
		FollowUps:       []string{long},
	})

	if len(markdown) > 12000 {
		t.Fatalf("expected truncated markdown, got %d bytes", len(markdown))
	}
	if strings.Contains(markdown, "Title with\nnewline") || strings.Contains(markdown, "`code`") {
		t.Fatalf("expected line sanitization, got:\n%s", markdown)
	}
	if !strings.Contains(markdown, "…") {
		t.Fatalf("expected truncation marker, got:\n%s", markdown)
	}
}

func TestRenderLinearCommentIsBounded(t *testing.T) {
	comment := RenderLinearComment(Summary{PRURL: "https://example.test/pr/1", RuntimeUsage: "usage", Review: "passed", Duration: time.Minute})

	for _, expected := range []string{"Runtime run completed.", "https://example.test/pr/1", "usage", "passed", "1m0s"} {
		if !strings.Contains(comment, expected) {
			t.Fatalf("expected %q in %q", expected, comment)
		}
	}
}
