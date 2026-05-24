package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	sh "github.com/weskor/pi-symphony/internal/shell"
)

// CodexCLIAdapter executes attempts through `codex exec`, using stdin for the
// prepared prompt file while preserving the runner-owned AgentRuntime contract.
type CodexCLIAdapter struct {
	RunCommand           CommandRunner
	ParseUsage           func(string) *AttemptUsage
	FirstPRURL           func(string) string
	NeedsInfoQuestions   func(string) []string
	ReviewStatus         func(string) string
	ReviewClassification func(string, string) string
	Now                  func() time.Time
}

func (a CodexCLIAdapter) Capabilities() RuntimeCapabilities {
	return RuntimeCapabilities{SupportsReview: true}
}

func (a CodexCLIAdapter) Preflight(_ context.Context, input PreflightInput) (PreflightResult, error) {
	result := PreflightResult{Provider: "codex_cli"}
	if input.MaxTurns > 1 {
		result.Checks = append(result.Checks, PreflightCheck{
			Name:    "max_turns",
			OK:      false,
			Message: fmt.Sprintf("provider codex_cli supports exactly one exec attempt; agent.max_turns=%d requires a provider with a proven multi-turn contract or agent.max_turns: 1", input.MaxTurns),
		})
	}
	result.Checks = append(result.Checks, preflightCommand("implementation_command", input.ImplementationCommand))
	if strings.TrimSpace(input.ReviewCommand) != "" {
		result.Checks = append(result.Checks, preflightCommand("review_command", input.ReviewCommand))
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

func (a CodexCLIAdapter) StartAttempt(_ context.Context, input StartAttemptInput) (AttemptContext, error) {
	now := a.now()
	attemptID := input.IssueIdentifier
	if attemptID == "" {
		attemptID = input.IssueID
	}
	if attemptID == "" {
		attemptID = fmt.Sprintf("attempt-%d", now.UnixNano())
	}
	return AttemptContext{ID: attemptID, IssueID: input.IssueID, IssueIdentifier: input.IssueIdentifier, Workspace: input.Workspace, ExpectedBranch: input.ExpectedBranch, Branch: input.Branch, Attempt: input.Attempt, RunTimeouts: input.Timeouts}, nil
}

func (a CodexCLIAdapter) RunAttempt(_ context.Context, attemptID string, input RunAttemptInput, events EventSink) (AttemptResult, error) {
	if strings.TrimSpace(input.Command) == "" {
		return AttemptResult{AttemptID: attemptID, AttemptOutcome: AttemptOutcomeFailed, ErrorKind: RuntimeErrorKindConfiguration, Error: "missing Codex command"}, RuntimeError{Kind: RuntimeErrorKindConfiguration, Message: "missing Codex command"}
	}
	if a.RunCommand == nil {
		return AttemptResult{AttemptID: attemptID, AttemptOutcome: AttemptOutcomeFailed, ErrorKind: RuntimeErrorKindConfiguration, Error: "missing command runner"}, RuntimeError{Kind: RuntimeErrorKindConfiguration, Message: "missing command runner"}
	}
	sink := normalizeSink(events)
	started := a.now()
	sink.Emit(RuntimeEvent{Type: RuntimeEventAttemptRunStarted, AttemptID: attemptID, Occurred: started, Phase: "implementation"})
	output, err := a.RunCommand(codexExecCommand(input.Command, input.PromptPath), input.WorkingDir, input.Environment, input.Timeout, "implementation")
	ended := a.now()
	sink.Emit(RuntimeEvent{Type: RuntimeEventAttemptRunFinished, AttemptID: attemptID, Occurred: ended, Phase: "implementation"})
	envelope := a.attemptOutcomeEnvelope(output, AttemptOutcomeSuccess, "", "")
	result := AttemptResult{AttemptID: attemptID, AttemptOutcome: AttemptOutcomeSuccess, PRURL: envelope.PRURL, Output: output, Usage: envelope.Usage, Envelope: envelope, StartedAt: started, EndedAt: ended}
	if err != nil {
		result.AttemptOutcome = AttemptOutcomeFailed
		result.Error = err.Error()
		result.ErrorKind = RuntimeErrorKindExecution
		if errors.Is(err, sh.ErrCommandTimeout) {
			result.AttemptOutcome = AttemptOutcomeTimeout
			result.ErrorKind = RuntimeErrorKindTimeout
		}
		result.Envelope = a.attemptOutcomeEnvelope(output, result.AttemptOutcome, result.ErrorKind, result.Error)
		sink.Emit(RuntimeEvent{Type: RuntimeEventAttemptTerminalOutcome, AttemptID: attemptID, Occurred: ended, Data: map[string]any{"outcome": result.AttemptOutcome}})
		return result, err
	}
	sink.Emit(RuntimeEvent{Type: RuntimeEventAttemptTerminalOutcome, AttemptID: attemptID, Occurred: ended, Data: map[string]any{"outcome": result.AttemptOutcome}})
	return result, nil
}

func (a CodexCLIAdapter) ReviewAttempt(_ context.Context, attemptID string, input ReviewAttemptInput, events EventSink) (ReviewResult, error) {
	if strings.TrimSpace(input.Command) == "" {
		return ReviewResult{}, nil
	}
	if a.RunCommand == nil {
		return ReviewResult{}, RuntimeError{Kind: RuntimeErrorKindConfiguration, Message: "missing command runner"}
	}
	promptPath := filepath.Join(input.WorkingDir, ".pi-symphony-review-prompt.md")
	if err := os.WriteFile(promptPath, []byte(input.Prompt), 0o600); err != nil {
		return ReviewResult{}, err
	}
	sink := normalizeSink(events)
	started := a.now()
	sink.Emit(RuntimeEvent{Type: RuntimeEventReviewStarted, AttemptID: attemptID, Occurred: started, Phase: "review"})
	output, err := a.RunCommand(codexExecCommand(input.Command, promptPath), input.WorkingDir, input.Environment, input.Timeout, "review")
	ended := a.now()
	sink.Emit(RuntimeEvent{Type: RuntimeEventReviewFinished, AttemptID: attemptID, Occurred: ended, Phase: "review"})
	status := "unknown"
	if a.ReviewStatus != nil {
		status = a.ReviewStatus(output)
	}
	classification := ""
	if a.ReviewClassification != nil {
		classification = a.ReviewClassification(status, output)
	}
	result := ReviewResult{Status: status, Classification: classification, Findings: strings.TrimSpace(output), Usage: a.parseUsage(output)}
	if err != nil {
		result.Status = "error"
		return result, err
	}
	return result, nil
}

func (CodexCLIAdapter) Stop(context.Context, string, string) error {
	return UnsupportedOperation{Operation: "stop"}
}
func (CodexCLIAdapter) Cancel(context.Context, string, string) error {
	return UnsupportedOperation{Operation: "cancel"}
}

func codexExecCommand(command, promptPath string) string {
	return fmt.Sprintf("%s - < %s", strings.TrimSpace(command), sh.Quote(promptPath))
}

func (a CodexCLIAdapter) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}
func (a CodexCLIAdapter) parseUsage(output string) *AttemptUsage {
	if a.ParseUsage == nil {
		return nil
	}
	return a.ParseUsage(output)
}
func (a CodexCLIAdapter) firstPRURL(output string) string {
	if a.FirstPRURL == nil {
		return ""
	}
	return a.FirstPRURL(output)
}
func (a CodexCLIAdapter) needsInfoQuestions(output string) []string {
	if a.NeedsInfoQuestions == nil {
		return nil
	}
	return a.NeedsInfoQuestions(output)
}
func (a CodexCLIAdapter) attemptOutcomeEnvelope(output string, outcome AttemptOutcome, errorKind RuntimeErrorKind, errorMessage string) AttemptOutcomeEnvelope {
	return AttemptOutcomeEnvelope{
		RuntimeOutcome:     outcome,
		PRURL:              a.firstPRURL(output),
		NeedsInfoQuestions: a.needsInfoQuestions(output),
		Usage:              a.parseUsage(output),
		RawOutput:          output,
		ErrorKind:          errorKind,
		Error:              errorMessage,
	}
}
