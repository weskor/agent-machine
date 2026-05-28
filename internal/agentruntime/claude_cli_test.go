package agentruntime

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestClaudeCLIAdapterPreflightReportsProviderAndMissingCommand(t *testing.T) {
	runtime := ClaudeCLIAdapter{}
	result, err := runtime.Preflight(context.Background(), PreflightInput{ImplementationCommand: "definitely-missing-claude-test-binary"})
	if err == nil {
		t.Fatal("expected preflight error")
	}
	if result.Provider != "claude_cli" || len(result.Checks) != 1 {
		t.Fatalf("unexpected preflight result: %+v", result)
	}
	check := result.Checks[0]
	if check.OK || check.Name != "implementation_command" || check.Executable != "definitely-missing-claude-test-binary" || !strings.Contains(check.Message, "definitely-missing-claude-test-binary") {
		t.Fatalf("preflight result was not actionable: %+v", result)
	}
	if !strings.Contains(err.Error(), "claude_cli") || !strings.Contains(err.Error(), "definitely-missing-claude-test-binary") {
		t.Fatalf("error did not mention provider and command: %v", err)
	}
}

func TestClaudeCLIAdapterRunAttemptUsesStdinCommandShapeAndParsesOutput(t *testing.T) {
	var gotCommand, gotPhase string
	runtime := ClaudeCLIAdapter{
		RunCommand: func(ctx context.Context, command, workdir string, env map[string]string, timeout time.Duration, phase string) (string, error) {
			gotCommand = command
			gotPhase = phase
			if workdir != "/tmp/work" || env["TOKEN"] != "x" || timeout != time.Second {
				t.Fatalf("runtime inputs were not forwarded")
			}
			return "opened https://github.com/acme/repo/pull/7", nil
		},
		FirstPRURL: func(output string) string { return "https://github.com/acme/repo/pull/7" },
	}

	result, err := runtime.RunAttempt(context.Background(), "CAG-1", RunAttemptInput{Command: "claude --print --no-session-persistence", PromptPath: "/tmp/work/prompt.md", WorkingDir: "/tmp/work", Timeout: time.Second, Environment: map[string]string{"TOKEN": "x"}}, NoopSink{})
	if err != nil {
		t.Fatalf("RunAttempt returned error: %v", err)
	}
	if gotPhase != "implementation" || !strings.HasPrefix(gotCommand, "claude --print --no-session-persistence < ") || !strings.Contains(gotCommand, "/tmp/work/prompt.md") {
		t.Fatalf("unexpected command shape phase=%q command=%q", gotPhase, gotCommand)
	}
	if result.AttemptOutcome != AttemptOutcomeSuccess || result.PRURL == "" || result.Output == "" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.Envelope.RuntimeOutcome != AttemptOutcomeSuccess || result.Envelope.PRURL != result.PRURL || result.Envelope.RawOutput != result.Output {
		t.Fatalf("unexpected outcome envelope: %+v", result.Envelope)
	}
}

func TestClaudeCLIAdapterReviewAttemptWritesPromptAndClassifiesFindings(t *testing.T) {
	workspace := t.TempDir()
	runtime := ClaudeCLIAdapter{
		RunCommand: func(ctx context.Context, command, workdir string, env map[string]string, timeout time.Duration, phase string) (string, error) {
			if phase != "review" || !strings.Contains(command, ".am-review-prompt-") || !strings.Contains(command, " < ") {
				t.Fatalf("unexpected review command phase=%q command=%q", phase, command)
			}
			return `{"type":"result","result":"REVIEW_PASS\nLooks good."}`, nil
		},
		ReviewFindings: func(output string) string {
			return "REVIEW_PASS\nLooks good."
		},
		ReviewStatus: func(output string) string { return "passed" },
		ReviewClassification: func(status, output string) string {
			return ""
		},
	}

	result, err := runtime.ReviewAttempt(context.Background(), "CAG-1", ReviewAttemptInput{Command: "claude --print", WorkingDir: workspace, Prompt: "review this"}, NoopSink{})
	if err != nil {
		t.Fatalf("ReviewAttempt returned error: %v", err)
	}
	if result.Status != "passed" || !strings.Contains(result.Findings, "Looks good") {
		t.Fatalf("unexpected review result: %+v", result)
	}
}
