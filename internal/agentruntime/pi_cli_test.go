package agentruntime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	sh "github.com/weskor/pi-symphony/internal/shell"
)

func TestPiCLIAdapterRunAttemptPreservesCommandShapeAndParsesOutput(t *testing.T) {
	var gotCommand, gotPhase string
	runtime := PiCLIAdapter{
		RunCommand: func(command, workdir string, env map[string]string, timeout time.Duration, phase string) (string, error) {
			gotCommand = command
			gotPhase = phase
			if workdir != "/tmp/work" || env["TOKEN"] != "x" || timeout != time.Second {
				t.Fatalf("runtime inputs were not forwarded")
			}
			return "opened https://github.com/acme/repo/pull/7", nil
		},
		FirstPRURL: func(output string) string { return "https://github.com/acme/repo/pull/7" },
		ParseUsage: func(output string) *AttemptUsage { return &AttemptUsage{TotalTokens: 42, CostTotal: 0.5} },
	}

	result, err := runtime.RunAttempt(context.Background(), "CAG-1", RunAttemptInput{Command: "pi --model test", PromptPath: "/tmp/work/prompt.md", WorkingDir: "/tmp/work", Timeout: time.Second, Environment: map[string]string{"TOKEN": "x"}}, NoopSink{})
	if err != nil {
		t.Fatalf("RunAttempt returned error: %v", err)
	}
	if gotPhase != "implementation" || !strings.HasPrefix(gotCommand, "pi --model test @") || !strings.Contains(gotCommand, "/tmp/work/prompt.md") {
		t.Fatalf("unexpected command shape phase=%q command=%q", gotPhase, gotCommand)
	}
	if result.AttemptOutcome != AttemptOutcomeSuccess || result.PRURL == "" || result.Output == "" || result.Usage.TotalTokens != 42 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestPiCLIAdapterRunAttemptMapsTimeout(t *testing.T) {
	runtime := PiCLIAdapter{RunCommand: func(string, string, map[string]string, time.Duration, string) (string, error) {
		return "partial", sh.ErrCommandTimeout
	}}

	result, err := runtime.RunAttempt(context.Background(), "CAG-1", RunAttemptInput{Command: "pi", PromptPath: "prompt.md"}, NoopSink{})
	if !errors.Is(err, sh.ErrCommandTimeout) {
		t.Fatalf("expected timeout error, got %v", err)
	}
	if result.AttemptOutcome != AttemptOutcomeTimeout || result.ErrorKind != RuntimeErrorKindTimeout || result.Output != "partial" {
		t.Fatalf("unexpected timeout result: %+v", result)
	}
}

func TestPiCLIAdapterReviewAttemptWritesPromptAndClassifiesFindings(t *testing.T) {
	workspace := t.TempDir()
	runtime := PiCLIAdapter{
		RunCommand: func(command, workdir string, env map[string]string, timeout time.Duration, phase string) (string, error) {
			if phase != "review" || !strings.Contains(command, ".pi-symphony-review-prompt.md") {
				t.Fatalf("unexpected review command phase=%q command=%q", phase, command)
			}
			return "REVIEW_FAIL\nREVIEW_CLASSIFICATION: missing_evidence_only\nAdd evidence.", nil
		},
		ReviewStatus: func(output string) string { return "failed" },
		ReviewClassification: func(status, output string) string {
			return "missing_evidence_only"
		},
	}

	result, err := runtime.ReviewAttempt(context.Background(), "CAG-1", ReviewAttemptInput{Command: "pi review", WorkingDir: workspace, Prompt: "review this"}, NoopSink{})
	if err != nil {
		t.Fatalf("ReviewAttempt returned error: %v", err)
	}
	if result.Status != "failed" || result.Classification != "missing_evidence_only" || !strings.Contains(result.Findings, "Add evidence") {
		t.Fatalf("unexpected review result: %+v", result)
	}
}
