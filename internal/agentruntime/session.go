package agentruntime

import (
	"context"
	"time"
)

type SessionStartInput struct {
	AttemptContext AttemptContext
	WorkingDir     string
	ApprovalPolicy string
	Sandbox        string
	Model          string
}

type SessionContext struct {
	AttemptID string `json:"attempt_id"`
	SessionID string `json:"session_id"`
	Workspace string `json:"workspace"`
	MaxTurns  int    `json:"max_turns"`
}

type SessionTurnInput struct {
	SessionID  string
	TurnNumber int
	Prompt     string
	WorkingDir string
	Timeout    time.Duration
}

type SessionTurnResult struct {
	SessionID         string
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
// runtime session across multiple runner turns.
type SessionRuntime interface {
	AgentRuntime
	StartSession(ctx context.Context, input SessionStartInput) (SessionContext, error)
	RunSessionTurn(ctx context.Context, session SessionContext, input SessionTurnInput, events EventSink) (SessionTurnResult, error)
}

// SessionClient is the provider-neutral transport seam behind SessionRuntime.
// Adapter implementations map this to provider-specific protocol terms, such as
// Codex app-server threads or future Claude/API session identifiers.
type SessionClient interface {
	StartSession(ctx context.Context, input SessionStartInput) (SessionContext, error)
	RunTurn(ctx context.Context, session SessionContext, input SessionTurnInput) (SessionTurnResult, error)
}
