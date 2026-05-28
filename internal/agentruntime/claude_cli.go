package agentruntime

import (
	"context"
	"fmt"
	"strings"
	"time"

	sh "github.com/weskor/agent-machine/internal/shell"
)

// ClaudeCLIAdapter executes attempts through `claude --print`, using stdin for
// the prepared prompt file while preserving the runner-owned AgentRuntime
// contract.
type ClaudeCLIAdapter struct {
	RunCommand           CommandRunner
	ParseUsage           func(string) *AttemptUsage
	FirstPRURL           func(string) string
	NeedsInfoQuestions   func(string) []string
	ParseOutcomeEnvelope func(string) (AttemptOutcomeEnvelope, bool, error)
	ReviewFindings       func(string) string
	ReviewStatus         func(string) string
	ReviewClassification func(string, string) string
	Now                  func() time.Time
}

func (a ClaudeCLIAdapter) Capabilities() RuntimeCapabilities {
	return a.shell().Capabilities()
}

func (a ClaudeCLIAdapter) Preflight(ctx context.Context, input PreflightInput) (PreflightResult, error) {
	return a.shell().Preflight(ctx, input)
}

func (a ClaudeCLIAdapter) StartAttempt(ctx context.Context, input StartAttemptInput) (AttemptContext, error) {
	return a.shell().StartAttempt(ctx, input)
}

func (a ClaudeCLIAdapter) RunAttempt(ctx context.Context, attemptID string, input RunAttemptInput, events EventSink) (AttemptResult, error) {
	return a.shell().RunAttempt(ctx, attemptID, input, events)
}

func (a ClaudeCLIAdapter) ReviewAttempt(ctx context.Context, attemptID string, input ReviewAttemptInput, events EventSink) (ReviewResult, error) {
	return a.shell().ReviewAttempt(ctx, attemptID, input, events)
}

func (a ClaudeCLIAdapter) Stop(ctx context.Context, attemptID, reason string) error {
	return a.shell().Stop(ctx, attemptID, reason)
}
func (a ClaudeCLIAdapter) Cancel(ctx context.Context, attemptID, reason string) error {
	return a.shell().Cancel(ctx, attemptID, reason)
}

func claudePrintCommand(command, promptPath string) string {
	return fmt.Sprintf("%s < %s", strings.TrimSpace(command), sh.Quote(promptPath))
}

func (a ClaudeCLIAdapter) shell() ShellCLIAdapter {
	return ShellCLIAdapter{
		Provider:             "claude_cli",
		MissingCommand:       "missing Claude command",
		RunCommand:           a.RunCommand,
		ParseUsage:           a.ParseUsage,
		FirstPRURL:           a.FirstPRURL,
		NeedsInfoQuestions:   a.NeedsInfoQuestions,
		ParseOutcomeEnvelope: a.ParseOutcomeEnvelope,
		ReviewFindings:       a.ReviewFindings,
		ReviewStatus:         a.ReviewStatus,
		ReviewClassification: a.ReviewClassification,
		BuildCommand:         claudePrintCommand,
		Now:                  a.Now,
	}
}
