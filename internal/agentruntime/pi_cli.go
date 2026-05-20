package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	sh "github.com/weskor/pi-symphony/internal/shell"
)

// CommandRunner executes a shell command in a working directory with an
// environment, timeout, and human-readable phase label.
type CommandRunner func(command, workdir string, env map[string]string, timeout time.Duration, phase string) (string, error)

// PiCLIAdapter routes runtime execution through the existing Pi CLI command
// shape while exposing the AgentRuntime seam to orchestration.
type PiCLIAdapter struct {
	RunCommand           CommandRunner
	ParseUsage           func(string) *AttemptUsage
	FirstPRURL           func(string) string
	AssistantText        func(string) string
	ReviewStatus         func(string) string
	ReviewClassification func(string, string) string
	Now                  func() time.Time
}

func (a PiCLIAdapter) Capabilities() RuntimeCapabilities {
	return RuntimeCapabilities{SupportsReview: true}
}

func (a PiCLIAdapter) Preflight(_ context.Context, input PreflightInput) (PreflightResult, error) {
	result := PreflightResult{Provider: "pi_cli"}
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

func preflightCommand(name, command string) PreflightCheck {
	check := PreflightCheck{Name: name, Command: command}
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		check.Message = fmt.Sprintf("provider pi_cli requires a non-empty %s", name)
		return check
	}
	executable := firstCommandToken(trimmed)
	check.Executable = executable
	if executable == "" {
		check.Message = fmt.Sprintf("provider pi_cli could not parse executable for %s", name)
		return check
	}
	resolved, err := resolveExecutable(executable)
	if err != nil {
		check.Message = fmt.Sprintf("provider pi_cli could not resolve executable %q for %s on PATH or as an executable path", executable, name)
		return check
	}
	check.OK = true
	check.Resolved = resolved
	check.Message = fmt.Sprintf("provider pi_cli resolved executable %q for %s", executable, name)
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

func (a PiCLIAdapter) StartAttempt(_ context.Context, input StartAttemptInput) (AttemptContext, error) {
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

func (a PiCLIAdapter) RunAttempt(_ context.Context, attemptID string, input RunAttemptInput, events EventSink) (AttemptResult, error) {
	if strings.TrimSpace(input.Command) == "" {
		return AttemptResult{AttemptID: attemptID, AttemptOutcome: AttemptOutcomeFailed, ErrorKind: RuntimeErrorKindConfiguration, Error: "missing Pi command"}, RuntimeError{Kind: RuntimeErrorKindConfiguration, Message: "missing Pi command"}
	}
	if a.RunCommand == nil {
		return AttemptResult{AttemptID: attemptID, AttemptOutcome: AttemptOutcomeFailed, ErrorKind: RuntimeErrorKindConfiguration, Error: "missing command runner"}, RuntimeError{Kind: RuntimeErrorKindConfiguration, Message: "missing command runner"}
	}
	sink := normalizeSink(events)
	started := a.now()
	sink.Emit(RuntimeEvent{Type: RuntimeEventAttemptRunStarted, AttemptID: attemptID, Occurred: started, Phase: "implementation"})
	output, err := a.RunCommand(fmt.Sprintf("%s @%s", input.Command, sh.Quote(input.PromptPath)), input.WorkingDir, input.Environment, input.Timeout, "implementation")
	ended := a.now()
	sink.Emit(RuntimeEvent{Type: RuntimeEventAttemptRunFinished, AttemptID: attemptID, Occurred: ended, Phase: "implementation"})
	result := AttemptResult{AttemptID: attemptID, AttemptOutcome: AttemptOutcomeSuccess, PRURL: a.firstPRURL(output), Output: output, Usage: a.parseUsage(output), StartedAt: started, EndedAt: ended}
	if err != nil {
		result.AttemptOutcome = AttemptOutcomeFailed
		result.Error = err.Error()
		result.ErrorKind = RuntimeErrorKindExecution
		if errors.Is(err, sh.ErrCommandTimeout) {
			result.AttemptOutcome = AttemptOutcomeTimeout
			result.ErrorKind = RuntimeErrorKindTimeout
		}
		sink.Emit(RuntimeEvent{Type: RuntimeEventAttemptTerminalOutcome, AttemptID: attemptID, Occurred: ended, Data: map[string]any{"outcome": result.AttemptOutcome}})
		return result, err
	}
	sink.Emit(RuntimeEvent{Type: RuntimeEventAttemptTerminalOutcome, AttemptID: attemptID, Occurred: ended, Data: map[string]any{"outcome": result.AttemptOutcome}})
	return result, nil
}

func (a PiCLIAdapter) ReviewAttempt(_ context.Context, attemptID string, input ReviewAttemptInput, events EventSink) (ReviewResult, error) {
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
	output, err := a.RunCommand(fmt.Sprintf("%s @%s", input.Command, sh.Quote(promptPath)), input.WorkingDir, input.Environment, input.Timeout, "review")
	ended := a.now()
	sink.Emit(RuntimeEvent{Type: RuntimeEventReviewFinished, AttemptID: attemptID, Occurred: ended, Phase: "review"})
	findings := output
	if a.AssistantText != nil {
		if text := a.AssistantText(output); text != "" {
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

func (PiCLIAdapter) Stop(context.Context, string, string) error {
	return UnsupportedOperation{Operation: "stop"}
}
func (PiCLIAdapter) Cancel(context.Context, string, string) error {
	return UnsupportedOperation{Operation: "cancel"}
}

func (a PiCLIAdapter) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}
func (a PiCLIAdapter) parseUsage(output string) *AttemptUsage {
	if a.ParseUsage == nil {
		return nil
	}
	return a.ParseUsage(output)
}
func (a PiCLIAdapter) firstPRURL(output string) string {
	if a.FirstPRURL == nil {
		return ""
	}
	return a.FirstPRURL(output)
}
func normalizeSink(s EventSink) EventSink {
	if s == nil {
		return NoopSink{}
	}
	return s
}
