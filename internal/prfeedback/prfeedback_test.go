package prfeedback

import (
	"strings"
	"testing"

	"github.com/weskor/agent-machine/internal/artifacts"
	"github.com/weskor/agent-machine/internal/codehost"
)

func TestRenderIncludesChangeRequestsAndComments(t *testing.T) {
	var feedback codehost.PRFeedback
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

	markdown := Render(123, feedback)
	for _, expected := range []string{"PR #123", "CHANGES_REQUESTED", "reviewer", "Please add tests.", "operator", "Also update docs.", "Inline review comment", ".pi/skills/tdd/SKILL.md:3", "Make this guidance general"} {
		if !strings.Contains(markdown, expected) {
			t.Fatalf("expected %q in feedback markdown:\n%s", expected, markdown)
		}
	}
}

func TestRenderHashChangesWhenInlineReviewCommentsAppear(t *testing.T) {
	var feedback codehost.PRFeedback
	feedback.Reviews = append(feedback.Reviews, struct {
		Author struct {
			Login string `json:"login"`
		} `json:"author"`
		State       string `json:"state"`
		Body        string `json:"body"`
		SubmittedAt string `json:"submittedAt"`
	}{State: "CHANGES_REQUESTED", Body: "Test should be unit test."})
	feedback.Reviews[0].Author.Login = "reviewer"

	withoutInline := Render(429, feedback)
	feedback.ReviewComments = append(feedback.ReviewComments, struct {
		User struct {
			Login string `json:"login"`
		} `json:"user"`
		Path      string `json:"path"`
		Line      int    `json:"line"`
		Body      string `json:"body"`
		CreatedAt string `json:"created_at"`
	}{Path: ".pi/skills/tdd/SKILL.md", Line: 3, Body: "It's not only for am, should also be for other agents in general in this repo"})
	feedback.ReviewComments[0].User.Login = "reviewer"
	withInline := Render(429, feedback)

	if artifacts.FeedbackHash(withoutInline) == artifacts.FeedbackHash(withInline) {
		t.Fatal("inline review comments must affect feedback hash so they trigger a retry")
	}
}

func TestRenderKeepsEmptyChangeRequestVisible(t *testing.T) {
	var feedback codehost.PRFeedback
	feedback.Reviews = append(feedback.Reviews, struct {
		Author struct {
			Login string `json:"login"`
		} `json:"author"`
		State       string `json:"state"`
		Body        string `json:"body"`
		SubmittedAt string `json:"submittedAt"`
	}{State: "CHANGES_REQUESTED"})

	markdown := Render(124, feedback)
	if !strings.Contains(markdown, "No review body provided") {
		t.Fatalf("expected empty change request guidance, got:\n%s", markdown)
	}
}
