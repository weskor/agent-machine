package main

import (
	"strings"
	"testing"
)

func TestImplementationPromptKeepsRunnerHandoffBoundary(t *testing.T) {
	candidate := &issue{
		Identifier:  "CAG-154",
		Title:       "Split workers",
		Description: "## Goal\n\nRefactor run_one boundaries.",
	}
	config := runnerConfig{BaseBranch: "main", HandoffState: "Human Review", NeedsInfoState: "Needs Info"}

	prompt := implementationPrompt("Work on {{issue.identifier}} attempt {{attempt}}.", candidate, "Reviewer asked for focused tests.", config)

	for _, want := range []string{
		"Work on CAG-154 attempt 1.",
		"Linear issue description:\n## Goal\n\nRefactor run_one boundaries.",
		"GitHub PR feedback to address before handoff:\nReviewer asked for focused tests.",
		"output NEEDS_INFO followed by numbered questions instead of guessing",
		"Do not create, update, push, or comment on a GitHub PR",
		"branch symphony/CAG-154-workspace into base branch main",
		"The runner will move the Linear issue to Human Review after runner PR handoff, or to Needs Info when NEEDS_INFO is detected.",
		"Behavior-contract preflight for refactors, replacements, and rewrites:",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("implementation prompt missing %q:\n%s", want, prompt)
		}
	}
}
