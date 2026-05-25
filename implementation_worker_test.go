package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
		"Code-host PR/MR feedback to address before handoff:\nReviewer asked for focused tests.",
		"output NEEDS_INFO followed by numbered questions instead of guessing",
		"Do not create, update, push, or comment on a code-host PR/MR",
		"branch am/CAG-154-workspace into base branch main",
		"The runner will move the Linear issue to Human Review after runner PR/MR handoff, or to Needs Info when NEEDS_INFO is detected.",
		"Behavior-contract preflight for refactors, replacements, and rewrites:",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("implementation prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestImplementationWorkerPrepareHonorsCanceledContextBeforeWorkspaceSetup(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-190")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := implementationWorker{
		config: runnerConfig{
			WorkspaceRoot: root,
			AfterCreate:   "sleep 1",
			BeforeRun:     "sleep 1",
			Budget:        runBudget{CommandTimeout: time.Second},
		},
		candidate:       &issue{Identifier: "CAG-190"},
		workspace:       workspace,
		branch:          expectedWorkspaceBranch("CAG-190"),
		progressStarted: time.Now(),
		runStarted:      time.Now(),
	}.Prepare(ctx)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Prepare() error = %v; want context canceled", err)
	}
	if _, statErr := os.Stat(workspace); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("workspace stat error = %v; want workspace not created", statErr)
	}
}
