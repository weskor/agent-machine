package agentruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sh "github.com/weskor/pi-symphony/internal/shell"
)

func TestPiCLIAdapterPreflightReportsProviderAndMissingCommand(t *testing.T) {
	runtime := PiCLIAdapter{}
	result, err := runtime.Preflight(context.Background(), PreflightInput{ImplementationCommand: "definitely-missing-pi-symphony-test-binary"})
	if err == nil {
		t.Fatal("expected preflight error")
	}
	if result.Provider != "pi_cli" || len(result.Checks) != 1 {
		t.Fatalf("unexpected preflight result: %+v", result)
	}
	check := result.Checks[0]
	if check.OK || check.Name != "implementation_command" || check.Executable != "definitely-missing-pi-symphony-test-binary" || !strings.Contains(check.Message, "definitely-missing-pi-symphony-test-binary") {
		t.Fatalf("preflight result was not actionable: %+v", result)
	}
	if !strings.Contains(err.Error(), "pi_cli") || !strings.Contains(err.Error(), "definitely-missing-pi-symphony-test-binary") {
		t.Fatalf("error did not mention provider and command: %v", err)
	}
}

func TestPiCLIAdapterPreflightChecksReviewCommandWhenConfigured(t *testing.T) {
	dir := t.TempDir()
	impl := filepath.Join(dir, "pi-ok")
	if err := os.WriteFile(impl, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	runtime := PiCLIAdapter{}
	result, err := runtime.Preflight(context.Background(), PreflightInput{ImplementationCommand: impl, ReviewCommand: "missing-review-binary"})
	if err == nil {
		t.Fatal("expected review preflight error")
	}
	if len(result.Checks) != 2 || !result.Checks[0].OK || result.Checks[1].OK || result.Checks[1].Name != "review_command" || !strings.Contains(err.Error(), "missing-review-binary") {
		t.Fatalf("unexpected review preflight result=%+v err=%v", result, err)
	}
}

func TestPiCLIAdapterPreflightRejectsUnsupportedMaxTurns(t *testing.T) {
	dir := t.TempDir()
	impl := filepath.Join(dir, "pi-ok")
	if err := os.WriteFile(impl, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	runtime := PiCLIAdapter{}
	result, err := runtime.Preflight(context.Background(), PreflightInput{ImplementationCommand: impl, MaxTurns: 2})
	if err == nil {
		t.Fatal("expected max_turns preflight error")
	}
	if len(result.Checks) == 0 || result.Checks[0].Name != "max_turns" || result.Checks[0].OK {
		t.Fatalf("max_turns check was not first actionable failure: %+v", result)
	}
	if !strings.Contains(err.Error(), "agent.max_turns=2") || !strings.Contains(err.Error(), "session runtime") || !strings.Contains(err.Error(), "agent.max_turns: 1") {
		t.Fatalf("error was not actionable: %v", err)
	}
}

func TestPiCLIAdapterPreflightAllowsDefaultAndOneMaxTurns(t *testing.T) {
	dir := t.TempDir()
	impl := filepath.Join(dir, "pi-ok")
	if err := os.WriteFile(impl, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	runtime := PiCLIAdapter{}
	for _, maxTurns := range []int{0, 1} {
		if _, err := runtime.Preflight(context.Background(), PreflightInput{ImplementationCommand: impl, MaxTurns: maxTurns}); err != nil {
			t.Fatalf("max_turns=%d should preserve single-attempt behavior: %v", maxTurns, err)
		}
	}
}

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
