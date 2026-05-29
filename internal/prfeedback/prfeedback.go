package prfeedback

import (
	"fmt"
	"strings"

	"github.com/weskor/agent-machine/internal/codehost"
)

func Render(prNumber int, feedback codehost.PRFeedback) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "# PR #%d review feedback\n\n", prNumber)
	for _, review := range feedback.Reviews {
		body := strings.TrimSpace(review.Body)
		if body == "" && review.State != "CHANGES_REQUESTED" {
			continue
		}
		fmt.Fprintf(&builder, "## Review: %s by %s\n\n", review.State, review.Author.Login)
		if body == "" {
			body = "No review body provided. Check GitHub for inline review comments if needed."
		}
		fmt.Fprintf(&builder, "%s\n\n", body)
	}
	for _, comment := range feedback.Comments {
		body := strings.TrimSpace(comment.Body)
		if body == "" {
			continue
		}
		fmt.Fprintf(&builder, "## Comment by %s\n\n%s\n\n", comment.Author.Login, body)
	}
	for _, comment := range feedback.ReviewComments {
		body := strings.TrimSpace(comment.Body)
		if body == "" {
			continue
		}
		location := comment.Path
		if comment.Line > 0 {
			location = fmt.Sprintf("%s:%d", comment.Path, comment.Line)
		}
		fmt.Fprintf(&builder, "## Inline review comment by %s on %s\n\n%s\n\n", comment.User.Login, location, body)
	}
	if strings.TrimSpace(builder.String()) == fmt.Sprintf("# PR #%d review feedback", prNumber) {
		fmt.Fprintln(&builder, "No review feedback returned by GitHub.")
	}
	return builder.String()
}
