package agentruntime

import (
	"context"
	"fmt"
	"strings"
	"time"

	sh "github.com/weskor/agent-machine/internal/shell"
)

// PiCLIAdapter routes runtime execution through the existing Pi CLI command
// shape while exposing the AgentRuntime seam to orchestration.
type PiCLIAdapter struct {
	RunCommand           CommandRunner
	ParseUsage           func(string) *AttemptUsage
	FirstPRURL           func(string) string
	NeedsInfoQuestions   func(string) []string
	AssistantText        func(string) string
	ReviewStatus         func(string) string
	ReviewClassification func(string, string) string
	Now                  func() time.Time
}

func (a PiCLIAdapter) Capabilities() RuntimeCapabilities {
	return a.shell().Capabilities()
}

func (a PiCLIAdapter) Preflight(ctx context.Context, input PreflightInput) (PreflightResult, error) {
	return a.shell().Preflight(ctx, input)
}

func (a PiCLIAdapter) StartAttempt(ctx context.Context, input StartAttemptInput) (AttemptContext, error) {
	return a.shell().StartAttempt(ctx, input)
}

func (a PiCLIAdapter) RunAttempt(ctx context.Context, attemptID string, input RunAttemptInput, events EventSink) (AttemptResult, error) {
	return a.shell().RunAttempt(ctx, attemptID, input, events)
}

func (a PiCLIAdapter) ReviewAttempt(ctx context.Context, attemptID string, input ReviewAttemptInput, events EventSink) (ReviewResult, error) {
	return a.shell().ReviewAttempt(ctx, attemptID, input, events)
}

func (a PiCLIAdapter) Stop(ctx context.Context, attemptID, reason string) error {
	return a.shell().Stop(ctx, attemptID, reason)
}
func (a PiCLIAdapter) Cancel(ctx context.Context, attemptID, reason string) error {
	return a.shell().Cancel(ctx, attemptID, reason)
}

func piCLICommand(command, promptPath string) string {
	return fmt.Sprintf("%s @%s", strings.TrimSpace(command), sh.Quote(promptPath))
}

func (a PiCLIAdapter) shell() ShellCLIAdapter {
	return ShellCLIAdapter{
		Provider:             "pi_cli",
		MissingCommand:       "missing Pi command",
		RunCommand:           a.RunCommand,
		ParseUsage:           a.ParseUsage,
		FirstPRURL:           a.FirstPRURL,
		NeedsInfoQuestions:   a.NeedsInfoQuestions,
		ReviewFindings:       a.AssistantText,
		ReviewStatus:         a.ReviewStatus,
		ReviewClassification: a.ReviewClassification,
		BuildCommand:         piCLICommand,
		Now:                  a.Now,
	}
}
