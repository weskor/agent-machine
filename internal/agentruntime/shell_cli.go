package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	sh "github.com/weskor/agent-machine/internal/shell"
)

// CommandRunner executes a shell command in a working directory with an
// environment, timeout, and human-readable phase label.
type CommandRunner func(command, workdir string, env map[string]string, timeout time.Duration, phase string) (string, error)

type ShellCommandBuilder func(command, promptPath string) string

const OutcomeEnvelopeLinePrefix = "AM_OUTCOME:"

// ShellCLIAdapter contains the common AgentRuntime behavior for one-shot local
// CLI providers. Provider wrappers own command shape and provider-specific
// parsing while this type keeps orchestration-agnostic execution consistent.
type ShellCLIAdapter struct {
	Provider             string
	MissingCommand       string
	RunCommand           CommandRunner
	ParseUsage           func(string) *AttemptUsage
	FirstPRURL           func(string) string
	NeedsInfoQuestions   func(string) []string
	ParseOutcomeEnvelope func(string) (AttemptOutcomeEnvelope, bool, error)
	ReviewFindings       func(string) string
	ReviewStatus         func(string) string
	ReviewClassification func(string, string) string
	BuildCommand         ShellCommandBuilder
	Now                  func() time.Time
}

func (a ShellCLIAdapter) Capabilities() RuntimeCapabilities {
	return RuntimeCapabilities{SupportsReview: true}
}

func (a ShellCLIAdapter) Preflight(_ context.Context, input PreflightInput) (PreflightResult, error) {
	result := PreflightResult{Provider: a.provider()}
	result.Checks = append(result.Checks, preflightCommandForProvider(a.provider(), "implementation_command", input.ImplementationCommand))
	if strings.TrimSpace(input.ReviewCommand) != "" {
		result.Checks = append(result.Checks, preflightCommandForProvider(a.provider(), "review_command", input.ReviewCommand))
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

func (a ShellCLIAdapter) StartAttempt(_ context.Context, input StartAttemptInput) (AttemptContext, error) {
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

func (a ShellCLIAdapter) RunAttempt(_ context.Context, attemptID string, input RunAttemptInput, events EventSink) (AttemptResult, error) {
	if strings.TrimSpace(input.Command) == "" {
		message := a.MissingCommand
		if message == "" {
			message = "missing runtime command"
		}
		return AttemptResult{AttemptID: attemptID, AttemptOutcome: AttemptOutcomeFailed, ErrorKind: RuntimeErrorKindConfiguration, Error: message}, RuntimeError{Kind: RuntimeErrorKindConfiguration, Message: message}
	}
	if a.RunCommand == nil {
		return AttemptResult{AttemptID: attemptID, AttemptOutcome: AttemptOutcomeFailed, ErrorKind: RuntimeErrorKindConfiguration, Error: "missing command runner"}, RuntimeError{Kind: RuntimeErrorKindConfiguration, Message: "missing command runner"}
	}
	sink := normalizeSink(events)
	started := a.now()
	sink.Emit(RuntimeEvent{Type: RuntimeEventAttemptRunStarted, AttemptID: attemptID, Occurred: started, Phase: "implementation"})
	output, err := a.RunCommand(a.buildCommand(input.Command, input.PromptPath), input.WorkingDir, input.Environment, input.Timeout, "implementation")
	ended := a.now()
	sink.Emit(RuntimeEvent{Type: RuntimeEventAttemptRunFinished, AttemptID: attemptID, Occurred: ended, Phase: "implementation"})
	envelope, envelopeErr := a.attemptOutcomeEnvelope(output, AttemptOutcomeSuccess, "", "")
	result := AttemptResult{AttemptID: attemptID, AttemptOutcome: envelope.RuntimeOutcome, PRURL: envelope.PRURL, Output: output, Usage: envelope.Usage, Envelope: envelope, StartedAt: started, EndedAt: ended}
	if result.AttemptOutcome == "" {
		result.AttemptOutcome = AttemptOutcomeSuccess
	}
	if envelopeErr != nil && err == nil {
		err = RuntimeError{Kind: RuntimeErrorKindExecution, Message: envelopeErr.Error(), Cause: envelopeErr}
	}
	if err == nil && result.AttemptOutcome == AttemptOutcomeFailed {
		message := envelope.Error
		if strings.TrimSpace(message) == "" {
			message = "runtime reported failed outcome"
		}
		err = RuntimeError{Kind: RuntimeErrorKindExecution, Message: message}
	}
	if err != nil {
		result.Error = err.Error()
		result.ErrorKind = RuntimeErrorKindExecution
		if errors.Is(err, sh.ErrCommandTimeout) {
			result.AttemptOutcome = AttemptOutcomeTimeout
			result.ErrorKind = RuntimeErrorKindTimeout
		}
		if result.AttemptOutcome == "" || result.AttemptOutcome == AttemptOutcomeSuccess {
			result.AttemptOutcome = AttemptOutcomeFailed
		}
		result.Envelope, _ = a.attemptOutcomeEnvelope(output, result.AttemptOutcome, result.ErrorKind, result.Error)
		sink.Emit(RuntimeEvent{Type: RuntimeEventAttemptTerminalOutcome, AttemptID: attemptID, Occurred: ended, Data: map[string]any{"outcome": result.AttemptOutcome}})
		return result, err
	}
	sink.Emit(RuntimeEvent{Type: RuntimeEventAttemptTerminalOutcome, AttemptID: attemptID, Occurred: ended, Data: map[string]any{"outcome": result.AttemptOutcome}})
	return result, nil
}

func (a ShellCLIAdapter) ReviewAttempt(_ context.Context, attemptID string, input ReviewAttemptInput, events EventSink) (ReviewResult, error) {
	if strings.TrimSpace(input.Command) == "" {
		return ReviewResult{}, nil
	}
	if a.RunCommand == nil {
		return ReviewResult{}, RuntimeError{Kind: RuntimeErrorKindConfiguration, Message: "missing command runner"}
	}
	promptPath := filepath.Join(input.WorkingDir, ".am-review-prompt.md")
	if err := os.WriteFile(promptPath, []byte(input.Prompt), 0o600); err != nil {
		return ReviewResult{}, err
	}
	sink := normalizeSink(events)
	started := a.now()
	sink.Emit(RuntimeEvent{Type: RuntimeEventReviewStarted, AttemptID: attemptID, Occurred: started, Phase: "review"})
	output, err := a.RunCommand(a.buildCommand(input.Command, promptPath), input.WorkingDir, input.Environment, input.Timeout, "review")
	ended := a.now()
	sink.Emit(RuntimeEvent{Type: RuntimeEventReviewFinished, AttemptID: attemptID, Occurred: ended, Phase: "review"})
	findings := output
	if a.ReviewFindings != nil {
		if text := a.ReviewFindings(output); text != "" {
			findings = text
		}
	}
	status := "unknown"
	if a.ReviewStatus != nil {
		status = a.ReviewStatus(findings)
	}
	classification := ""
	if a.ReviewClassification != nil {
		classification = a.ReviewClassification(status, findings)
	}
	result := ReviewResult{Status: status, Classification: classification, Findings: strings.TrimSpace(findings), Usage: a.parseUsage(output)}
	if err != nil {
		result.Status = "error"
		return result, err
	}
	return result, nil
}

func (ShellCLIAdapter) Stop(context.Context, string, string) error {
	return UnsupportedOperation{Operation: "stop"}
}
func (ShellCLIAdapter) Cancel(context.Context, string, string) error {
	return UnsupportedOperation{Operation: "cancel"}
}

func (a ShellCLIAdapter) provider() string {
	if strings.TrimSpace(a.Provider) == "" {
		return "shell_cli"
	}
	return a.Provider
}

func (a ShellCLIAdapter) buildCommand(command, promptPath string) string {
	if a.BuildCommand != nil {
		return a.BuildCommand(command, promptPath)
	}
	return fmt.Sprintf("%s < %s", strings.TrimSpace(command), sh.Quote(promptPath))
}

func (a ShellCLIAdapter) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a ShellCLIAdapter) parseUsage(output string) *AttemptUsage {
	if a.ParseUsage == nil {
		return nil
	}
	return a.ParseUsage(output)
}

func (a ShellCLIAdapter) firstPRURL(output string) string {
	if a.FirstPRURL == nil {
		return ""
	}
	return a.FirstPRURL(output)
}

func (a ShellCLIAdapter) needsInfoQuestions(output string) []string {
	if a.NeedsInfoQuestions == nil {
		return nil
	}
	return a.NeedsInfoQuestions(output)
}

func (a ShellCLIAdapter) attemptOutcomeEnvelope(output string, outcome AttemptOutcome, errorKind RuntimeErrorKind, errorMessage string) (AttemptOutcomeEnvelope, error) {
	legacy := AttemptOutcomeEnvelope{
		RuntimeOutcome:     outcome,
		PRURL:              a.firstPRURL(output),
		NeedsInfoQuestions: a.needsInfoQuestions(output),
		Usage:              a.parseUsage(output),
		RawOutput:          output,
		ErrorKind:          errorKind,
		Error:              errorMessage,
	}
	parser := a.ParseOutcomeEnvelope
	if parser == nil {
		parser = ParseAttemptOutcomeEnvelope
	}
	structured, ok, err := parser(output)
	if err != nil {
		return legacy, err
	}
	if !ok {
		return legacy, nil
	}
	return mergeAttemptOutcomeEnvelope(legacy, structured), nil
}

func ParseAttemptOutcomeEnvelope(output string) (AttemptOutcomeEnvelope, bool, error) {
	lines := strings.Split(output, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, OutcomeEnvelopeLinePrefix) {
			continue
		}
		raw := strings.TrimSpace(strings.TrimPrefix(line, OutcomeEnvelopeLinePrefix))
		if raw == "" {
			return AttemptOutcomeEnvelope{}, true, errors.New("empty structured outcome envelope")
		}
		var envelope AttemptOutcomeEnvelope
		if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
			return AttemptOutcomeEnvelope{}, true, fmt.Errorf("parse structured outcome envelope: %w", err)
		}
		if err := validateAttemptOutcomeEnvelope(envelope); err != nil {
			return AttemptOutcomeEnvelope{}, true, err
		}
		return envelope, true, nil
	}
	return AttemptOutcomeEnvelope{}, false, nil
}

func mergeAttemptOutcomeEnvelope(legacy, structured AttemptOutcomeEnvelope) AttemptOutcomeEnvelope {
	if structured.RuntimeOutcome == "" {
		structured.RuntimeOutcome = legacy.RuntimeOutcome
	}
	if structured.PRURL == "" {
		structured.PRURL = legacy.PRURL
	}
	if len(structured.NeedsInfoQuestions) == 0 {
		structured.NeedsInfoQuestions = legacy.NeedsInfoQuestions
	}
	if structured.Usage == nil {
		structured.Usage = legacy.Usage
	}
	if structured.RawOutput == "" {
		structured.RawOutput = legacy.RawOutput
	}
	if structured.ErrorKind == "" {
		structured.ErrorKind = legacy.ErrorKind
	}
	if structured.Error == "" {
		structured.Error = legacy.Error
	}
	return structured
}

func validateAttemptOutcomeEnvelope(envelope AttemptOutcomeEnvelope) error {
	switch envelope.RuntimeOutcome {
	case "", AttemptOutcomeSuccess, AttemptOutcomeFailed, AttemptOutcomeReviewFailed, AttemptOutcomeNeedsInfo, AttemptOutcomeNeedsInfoFail, AttemptOutcomeTimeout, AttemptOutcomeBudgetExceeded:
		return nil
	default:
		return fmt.Errorf("unsupported structured outcome %q", envelope.RuntimeOutcome)
	}
}

func preflightCommandForProvider(provider, name, command string) PreflightCheck {
	check := PreflightCheck{Name: name, Command: command}
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		check.Message = fmt.Sprintf("provider %s requires a non-empty %s", provider, name)
		return check
	}
	executable := firstCommandToken(trimmed)
	check.Executable = executable
	if executable == "" {
		check.Message = fmt.Sprintf("provider %s could not parse executable for %s", provider, name)
		return check
	}
	resolved, err := resolveExecutable(executable)
	if err != nil {
		check.Message = fmt.Sprintf("provider %s could not resolve executable %q for %s on PATH or as an executable path", provider, executable, name)
		return check
	}
	check.OK = true
	check.Resolved = resolved
	check.Message = fmt.Sprintf("provider %s resolved executable %q for %s", provider, executable, name)
	return check
}

func firstCommandToken(command string) string {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return ""
	}
	return strings.Trim(fields[0], "'\"")
}

func resolveExecutable(executable string) (string, error) {
	if strings.ContainsRune(executable, os.PathSeparator) {
		info, err := os.Stat(executable)
		if err != nil {
			return "", err
		}
		if info.IsDir() || info.Mode()&0o111 == 0 {
			return "", fmt.Errorf("not executable: %s", executable)
		}
		return executable, nil
	}
	return exec.LookPath(executable)
}

func normalizeSink(s EventSink) EventSink {
	if s == nil {
		return NoopSink{}
	}
	return s
}
