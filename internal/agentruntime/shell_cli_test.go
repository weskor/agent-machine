package agentruntime

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestShellCLIAdapterRunAttemptUsesConfiguredCommandBuilder(t *testing.T) {
	var gotCommand string
	runtime := ShellCLIAdapter{
		Provider:       "test_cli",
		MissingCommand: "missing test command",
		RunCommand: func(command, workdir string, env map[string]string, timeout time.Duration, phase string) (string, error) {
			gotCommand = command
			if phase != "implementation" || workdir != "/tmp/work" || env["TOKEN"] != "x" || timeout != time.Second {
				t.Fatalf("runtime inputs were not forwarded")
			}
			return "ok", nil
		},
		BuildCommand: func(command, promptPath string) string {
			return strings.TrimSpace(command) + " --prompt " + promptPath
		},
	}

	result, err := runtime.RunAttempt(context.Background(), "CAG-1", RunAttemptInput{Command: "test-agent", PromptPath: "/tmp/work/prompt.md", WorkingDir: "/tmp/work", Timeout: time.Second, Environment: map[string]string{"TOKEN": "x"}}, NoopSink{})
	if err != nil {
		t.Fatalf("RunAttempt returned error: %v", err)
	}
	if gotCommand != "test-agent --prompt /tmp/work/prompt.md" {
		t.Fatalf("command = %q", gotCommand)
	}
	if result.AttemptOutcome != AttemptOutcomeSuccess || result.Output != "ok" || result.Envelope.RawOutput != "ok" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestShellCLIAdapterRunAttemptPrefersStructuredOutcomeEnvelope(t *testing.T) {
	output := `legacy text without parseable questions
AM_OUTCOME: {"runtime_outcome":"needs_info","needs_info_questions":["Which tenant?"],"validation":["typed validation"],"pr_url":"https://github.com/acme/repo/pull/9"}`
	runtime := ShellCLIAdapter{
		Provider:       "test_cli",
		MissingCommand: "missing test command",
		RunCommand: func(string, string, map[string]string, time.Duration, string) (string, error) {
			return output, nil
		},
		FirstPRURL: func(string) string { return "https://github.com/acme/repo/pull/legacy" },
		NeedsInfoQuestions: func(string) []string {
			return []string{"legacy question"}
		},
	}

	result, err := runtime.RunAttempt(context.Background(), "CAG-1", RunAttemptInput{Command: "test-agent", PromptPath: "/tmp/work/prompt.md"}, NoopSink{})
	if err != nil {
		t.Fatalf("RunAttempt returned error: %v", err)
	}
	if result.AttemptOutcome != AttemptOutcomeNeedsInfo {
		t.Fatalf("attempt outcome = %q, want needs_info", result.AttemptOutcome)
	}
	if result.Envelope.PRURL != "https://github.com/acme/repo/pull/9" || strings.Join(result.Envelope.NeedsInfoQuestions, "\n") != "Which tenant?" || strings.Join(result.Envelope.Validation, "\n") != "typed validation" {
		t.Fatalf("envelope = %+v; want structured fields to win", result.Envelope)
	}
	if result.Envelope.RawOutput != output {
		t.Fatalf("raw output = %q, want original output", result.Envelope.RawOutput)
	}
}

func TestShellCLIAdapterRunAttemptRejectsMalformedStructuredOutcomeEnvelope(t *testing.T) {
	runtime := ShellCLIAdapter{
		Provider:       "test_cli",
		MissingCommand: "missing test command",
		RunCommand: func(string, string, map[string]string, time.Duration, string) (string, error) {
			return `AM_OUTCOME: {"runtime_outcome":`, nil
		},
	}

	result, err := runtime.RunAttempt(context.Background(), "CAG-1", RunAttemptInput{Command: "test-agent", PromptPath: "/tmp/work/prompt.md"}, NoopSink{})
	if err == nil {
		t.Fatal("RunAttempt error = nil; want malformed envelope error")
	}
	if result.AttemptOutcome != AttemptOutcomeFailed || result.Envelope.RuntimeOutcome != AttemptOutcomeFailed || result.ErrorKind != RuntimeErrorKindExecution {
		t.Fatalf("result = %+v; want failed structured envelope result", result)
	}
}

func TestShellCLIAdapterReviewAttemptCanTransformFindings(t *testing.T) {
	workspace := t.TempDir()
	runtime := ShellCLIAdapter{
		Provider:       "test_cli",
		MissingCommand: "missing test command",
		RunCommand: func(command, workdir string, env map[string]string, timeout time.Duration, phase string) (string, error) {
			if phase != "review" || !strings.Contains(command, ".am-review-prompt.md") {
				t.Fatalf("unexpected review command phase=%q command=%q", phase, command)
			}
			return "json wrapper\nREVIEW_PASS", nil
		},
		ReviewFindings: func(output string) string {
			return "REVIEW_PASS"
		},
		ReviewStatus: func(output string) string {
			if output == "REVIEW_PASS" {
				return "passed"
			}
			return "unknown"
		},
	}

	result, err := runtime.ReviewAttempt(context.Background(), "CAG-1", ReviewAttemptInput{Command: "test-agent", WorkingDir: workspace, Prompt: "review this"}, NoopSink{})
	if err != nil {
		t.Fatalf("ReviewAttempt returned error: %v", err)
	}
	if result.Status != "passed" || result.Findings != "REVIEW_PASS" {
		t.Fatalf("unexpected review result: %+v", result)
	}
}
