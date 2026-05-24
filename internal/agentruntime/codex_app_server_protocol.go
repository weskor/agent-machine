package agentruntime

const (
	CodexAppServerProvider = "codex_app_server"

	CodexRPCInitialize  = "initialize"
	CodexRPCThreadStart = "thread/start"
	CodexRPCTurnStart   = "turn/start"
	CodexRPCTurnSteer   = "turn/steer"
	CodexRPCTurnCancel  = "turn/interrupt"
)

type CodexRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type CodexThreadStartParams struct {
	ApprovalPolicy string         `json:"approvalPolicy,omitempty"`
	CWD            string         `json:"cwd,omitempty"`
	Ephemeral      *bool          `json:"ephemeral,omitempty"`
	Sandbox        string         `json:"sandbox,omitempty"`
	Model          string         `json:"model,omitempty"`
	ThreadSource   *CodexSource   `json:"threadSource,omitempty"`
	Config         map[string]any `json:"config,omitempty"`
}

type CodexTurnStartParams struct {
	ThreadID       string           `json:"threadId"`
	Input          []CodexUserInput `json:"input"`
	CWD            string           `json:"cwd,omitempty"`
	ApprovalPolicy string           `json:"approvalPolicy,omitempty"`
	SandboxPolicy  string           `json:"sandboxPolicy,omitempty"`
	Model          string           `json:"model,omitempty"`
}

type CodexSource struct {
	Kind string `json:"kind"`
}

type CodexUserInput struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func NewCodexThreadStartRequest(id int64, input SessionStartInput) CodexRPCRequest {
	ephemeral := false
	return CodexRPCRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  CodexRPCThreadStart,
		Params: CodexThreadStartParams{
			ApprovalPolicy: input.ApprovalPolicy,
			CWD:            input.WorkingDir,
			Ephemeral:      &ephemeral,
			Sandbox:        input.Sandbox,
			Model:          input.Model,
			ThreadSource:   &CodexSource{Kind: "pi_symphony"},
			Config:         map[string]any{"ignore_user_config": true, "ignore_rules": true},
		},
	}
}

func NewCodexTurnStartRequest(id int64, input SessionTurnInput, approvalPolicy, sandboxPolicy, model string) CodexRPCRequest {
	return CodexRPCRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  CodexRPCTurnStart,
		Params: CodexTurnStartParams{
			ThreadID:       input.ThreadID,
			Input:          []CodexUserInput{{Type: "input_text", Text: input.Prompt}},
			CWD:            input.WorkingDir,
			ApprovalPolicy: approvalPolicy,
			SandboxPolicy:  sandboxPolicy,
			Model:          model,
		},
	}
}
