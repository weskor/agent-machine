package agentruntime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	sh "github.com/weskor/pi-symphony/internal/shell"
)

func TestCodexCLIAdapterPreflightReportsProviderAndMissingCommand(t *testing.T) {
	runtime := CodexCLIAdapter{}
	result, err := runtime.Preflight(context.Background(), PreflightInput{ImplementationCommand: "definitely-missing-codex-test-binary"})
	if err == nil {
		t.Fatal("expected preflight error")
	}
	if result.Provider != "codex_cli" || len(result.Checks) != 1 {
		t.Fatalf("unexpected preflight result: %+v", result)
	}
	check := result.Checks[0]
	if check.OK || check.Name != "implementation_command" || check.Executable != "definitely-missing-codex-test-binary" || !strings.Contains(check.Message, "definitely-missing-codex-test-binary") {
		t.Fatalf("preflight result was not actionable: %+v", result)
	}
	if !strings.Contains(err.Error(), "codex_cli") || !strings.Contains(err.Error(), "definitely-missing-codex-test-binary") {
		t.Fatalf("error did not mention provider and command: %v", err)
	}
}

func TestCodexCLIAdapterRunAttemptUsesStdinCommandShapeAndParsesOutput(t *testing.T) {
	var gotCommand, gotPhase string
	runtime := CodexCLIAdapter{
		RunCommand: func(command, workdir string, env map[string]string, timeout time.Duration, phase string) (string, error) {
			gotCommand = command
			gotPhase = phase
			if workdir != "/tmp/work" || env["TOKEN"] != "x" || timeout != time.Second {
				t.Fatalf("runtime inputs were not forwarded")
			}
			return "opened https://github.com/acme/repo/pull/7\ntokens used\n1,234", nil
		},
		FirstPRURL: func(output string) string { return "https://github.com/acme/repo/pull/7" },
		ParseUsage: func(output string) *AttemptUsage { return &AttemptUsage{TotalTokens: 1234} },
	}

	result, err := runtime.RunAttempt(context.Background(), "CAG-1", RunAttemptInput{Command: "codex exec --ephemeral", PromptPath: "/tmp/work/prompt.md", WorkingDir: "/tmp/work", Timeout: time.Second, Environment: map[string]string{"TOKEN": "x"}}, NoopSink{})
	if err != nil {
		t.Fatalf("RunAttempt returned error: %v", err)
	}
	if gotPhase != "implementation" || !strings.HasPrefix(gotCommand, "codex exec --ephemeral - < ") || !strings.Contains(gotCommand, "/tmp/work/prompt.md") {
		t.Fatalf("unexpected command shape phase=%q command=%q", gotPhase, gotCommand)
	}
	if result.AttemptOutcome != AttemptOutcomeSuccess || result.PRURL == "" || result.Output == "" || result.Usage.TotalTokens != 1234 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.Envelope.RuntimeOutcome != AttemptOutcomeSuccess || result.Envelope.PRURL != result.PRURL || result.Envelope.RawOutput != result.Output || result.Envelope.Usage.TotalTokens != 1234 {
		t.Fatalf("unexpected outcome envelope: %+v", result.Envelope)
	}
}

func TestCodexCLIAdapterRunAttemptMapsTimeout(t *testing.T) {
	runtime := CodexCLIAdapter{RunCommand: func(string, string, map[string]string, time.Duration, string) (string, error) {
		return "partial", sh.ErrCommandTimeout
	}}

	result, err := runtime.RunAttempt(context.Background(), "CAG-1", RunAttemptInput{Command: "codex exec", PromptPath: "prompt.md"}, NoopSink{})
	if !errors.Is(err, sh.ErrCommandTimeout) {
		t.Fatalf("expected timeout error, got %v", err)
	}
	if result.AttemptOutcome != AttemptOutcomeTimeout || result.ErrorKind != RuntimeErrorKindTimeout || result.Output != "partial" {
		t.Fatalf("unexpected timeout result: %+v", result)
	}
	if result.Envelope.RuntimeOutcome != AttemptOutcomeTimeout || result.Envelope.ErrorKind != RuntimeErrorKindTimeout || result.Envelope.RawOutput != "partial" {
		t.Fatalf("unexpected timeout envelope: %+v", result.Envelope)
	}
}

func TestCodexCLIAdapterReviewAttemptWritesPromptAndClassifiesFindings(t *testing.T) {
	workspace := t.TempDir()
	runtime := CodexCLIAdapter{
		RunCommand: func(command, workdir string, env map[string]string, timeout time.Duration, phase string) (string, error) {
			if phase != "review" || !strings.Contains(command, ".pi-symphony-review-prompt.md") || !strings.Contains(command, " - < ") {
				t.Fatalf("unexpected review command phase=%q command=%q", phase, command)
			}
			return "REVIEW_FAIL\nREVIEW_CLASSIFICATION: missing_evidence_only\nAdd evidence.", nil
		},
		ReviewStatus: func(output string) string { return "failed" },
		ReviewClassification: func(status, output string) string {
			return "missing_evidence_only"
		},
	}

	result, err := runtime.ReviewAttempt(context.Background(), "CAG-1", ReviewAttemptInput{Command: "codex exec", WorkingDir: workspace, Prompt: "review this"}, NoopSink{})
	if err != nil {
		t.Fatalf("ReviewAttempt returned error: %v", err)
	}
	if result.Status != "failed" || result.Classification != "missing_evidence_only" || !strings.Contains(result.Findings, "Add evidence") {
		t.Fatalf("unexpected review result: %+v", result)
	}
}
