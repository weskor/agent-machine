package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// AttemptOutcome is the terminal outcome vocabulary for a runtime attempt.
//
// These outcomes are intentionally minimal and transport-agnostic; orchestration
// layers should classify these values into broader runner outcomes.
type AttemptOutcome string

const (
	AttemptOutcomeSuccess        AttemptOutcome = "success"
	AttemptOutcomeFailed         AttemptOutcome = "failed"
	AttemptOutcomeReviewFailed   AttemptOutcome = "review_failed"
	AttemptOutcomeNeedsInfo      AttemptOutcome = "needs_info"
	AttemptOutcomeNeedsInfoFail  AttemptOutcome = "needs_info_failed"
	AttemptOutcomeContinuation   AttemptOutcome = "needs_continuation"
	AttemptOutcomeTimeout        AttemptOutcome = "timeout"
	AttemptOutcomeBudgetExceeded AttemptOutcome = "budget_exceeded"
)

// RuntimeEventType captures cross-runtime lifecycle signals. Implementers should
// emit stable event types so schedulers can inspect runtime behavior.
type RuntimeEventType string

const (
	RuntimeEventAttemptStarted         RuntimeEventType = "attempt_started"
	RuntimeEventAttemptRunStarted      RuntimeEventType = "attempt_run_started"
	RuntimeEventAttemptRunFinished     RuntimeEventType = "attempt_run_finished"
	RuntimeEventReviewStarted          RuntimeEventType = "review_started"
	RuntimeEventReviewFinished         RuntimeEventType = "review_finished"
	RuntimeEventRunCanceled            RuntimeEventType = "run_canceled"
	RuntimeEventRunStopped             RuntimeEventType = "run_stopped"
	RuntimeEventRuntimeOutput          RuntimeEventType = "runtime_output"
	RuntimeEventRuntimeError           RuntimeEventType = "runtime_error"
	RuntimeEventRuntimeTimeout         RuntimeEventType = "runtime_timeout"
	RuntimeEventAttemptTerminalOutcome RuntimeEventType = "attempt_terminal_outcome"
)

// RuntimeEvent is emitted by implementations to describe adapter-visible actions.
type RuntimeEvent struct {
	Type      RuntimeEventType `json:"type"`
	AttemptID string           `json:"attempt_id"`
	Occurred  time.Time        `json:"occurred"`
	Phase     string           `json:"phase,omitempty"`
	Message   string           `json:"message,omitempty"`
	Data      map[string]any   `json:"data,omitempty"`
}

// EventSink receives runtime events.
type EventSink interface {
	Emit(event RuntimeEvent)
}

type NoopSink struct{}

func (NoopSink) Emit(RuntimeEvent) {}

// RuntimeErrorKind distinguishes structured runtime failures.
type RuntimeErrorKind string

const (
	RuntimeErrorKindTimeout       RuntimeErrorKind = "timeout"
	RuntimeErrorKindConfiguration RuntimeErrorKind = "configuration"
	RuntimeErrorKindExecution     RuntimeErrorKind = "execution"
	RuntimeErrorKindCanceled      RuntimeErrorKind = "canceled"
	RuntimeErrorKindUnsupported   RuntimeErrorKind = "unsupported"
)

// RuntimeError is a machine-readable runtime failure.
type RuntimeError struct {
	Kind    RuntimeErrorKind `json:"kind"`
	Message string           `json:"message"`
	Cause   error            `json:"-"`
}

func (e RuntimeError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("runtime %s: %s (%v)", e.Kind, e.Message, e.Cause)
	}
	if e.Message != "" {
		return fmt.Sprintf("runtime %s: %s", e.Kind, e.Message)
	}
	return string(e.Kind)
}

// UnsupportedOperation indicates an operation that is explicitly not supported
// by the adapter.
type UnsupportedOperation struct {
	Operation string
}

func (e UnsupportedOperation) Error() string {
	return fmt.Sprintf("unsupported runtime operation: %s", e.Operation)
}

func IsUnsupportedOperation(err error) bool {
	if err == nil {
		return false
	}
	var op UnsupportedOperation
	return errors.As(err, &op)
}

// RuntimeCapabilities declares which optional operations are implemented.
type RuntimeCapabilities struct {
	SupportsReview           bool
	SupportsCancel           bool
	SupportsStop             bool
	SupportsSessions         bool
	SupportsTurnContinuation bool
	SupportsMaxTurns         bool
}

func (c RuntimeCapabilities) CanRunMultipleTurns() bool {
	return c.SupportsSessions && c.SupportsTurnContinuation && c.SupportsMaxTurns
}

// PreflightInput describes runtime commands that must be available before the
// runner claims or mutates work.
type PreflightInput struct {
	ImplementationCommand string
	ReviewCommand         string
	MaxTurns              int
}

// PreflightCheck captures one prerequisite checked by a runtime provider.
type PreflightCheck struct {
	Name       string `json:"name"`
	Command    string `json:"command,omitempty"`
	Executable string `json:"executable,omitempty"`
	Resolved   string `json:"resolved,omitempty"`
	OK         bool   `json:"ok"`
	Message    string `json:"message,omitempty"`
}

// PreflightResult is provider-aware, actionable evidence about runtime
// readiness. It must not include secrets or expanded environment values.
type PreflightResult struct {
	Provider string           `json:"provider"`
	Checks   []PreflightCheck `json:"checks,omitempty"`
}

func (r PreflightResult) OK() bool {
	for _, check := range r.Checks {
		if !check.OK {
			return false
		}
	}
	return true
}

// AttemptTimeouts maps the same coarse time-bound concepts currently used by the
// runner's budget model.
type AttemptTimeouts struct {
	WallClock time.Duration
	Command   time.Duration
	Review    time.Duration
}

// AttemptContext identifies an active session/attempt at runtime level.
type AttemptContext struct {
	ID              string          `json:"id"`
	IssueID         string          `json:"issue_id"`
	IssueIdentifier string          `json:"issue_identifier"`
	Workspace       string          `json:"workspace"`
	ExpectedBranch  string          `json:"expected_branch,omitempty"`
	Branch          string          `json:"branch,omitempty"`
	Attempt         int             `json:"attempt"`
	MaxTurns        int             `json:"max_turns,omitempty"`
	RuntimeThreadID string          `json:"runtime_thread_id,omitempty"`
	RunTimeouts     AttemptTimeouts `json:"run_timeouts"`
}

// AttemptUsage holds parsed usage telemetry that can be surfaced for audit and
// cost enforcement, without hardcoding an agent implementation.
type AttemptUsage struct {
	Input       float64 `json:"input"`
	Output      float64 `json:"output"`
	CacheRead   float64 `json:"cache_read"`
	CacheWrite  float64 `json:"cache_write"`
	TotalTokens float64 `json:"total_tokens"`
	CostTotal   float64 `json:"cost_total"`
}

// StartAttemptInput and RunAttemptInput include only execution configuration.
type StartAttemptInput struct {
	IssueID         string
	IssueIdentifier string
	Workspace       string
	Branch          string
	ExpectedBranch  string
	Attempt         int
	MaxTurns        int
	WorkingDir      string
	Command         string
	PromptPath      string
	Timeouts        AttemptTimeouts
	Environment     map[string]string
}

type RunAttemptInput struct {
	Command     string
	PromptPath  string
	WorkingDir  string
	Timeout     time.Duration
	MaxTurns    int
	Environment map[string]string
}

type SessionStartInput struct {
	AttemptContext AttemptContext
	WorkingDir     string
	ApprovalPolicy string
	Sandbox        string
	Model          string
}

type SessionContext struct {
	AttemptID string `json:"attempt_id"`
	ThreadID  string `json:"thread_id"`
	Workspace string `json:"workspace"`
	MaxTurns  int    `json:"max_turns"`
}

type SessionTurnInput struct {
	ThreadID   string
	TurnNumber int
	Prompt     string
	WorkingDir string
	Timeout    time.Duration
}

type SessionTurnResult struct {
	ThreadID          string
	TurnID            string
	TurnNumber        int
	Output            string
	Usage             *AttemptUsage
	NeedsContinuation bool
	TerminalOutcome   AttemptOutcome
	ErrorKind         RuntimeErrorKind
	Error             string
}

// SessionRuntime extends AgentRuntime for providers that keep one durable
// runtime thread across multiple runner turns.
type SessionRuntime interface {
	AgentRuntime
	StartSession(ctx context.Context, input SessionStartInput) (SessionContext, error)
	RunSessionTurn(ctx context.Context, session SessionContext, input SessionTurnInput, events EventSink) (SessionTurnResult, error)
}

type ReviewAttemptInput struct {
	Command     string
	WorkingDir  string
	Prompt      string
	PullRequest string
	Timeout     time.Duration
	Environment map[string]string
}

// ReviewResult captures optional review command output.
type ReviewResult struct {
	Status         string        `json:"status"`
	Classification string        `json:"classification,omitempty"`
	Findings       string        `json:"findings,omitempty"`
	Usage          *AttemptUsage `json:"usage,omitempty"`
}

// AttemptOutcomeEnvelope is the structured runner-facing projection of legacy
// text runtime output. The raw output remains available for compatibility, but
// deterministic orchestration should prefer these parsed fields when present.
type AttemptOutcomeEnvelope struct {
	RuntimeOutcome     AttemptOutcome   `json:"runtime_outcome"`
	PRURL              string           `json:"pr_url,omitempty"`
	NeedsInfoQuestions []string         `json:"needs_info_questions,omitempty"`
	ContinuationPrompt string           `json:"continuation_prompt,omitempty"`
	ContinuationReason string           `json:"reason,omitempty"`
	ChangedFiles       []string         `json:"changed_files,omitempty"`
	Validation         []string         `json:"validation,omitempty"`
	Usage              *AttemptUsage    `json:"usage,omitempty"`
	RawOutput          string           `json:"raw_output,omitempty"`
	DebugOutputRef     string           `json:"debug_output_ref,omitempty"`
	ErrorKind          RuntimeErrorKind `json:"error_kind,omitempty"`
	Error              string           `json:"error,omitempty"`
}

// AttemptResult is the terminal projection of a runtime execution.
type AttemptResult struct {
	AttemptID      string
	AttemptOutcome AttemptOutcome
	PRURL          string
	Output         string
	Usage          *AttemptUsage
	Envelope       AttemptOutcomeEnvelope
	StartedAt      time.Time
	EndedAt        time.Time
	Error          string
	ErrorKind      RuntimeErrorKind
}

// AgentRuntime defines the execution contract for agent processes (including
// future adapters such as app-server, MCP, ACP) without encoding orchestration.
//
// Orchestration remains in the runner; it decides scheduling, retries,
// handoff state transitions, and workspace lease rules.
type AgentRuntime interface {
	Capabilities() RuntimeCapabilities
	Preflight(ctx context.Context, input PreflightInput) (PreflightResult, error)
	StartAttempt(ctx context.Context, input StartAttemptInput) (AttemptContext, error)
	RunAttempt(ctx context.Context, attemptID string, input RunAttemptInput, events EventSink) (AttemptResult, error)
	ReviewAttempt(ctx context.Context, attemptID string, input ReviewAttemptInput, events EventSink) (ReviewResult, error)
	Stop(ctx context.Context, attemptID, reason string) error
	Cancel(ctx context.Context, attemptID, reason string) error
}

// IsTerminalOutcome indicates whether an attempt outcome is terminal.
func IsTerminalOutcome(outcome AttemptOutcome) bool {
	switch outcome {
	case AttemptOutcomeSuccess, AttemptOutcomeFailed, AttemptOutcomeReviewFailed, AttemptOutcomeNeedsInfo, AttemptOutcomeNeedsInfoFail, AttemptOutcomeTimeout, AttemptOutcomeBudgetExceeded:
		return true
	default:
		return false
	}
}
