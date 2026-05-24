package agentruntime

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

const DefaultCodexAppServerCommand = "codex app-server --listen stdio://"

type CodexAppServerAdapter struct {
	Client SessionClient
	Now    func() time.Time
}

func (a CodexAppServerAdapter) Capabilities() RuntimeCapabilities {
	return RuntimeCapabilities{
		SupportsSessions:         true,
		SupportsTurnContinuation: true,
		SupportsMaxTurns:         true,
	}
}

func (a CodexAppServerAdapter) Preflight(_ context.Context, input PreflightInput) (PreflightResult, error) {
	result := PreflightResult{Provider: CodexAppServerProvider}
	command := strings.TrimSpace(input.ImplementationCommand)
	if command == "" {
		command = DefaultCodexAppServerCommand
	}
	result.Checks = append(result.Checks, preflightCommandForProvider(CodexAppServerProvider, "app_server_command", command))
	if a.Client == nil {
		result.Checks = append(result.Checks, PreflightCheck{
			Name:    "app_server_client",
			OK:      false,
			Message: "provider codex_app_server is recognized but production app-server client wiring is not enabled yet",
		})
	}
	if result.OK() {
		return result, nil
	}
	for _, check := range result.Checks {
		if !check.OK {
			return result, RuntimeError{Kind: RuntimeErrorKindConfiguration, Message: fmt.Sprintf("provider %s preflight failed: %s", result.Provider, check.Message)}
		}
	}
	return result, nil
}

func (a CodexAppServerAdapter) StartAttempt(_ context.Context, input StartAttemptInput) (AttemptContext, error) {
	now := a.now()
	attemptID := input.IssueIdentifier
	if attemptID == "" {
		attemptID = input.IssueID
	}
	if attemptID == "" {
		attemptID = fmt.Sprintf("attempt-%d", now.UnixNano())
	}
	return AttemptContext{ID: attemptID, IssueID: input.IssueID, IssueIdentifier: input.IssueIdentifier, Workspace: input.Workspace, ExpectedBranch: input.ExpectedBranch, Branch: input.Branch, Attempt: input.Attempt, MaxTurns: input.MaxTurns, RunTimeouts: input.Timeouts}, nil
}

func (a CodexAppServerAdapter) RunAttempt(ctx context.Context, attemptID string, input RunAttemptInput, events EventSink) (AttemptResult, error) {
	if a.Client == nil {
		err := RuntimeError{Kind: RuntimeErrorKindConfiguration, Message: "provider codex_app_server has no app-server client"}
		return AttemptResult{AttemptID: attemptID, AttemptOutcome: AttemptOutcomeFailed, ErrorKind: err.Kind, Error: err.Message}, err
	}
	prompt, err := os.ReadFile(input.PromptPath)
	if err != nil {
		return AttemptResult{AttemptID: attemptID, AttemptOutcome: AttemptOutcomeFailed, ErrorKind: RuntimeErrorKindConfiguration, Error: err.Error()}, err
	}
	started := a.now()
	sink := normalizeSink(events)
	sink.Emit(RuntimeEvent{Type: RuntimeEventAttemptRunStarted, AttemptID: attemptID, Occurred: started, Phase: "implementation"})
	attempt := AttemptContext{ID: attemptID, Workspace: input.WorkingDir, MaxTurns: input.MaxTurns}
	session, err := a.StartSession(ctx, SessionStartInput{AttemptContext: attempt, WorkingDir: input.WorkingDir, ApprovalPolicy: "never", Sandbox: "workspace-write"})
	if err != nil {
		ended := a.now()
		sink.Emit(RuntimeEvent{Type: RuntimeEventAttemptRunFinished, AttemptID: attemptID, Occurred: ended, Phase: "implementation"})
		return AttemptResult{AttemptID: attemptID, AttemptOutcome: AttemptOutcomeFailed, StartedAt: started, EndedAt: ended, ErrorKind: RuntimeErrorKindExecution, Error: err.Error()}, err
	}
	turn, err := a.RunSessionTurn(ctx, session, SessionTurnInput{SessionID: session.SessionID, TurnNumber: 1, Prompt: string(prompt), WorkingDir: input.WorkingDir, Timeout: input.Timeout}, sink)
	ended := a.now()
	sink.Emit(RuntimeEvent{Type: RuntimeEventAttemptRunFinished, AttemptID: attemptID, Occurred: ended, Phase: "implementation"})
	outcome := turn.TerminalOutcome
	if outcome == "" {
		outcome = AttemptOutcomeSuccess
	}
	envelope := AttemptOutcomeEnvelope{RuntimeOutcome: outcome, Usage: turn.Usage, RawOutput: turn.Output, ErrorKind: turn.ErrorKind, Error: turn.Error}
	if turn.NeedsContinuation {
		envelope.RuntimeOutcome = AttemptOutcomeContinuation
		err = RuntimeError{Kind: RuntimeErrorKindUnsupported, Message: "provider codex_app_server returned continuation, but runner turn loop is not wired yet"}
	}
	result := AttemptResult{AttemptID: attemptID, AttemptOutcome: envelope.RuntimeOutcome, Output: turn.Output, Usage: turn.Usage, Envelope: envelope, StartedAt: started, EndedAt: ended, Error: turn.Error, ErrorKind: turn.ErrorKind}
	if err != nil {
		result.Error = err.Error()
		if result.ErrorKind == "" {
			result.ErrorKind = RuntimeErrorKindUnsupported
		}
		sink.Emit(RuntimeEvent{Type: RuntimeEventAttemptTerminalOutcome, AttemptID: attemptID, Occurred: ended, Data: map[string]any{"outcome": result.AttemptOutcome, "error": result.Error}})
		return result, err
	}
	sink.Emit(RuntimeEvent{Type: RuntimeEventAttemptTerminalOutcome, AttemptID: attemptID, Occurred: ended, Data: map[string]any{"outcome": result.AttemptOutcome}})
	return result, nil
}

func (a CodexAppServerAdapter) StartSession(ctx context.Context, input SessionStartInput) (SessionContext, error) {
	if a.Client == nil {
		return SessionContext{}, RuntimeError{Kind: RuntimeErrorKindConfiguration, Message: "provider codex_app_server has no app-server client"}
	}
	return a.Client.StartSession(ctx, input)
}

func (a CodexAppServerAdapter) RunSessionTurn(ctx context.Context, session SessionContext, input SessionTurnInput, events EventSink) (SessionTurnResult, error) {
	sink := normalizeSink(events)
	sink.Emit(RuntimeEvent{Type: RuntimeEventRuntimeOutput, AttemptID: session.AttemptID, Occurred: a.now(), Phase: "turn", Data: map[string]any{"session_id": session.SessionID, "turn": input.TurnNumber}})
	return a.Client.RunTurn(ctx, session, input)
}

func (CodexAppServerAdapter) ReviewAttempt(context.Context, string, ReviewAttemptInput, EventSink) (ReviewResult, error) {
	return ReviewResult{}, UnsupportedOperation{Operation: "review"}
}

func (CodexAppServerAdapter) Stop(context.Context, string, string) error {
	return UnsupportedOperation{Operation: "stop"}
}

func (CodexAppServerAdapter) Cancel(context.Context, string, string) error {
	return UnsupportedOperation{Operation: "cancel"}
}

func (a CodexAppServerAdapter) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}
