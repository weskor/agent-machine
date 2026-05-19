package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func collectPRFeedback(prNumber int) (string, error) {
	github, ctx, cancel, err := githubClientWithTimeout(defaultGitHubCommandTimeout)
	if err != nil {
		return "", err
	}
	defer cancel()
	feedback, err := github.PullRequestFeedback(ctx, prNumber)
	if err != nil {
		return "", fmt.Errorf("GitHub API PR feedback lookup failed for #%d: %w", prNumber, err)
	}
	return renderPRFeedback(prNumber, feedback), nil
}

func feedbackHash(feedback string) string {
	normalized := strings.TrimSpace(feedback)
	if normalized == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}

func writePRFeedback(workspace string, prNumber int, feedback string) error {
	if strings.TrimSpace(feedback) == "" {
		feedback = fmt.Sprintf("# PR #%d feedback\n\nNo review feedback returned by GitHub.\n", prNumber)
	}
	path := filepath.Join(workspace, ".pi-symphony-feedback.md")
	if err := os.WriteFile(path, []byte(feedback), 0o600); err != nil {
		return err
	}
	log("wrote PR feedback: %s", path)
	return nil
}

func readPRFeedback(workspace string) (string, error) {
	data, err := os.ReadFile(filepath.Join(workspace, ".pi-symphony-feedback.md"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}

func renderPRFeedback(prNumber int, feedback prFeedback) string {
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
