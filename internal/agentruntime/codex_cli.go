package agentruntime

import (
	"context"
	"fmt"
	"strings"
	"time"

	sh "github.com/weskor/agent-machine/internal/shell"
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
	return a.shell().Capabilities()
}

func (a CodexCLIAdapter) Preflight(ctx context.Context, input PreflightInput) (PreflightResult, error) {
	return a.shell().Preflight(ctx, input)
}

func (a CodexCLIAdapter) StartAttempt(ctx context.Context, input StartAttemptInput) (AttemptContext, error) {
	return a.shell().StartAttempt(ctx, input)
}

func (a CodexCLIAdapter) RunAttempt(ctx context.Context, attemptID string, input RunAttemptInput, events EventSink) (AttemptResult, error) {
	return a.shell().RunAttempt(ctx, attemptID, input, events)
}

func (a CodexCLIAdapter) ReviewAttempt(ctx context.Context, attemptID string, input ReviewAttemptInput, events EventSink) (ReviewResult, error) {
	return a.shell().ReviewAttempt(ctx, attemptID, input, events)
}

func (a CodexCLIAdapter) Stop(ctx context.Context, attemptID, reason string) error {
	return a.shell().Stop(ctx, attemptID, reason)
}
func (a CodexCLIAdapter) Cancel(ctx context.Context, attemptID, reason string) error {
	return a.shell().Cancel(ctx, attemptID, reason)
}

func codexExecCommand(command, promptPath string) string {
	return fmt.Sprintf("%s - < %s", strings.TrimSpace(command), sh.Quote(promptPath))
}

func (a CodexCLIAdapter) shell() ShellCLIAdapter {
	return ShellCLIAdapter{
		Provider:             "codex_cli",
		MissingCommand:       "missing Codex command",
		RunCommand:           a.RunCommand,
		ParseUsage:           a.ParseUsage,
		FirstPRURL:           a.FirstPRURL,
		NeedsInfoQuestions:   a.NeedsInfoQuestions,
		ReviewStatus:         a.ReviewStatus,
		ReviewClassification: a.ReviewClassification,
		BuildCommand:         codexExecCommand,
		Now:                  a.Now,
	}
}
