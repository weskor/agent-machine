package agentruntime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeSessionClient struct {
	startInput SessionStartInput
	turnInput  SessionTurnInput
	turnResult SessionTurnResult
}

func (f *fakeSessionClient) StartSession(_ context.Context, input SessionStartInput) (SessionContext, error) {
	f.startInput = input
	return SessionContext{AttemptID: input.AttemptContext.ID, SessionID: "thread-1", Workspace: input.WorkingDir, MaxTurns: input.AttemptContext.MaxTurns}, nil
}

func (f *fakeSessionClient) RunTurn(_ context.Context, session SessionContext, input SessionTurnInput) (SessionTurnResult, error) {
	f.turnInput = input
	result := f.turnResult
	if result.SessionID == "" {
		result.SessionID = session.SessionID
	}
	if result.TurnNumber == 0 {
		result.TurnNumber = input.TurnNumber
	}
	return result, nil
}

func TestCodexAppServerAdapterCapabilities(t *testing.T) {
	runtime := CodexAppServerAdapter{}
	caps := runtime.Capabilities()
	if !caps.SupportsSessions || !caps.SupportsTurnContinuation || !caps.SupportsMaxTurns || !caps.CanRunMultipleTurns() {
		t.Fatalf("unexpected capabilities: %+v", caps)
	}
}

func TestCodexAppServerAdapterPreflightFailsClosedWithoutClient(t *testing.T) {
	dir := t.TempDir()
	codex := filepath.Join(dir, "codex")
	if err := os.WriteFile(codex, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	runtime := CodexAppServerAdapter{}
	result, err := runtime.Preflight(context.Background(), PreflightInput{ImplementationCommand: codex + " app-server --listen stdio://", MaxTurns: 3})
	if err == nil {
		t.Fatal("expected app-server preflight to fail closed without client wiring")
	}
	if result.Provider != CodexAppServerProvider || len(result.Checks) != 2 || !result.Checks[0].OK || result.Checks[1].OK {
		t.Fatalf("unexpected preflight result: %+v", result)
	}
	if !strings.Contains(err.Error(), "codex_app_server") || !strings.Contains(err.Error(), "not enabled yet") {
		t.Fatalf("preflight error was not actionable: %v", err)
	}
}

func TestCodexAppServerAdapterPreflightAllowsMaxTurnsWithClient(t *testing.T) {
	dir := t.TempDir()
	codex := filepath.Join(dir, "codex")
	if err := os.WriteFile(codex, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	runtime := CodexAppServerAdapter{Client: &fakeSessionClient{}}
	result, err := runtime.Preflight(context.Background(), PreflightInput{ImplementationCommand: codex + " app-server --listen stdio://", MaxTurns: 3})
	if err != nil {
		t.Fatalf("Preflight() error = %v result=%+v", err, result)
	}
	if !result.OK() || result.Provider != CodexAppServerProvider {
		t.Fatalf("unexpected preflight result: %+v", result)
	}
}

func TestCodexAppServerAdapterRunAttemptStartsSessionAndTurn(t *testing.T) {
	workspace := t.TempDir()
	promptPath := filepath.Join(workspace, "prompt.md")
	if err := os.WriteFile(promptPath, []byte("implement this"), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &fakeSessionClient{turnResult: SessionTurnResult{Output: "done", Usage: &AttemptUsage{TotalTokens: 12}, TerminalOutcome: AttemptOutcomeSuccess}}
	runtime := CodexAppServerAdapter{Client: client}

	result, err := runtime.RunAttempt(context.Background(), "CAG-1", RunAttemptInput{PromptPath: promptPath, WorkingDir: workspace, Timeout: time.Second, MaxTurns: 3}, NoopSink{})
	if err != nil {
		t.Fatalf("RunAttempt() error = %v", err)
	}
	if result.AttemptOutcome != AttemptOutcomeSuccess || result.Output != "done" || result.Usage.TotalTokens != 12 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if client.startInput.WorkingDir != workspace || client.startInput.AttemptContext.MaxTurns != 3 {
		t.Fatalf("unexpected session start input: %+v", client.startInput)
	}
	if client.turnInput.SessionID != "thread-1" || client.turnInput.Prompt != "implement this" || client.turnInput.TurnNumber != 1 {
		t.Fatalf("unexpected turn input: %+v", client.turnInput)
	}
}

func TestCodexAppServerAdapterRunAttemptFailsOnContinuationUntilRunnerLoopExists(t *testing.T) {
	workspace := t.TempDir()
	promptPath := filepath.Join(workspace, "prompt.md")
	if err := os.WriteFile(promptPath, []byte("implement this"), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &fakeSessionClient{turnResult: SessionTurnResult{Output: "need another turn", NeedsContinuation: true}}
	runtime := CodexAppServerAdapter{Client: client}

	result, err := runtime.RunAttempt(context.Background(), "CAG-1", RunAttemptInput{PromptPath: promptPath, WorkingDir: workspace, Timeout: time.Second, MaxTurns: 3}, NoopSink{})
	if err == nil {
		t.Fatal("expected continuation to fail until runner turn loop is wired")
	}
	if result.AttemptOutcome != AttemptOutcomeContinuation || result.ErrorKind != RuntimeErrorKindUnsupported {
		t.Fatalf("unexpected continuation result: %+v", result)
	}
}
