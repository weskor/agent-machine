package main

import (
	"strings"
	"testing"
)

func TestRenderPRFeedbackIncludesChangeRequestsAndComments(t *testing.T) {
	var feedback prFeedback
	feedback.Reviews = append(feedback.Reviews, struct {
		Author struct {
			Login string `json:"login"`
		} `json:"author"`
		State       string `json:"state"`
		Body        string `json:"body"`
		SubmittedAt string `json:"submittedAt"`
	}{State: "CHANGES_REQUESTED", Body: "Please add tests."})
	feedback.Reviews[0].Author.Login = "reviewer"
	feedback.Comments = append(feedback.Comments, struct {
		Author struct {
			Login string `json:"login"`
		} `json:"author"`
		Body      string `json:"body"`
		CreatedAt string `json:"createdAt"`
	}{Body: "Also update docs."})
	feedback.Comments[0].Author.Login = "operator"
	feedback.ReviewComments = append(feedback.ReviewComments, struct {
		User struct {
			Login string `json:"login"`
		} `json:"user"`
		Path      string `json:"path"`
		Line      int    `json:"line"`
		Body      string `json:"body"`
		CreatedAt string `json:"created_at"`
	}{Path: ".pi/skills/tdd/SKILL.md", Line: 3, Body: "Make this guidance general to all agents."})
	feedback.ReviewComments[0].User.Login = "reviewer"

	markdown := renderPRFeedback(123, feedback)
	for _, expected := range []string{"PR #123", "CHANGES_REQUESTED", "reviewer", "Please add tests.", "operator", "Also update docs.", "Inline review comment", ".pi/skills/tdd/SKILL.md:3", "Make this guidance general"} {
		if !strings.Contains(markdown, expected) {
			t.Fatalf("expected %q in feedback markdown:\n%s", expected, markdown)
		}
	}
}

func TestFeedbackHashChangesWhenInlineReviewCommentsAppear(t *testing.T) {
	var feedback prFeedback
	feedback.Reviews = append(feedback.Reviews, struct {
		Author struct {
			Login string `json:"login"`
		} `json:"author"`
		State       string `json:"state"`
		Body        string `json:"body"`
		SubmittedAt string `json:"submittedAt"`
	}{State: "CHANGES_REQUESTED", Body: "Test should be unit test."})
	feedback.Reviews[0].Author.Login = "reviewer"

	withoutInline := renderPRFeedback(429, feedback)
	feedback.ReviewComments = append(feedback.ReviewComments, struct {
		User struct {
			Login string `json:"login"`
		} `json:"user"`
		Path      string `json:"path"`
		Line      int    `json:"line"`
		Body      string `json:"body"`
		CreatedAt string `json:"created_at"`
	}{Path: ".pi/skills/tdd/SKILL.md", Line: 3, Body: "It's not only for symphony, should also be for other agents in general in this repo"})
	feedback.ReviewComments[0].User.Login = "reviewer"
	withInline := renderPRFeedback(429, feedback)

	if feedbackHash(withoutInline) == feedbackHash(withInline) {
		t.Fatal("inline review comments must affect feedback hash so they trigger a retry")
	}
}

func TestRenderPRFeedbackKeepsEmptyChangeRequestVisible(t *testing.T) {
	var feedback prFeedback
	feedback.Reviews = append(feedback.Reviews, struct {
		Author struct {
			Login string `json:"login"`
		} `json:"author"`
		State       string `json:"state"`
		Body        string `json:"body"`
		SubmittedAt string `json:"submittedAt"`
	}{State: "CHANGES_REQUESTED"})

	markdown := renderPRFeedback(124, feedback)
	if !strings.Contains(markdown, "No review body provided") {
		t.Fatalf("expected empty change request guidance, got:\n%s", markdown)
	}
}

func TestFeedbackHashStableForWhitespace(t *testing.T) {
	first := feedbackHash("\n# PR feedback\n\nPlease add tests.\n")
	second := feedbackHash("# PR feedback\n\nPlease add tests.")
	if first == "" || first != second {
		t.Fatalf("feedback hash should be stable for surrounding whitespace: %q %q", first, second)
	}
}
