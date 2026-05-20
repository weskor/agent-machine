package agentruntime

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestUnsupportedOperation(t *testing.T) {
	err := UnsupportedOperation{Operation: "review"}
	if err.Error() == "" {
		t.Fatal("expected non-empty error")
	}
}

func TestIsUnsupportedOperation(t *testing.T) {
	if !IsUnsupportedOperation(UnsupportedOperation{Operation: "cancel"}) {
		t.Fatal("unsupported op not detected")
	}
	if IsUnsupportedOperation(errors.New("other")) {
		t.Fatal("unexpected unsupported op detection")
	}
}

type stubRuntime struct{}

func (stubRuntime) Capabilities() RuntimeCapabilities {
	return RuntimeCapabilities{SupportsReview: true, SupportsCancel: false, SupportsStop: false}
}

func (stubRuntime) Preflight(context.Context, PreflightInput) (PreflightResult, error) {
	return PreflightResult{Provider: "stub"}, nil
}

func (stubRuntime) StartAttempt(context.Context, StartAttemptInput) (AttemptContext, error) {
	return AttemptContext{ID: "attempt-id"}, nil
}

func (stubRuntime) RunAttempt(context.Context, string, RunAttemptInput, EventSink) (AttemptResult, error) {
	return AttemptResult{AttemptID: "attempt-id", AttemptOutcome: AttemptOutcomeSuccess, StartedAt: time.Now(), EndedAt: time.Now().Add(time.Second)}, nil
}

func (stubRuntime) ReviewAttempt(context.Context, string, ReviewAttemptInput, EventSink) (ReviewResult, error) {
	return ReviewResult{Status: "passed", Classification: "ready", Findings: "PASS"}, nil
}

func (stubRuntime) Stop(context.Context, string, string) error {
	return UnsupportedOperation{Operation: "stop"}
}

func (stubRuntime) Cancel(context.Context, string, string) error {
	return UnsupportedOperation{Operation: "cancel"}
}

func TestStubRuntimeReturnsExplicitUnsupportedForStopCancel(t *testing.T) {
	runtime := stubRuntime{}
	if !IsUnsupportedOperation(runtime.Stop(context.Background(), "attempt-id", "reason")) {
		t.Fatal("stop should return explicit unsupported error")
	}
	if !IsUnsupportedOperation(runtime.Cancel(context.Background(), "attempt-id", "reason")) {
		t.Fatal("cancel should return explicit unsupported error")
	}
}

func TestIsTerminalOutcome(t *testing.T) {
	tests := []struct {
		name     string
		outcome  AttemptOutcome
		terminal bool
	}{
		{name: "success", outcome: AttemptOutcomeSuccess, terminal: true},
		{name: "failed", outcome: AttemptOutcomeFailed, terminal: true},
		{name: "needs info", outcome: AttemptOutcomeNeedsInfo, terminal: true},
		{name: "timeout", outcome: AttemptOutcomeTimeout, terminal: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsTerminalOutcome(tt.outcome); got != tt.terminal {
				t.Fatalf("IsTerminalOutcome(%q) = %v, want %v", tt.outcome, got, tt.terminal)
			}
		})
	}
}
