package artifacts

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/weskor/agent-machine/internal/domain"
	"github.com/weskor/agent-machine/internal/state"
)

const (
	RunRecordName  = ".am-run.json"
	EvaluationName = ".am-evaluation.json"
	FeedbackName   = ".am-feedback.md"

	CurrentArtifactSchemaVersion = 1
	ArtifactSchemaSourceCurrent  = "current"
	ArtifactSchemaSourceLegacy   = "legacy"
)

type EvaluationArtifact struct {
	SchemaVersion                int      `json:"schema_version"`
	SchemaSource                 string   `json:"schema_source,omitempty"`
	IssueIdentifier              string   `json:"issue_identifier"`
	IssueID                      string   `json:"issue_id,omitempty"`
	PRURL                        string   `json:"pr_url,omitempty"`
	FinalStatus                  string   `json:"final_status"`
	DurationMS                   int64    `json:"duration_ms"`
	ImplementationTotalTokens    float64  `json:"implementation_total_tokens,omitempty"`
	ImplementationTotalCost      float64  `json:"implementation_total_cost,omitempty"`
	ReviewTotalTokens            float64  `json:"review_total_tokens,omitempty"`
	ReviewTotalCost              float64  `json:"review_total_cost,omitempty"`
	TotalTokens                  float64  `json:"total_tokens,omitempty"`
	TotalCost                    float64  `json:"total_cost,omitempty"`
	ChecksStatus                 string   `json:"checks_status"`
	ReviewStatus                 string   `json:"review_status,omitempty"`
	ReviewClassification         string   `json:"review_classification,omitempty"`
	ReviewPassed                 *bool    `json:"review_passed,omitempty"`
	Outcome                      string   `json:"outcome"`
	MergeEligible                bool     `json:"merge_eligible"`
	BlockedBy                    []string `json:"blocked_by,omitempty"`
	RootCause                    string   `json:"root_cause,omitempty"`
	NextAction                   string   `json:"next_action,omitempty"`
	ShouldRetry                  bool     `json:"should_retry"`
	OperatorAttentionRequired    bool     `json:"operator_attention_required"`
	FeedbackRetryCount           int      `json:"feedback_retry_count,omitempty"`
	NeedsInfoUsed                bool     `json:"needs_info_used"`
	MergeBlockReason             string   `json:"merge_block_reason,omitempty"`
	MergeBlockerCodes            []string `json:"merge_blocker_codes,omitempty"`
	WorkspaceCleanupEligible     bool     `json:"workspace_cleanup_eligible"`
	FrictionSignals              []string `json:"friction_signals,omitempty"`
	CandidateHarnessImprovements []string `json:"candidate_harness_improvements,omitempty"`
	BehaviorContractEvidence     []string `json:"behavior_contract_evidence,omitempty"`
	TicketContractEvidence       []string `json:"ticket_contract_evidence,omitempty"`
}

type Manager struct {
	Evaluate       func(workspace string, record domain.RunRecord) EvaluationArtifact
	PRStateForURL  func(prURL string) (string, bool, error)
	TerminalStatus func(status string) bool
}

func RunRecordPath(workspace string) string  { return filepath.Join(workspace, RunRecordName) }
func EvaluationPath(workspace string) string { return filepath.Join(workspace, EvaluationName) }
func FeedbackPath(workspace string) string   { return filepath.Join(workspace, FeedbackName) }

func (m Manager) WriteRunRecord(workspace string, record domain.RunRecord) (string, error) {
	runPath := RunRecordPath(workspace)
	if err := writeVersionedRunRecord(runPath, record); err != nil {
		return runPath, err
	}
	return runPath, nil
}

func (m Manager) WriteEvaluation(workspace string, record domain.RunRecord) (string, EvaluationArtifact, error) {
	evaluation := m.Evaluate(workspace, record)
	markCurrentEvaluation(&evaluation)
	path := EvaluationPath(workspace)
	return path, evaluation, writeJSON(path, evaluation)
}

func writeVersionedRunRecord(path string, record domain.RunRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	var artifact map[string]any
	if err := json.Unmarshal(data, &artifact); err != nil {
		return err
	}
	artifact["schema_version"] = CurrentArtifactSchemaVersion
	artifact["schema_source"] = ArtifactSchemaSourceCurrent
	return writeJSON(path, artifact)
}

func markCurrentEvaluation(evaluation *EvaluationArtifact) {
	if evaluation.SchemaVersion == 0 {
		evaluation.SchemaVersion = CurrentArtifactSchemaVersion
	}
	if evaluation.SchemaSource == "" {
		evaluation.SchemaSource = ArtifactSchemaSourceCurrent
	}
}

func inferArtifactSchema(data []byte, artifactName string) (int, string, error) {
	var envelope struct {
		SchemaVersion *int   `json:"schema_version"`
		SchemaSource  string `json:"schema_source"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return 0, "", fmt.Errorf("malformed %s schema metadata: %w", artifactName, err)
	}
	if envelope.SchemaVersion == nil || *envelope.SchemaVersion == 0 {
		return CurrentArtifactSchemaVersion, ArtifactSchemaSourceLegacy, nil
	}
	if *envelope.SchemaVersion != CurrentArtifactSchemaVersion {
		return 0, "", fmt.Errorf("unsupported %s schema_version %d", artifactName, *envelope.SchemaVersion)
	}
	source := strings.TrimSpace(envelope.SchemaSource)
	if source == "" {
		source = ArtifactSchemaSourceCurrent
	}
	return *envelope.SchemaVersion, source, nil
}

func ValidateArtifactSchema(data []byte, artifactName string) (int, string, error) {
	return inferArtifactSchema(data, artifactName)
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func (m Manager) ReadBackfill(workspace, workspaceRoot string) (domain.RunRecord, EvaluationArtifact, time.Time, error) {
	path := RunRecordPath(workspace)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return domain.RunRecord{}, EvaluationArtifact{}, time.Time{}, fmt.Errorf("missing %s", RunRecordName)
		}
		return domain.RunRecord{}, EvaluationArtifact{}, time.Time{}, fmt.Errorf("read run record: %w", err)
	}
	_, runSchemaSource, err := inferArtifactSchema(data, RunRecordName)
	if err != nil {
		return domain.RunRecord{}, EvaluationArtifact{}, time.Time{}, err
	}
	var record domain.RunRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return domain.RunRecord{}, EvaluationArtifact{}, time.Time{}, fmt.Errorf("malformed %s: %w", RunRecordName, err)
	}
	if strings.TrimSpace(record.IssueIdentifier) == "" {
		return domain.RunRecord{}, EvaluationArtifact{}, time.Time{}, fmt.Errorf("missing required issue_identifier")
	}
	if strings.TrimSpace(record.Status) == "" {
		return domain.RunRecord{}, EvaluationArtifact{}, time.Time{}, fmt.Errorf("missing required status")
	}
	if record.Workspace == "" {
		record.Workspace = workspace
	}
	if record.WorkspaceRoot == "" {
		record.WorkspaceRoot = workspaceRoot
	}
	evaluation := m.Evaluate(workspace, record)
	markCurrentEvaluation(&evaluation)
	if evalData, err := os.ReadFile(EvaluationPath(workspace)); err == nil {
		evalVersion, evalSource, err := inferArtifactSchema(evalData, EvaluationName)
		if err != nil {
			return domain.RunRecord{}, EvaluationArtifact{}, time.Time{}, err
		}
		if err := json.Unmarshal(evalData, &evaluation); err != nil {
			return domain.RunRecord{}, EvaluationArtifact{}, time.Time{}, fmt.Errorf("malformed %s: %w", EvaluationName, err)
		}
		evaluation.SchemaVersion = evalVersion
		evaluation.SchemaSource = evalSource
	} else if err != nil && !os.IsNotExist(err) {
		return domain.RunRecord{}, EvaluationArtifact{}, time.Time{}, fmt.Errorf("read %s: %w", EvaluationName, err)
	} else {
		evaluation.SchemaSource = runSchemaSource
	}
	artifactTime := record.EndedAt
	if artifactTime.IsZero() {
		artifactTime = record.StartedAt
	}
	if artifactTime.IsZero() {
		if info, err := os.Stat(path); err == nil {
			artifactTime = info.ModTime()
		}
	}
	return record, evaluation, artifactTime, nil
}

type SnapshotOptions struct {
	BranchName       string
	BaseBranch       string
	Repository       string
	PRNumber         int
	ReviewOutputHash string
	TerminalStatus   bool
}

func RunArtifactSnapshot(workspace string, record domain.RunRecord, evaluation EvaluationArtifact, options SnapshotOptions) state.RunArtifactSnapshot {
	if evaluation.SchemaVersion == 0 {
		evaluation.SchemaVersion = CurrentArtifactSchemaVersion
	}
	if evaluation.SchemaSource == "" {
		evaluation.SchemaSource = ArtifactSchemaSourceCurrent
	}
	retryReason := ""
	retryNextState := ""
	if evaluation.ShouldRetry {
		retryReason = evaluation.RootCause
		if retryReason == "" {
			retryReason = evaluation.Outcome
		}
		retryNextState = evaluation.NextAction
	}
	terminalOutcome := ""
	if options.TerminalStatus {
		terminalOutcome = evaluation.Outcome
	}
	return state.RunArtifactSnapshot{
		SchemaVersion:         state.CurrentSchemaVersion,
		ArtifactSchemaVersion: evaluation.SchemaVersion,
		ArtifactSchemaSource:  evaluation.SchemaSource,
		IssueKey:              record.IssueIdentifier,
		IssueID:               record.IssueID,
		Attempt:               1,
		WorkspacePath:         record.Workspace,
		BranchName:            options.BranchName,
		BaseBranch:            options.BaseBranch,
		Status:                record.Status,
		StartedAt:             record.StartedAt,
		UpdatedAt:             record.EndedAt,
		Repository:            options.Repository,
		PRNumber:              options.PRNumber,
		PRURL:                 record.PRURL,
		ReviewStatus:          record.ReviewStatus,
		ReviewPassed:          record.ReviewStatus == "passed",
		ReviewClassification:  record.ReviewClassification,
		ReviewOutputRef:       EvaluationPath(workspace),
		ReviewOutputHash:      options.ReviewOutputHash,
		MergeEligible:         evaluation.MergeEligible,
		FeedbackHash:          record.FeedbackHash,
		FeedbackNextAction:    evaluation.NextAction,
		RetryCount:            evaluation.FeedbackRetryCount,
		RetryBudgetState:      record.BudgetExceeded,
		RetryReason:           retryReason,
		RetryInputHash:        record.FeedbackHash,
		RetryNextState:        retryNextState,
		TerminalOutcome:       terminalOutcome,
		TerminalReason:        evaluation.RootCause,
		RunArtifactRef:        RunRecordPath(workspace),
		EvaluationRef:         EvaluationPath(workspace),
	}
}

var reviewPRNumberPattern = regexp.MustCompile(`(?i)actual .*PR is #([0-9]+)|PR is #([0-9]+)`)

func (m Manager) Repair(path string) (bool, domain.RunRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, domain.RunRecord{}, err
	}
	var record domain.RunRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return false, domain.RunRecord{}, err
	}
	changed := false
	if record.PRURL != "" && record.ReviewFindings != "" {
		if corrected := CorrectedPRURL(record.PRURL, record.ReviewFindings); corrected != "" && corrected != record.PRURL {
			record.PRURL = corrected
			record.ManualRepair = AppendRepairNote(record.ManualRepair, "corrected_pr_url")
			changed = true
		}
	}
	if record.PRURL != "" && m.TerminalStatus(record.Status) && record.Status != "merged" && record.Status != "superseded" {
		state, merged, err := m.PRStateForURL(record.PRURL)
		if err != nil {
			return false, domain.RunRecord{}, err
		}
		if strings.EqualFold(state, "MERGED") || merged {
			MarkManualRepairStatus(&record, "merged", "pr_manually_merged")
			changed = true
		} else if strings.EqualFold(state, "CLOSED") {
			MarkManualRepairStatus(&record, "superseded", "pr_closed_unmerged")
			changed = true
		}
	}
	if !changed {
		return false, record, nil
	}
	if err := writeVersionedRunRecord(path, record); err != nil {
		return false, domain.RunRecord{}, err
	}
	return true, record, nil
}

func MarkManualRepairStatus(record *domain.RunRecord, status, note string) {
	if record.OriginalStatus == "" {
		record.OriginalStatus = record.Status
	}
	record.Status = status
	record.ManualRepair = AppendRepairNote(record.ManualRepair, note)
}

func AppendRepairNote(existing, note string) string {
	if existing == "" {
		return note
	}
	for _, part := range strings.Split(existing, ",") {
		if part == note {
			return existing
		}
	}
	return existing + "," + note
}

func CorrectedPRURL(currentURL, findings string) string {
	matches := reviewPRNumberPattern.FindStringSubmatch(findings)
	if len(matches) == 0 {
		return ""
	}
	number := ""
	for _, match := range matches[1:] {
		if match != "" {
			number = match
			break
		}
	}
	if number == "" {
		return ""
	}
	return regexp.MustCompile(`/pull/[0-9]+`).ReplaceAllString(currentURL, fmt.Sprintf("/pull/%s", number))
}

func FeedbackHash(feedback string) string {
	normalized := strings.TrimSpace(feedback)
	if normalized == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}

func WritePRFeedback(workspace string, prNumber int, feedback string) (string, error) {
	if strings.TrimSpace(feedback) == "" {
		feedback = fmt.Sprintf("# PR #%d feedback\n\nNo review feedback returned by GitHub.\n", prNumber)
	}
	path := FeedbackPath(workspace)
	return path, os.WriteFile(path, []byte(feedback), 0o600)
}

func ReadPRFeedback(workspace string) (string, error) {
	data, err := os.ReadFile(FeedbackPath(workspace))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}

func ParseUsage(output string) *domain.Usage {
	var last *domain.Usage
	ForEachJSONLLine(output, func(line string) {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			return
		}
		var event struct {
			Message *struct {
				Usage *domain.Usage `json:"usage"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(line), &event); err == nil && event.Message != nil && event.Message.Usage != nil {
			candidate := event.Message.Usage
			if candidate.TotalTokens > 0 {
				last = candidate
			}
		}
	})
	return last
}

func ForEachJSONLLine(output string, visit func(string)) {
	reader := bufio.NewReader(strings.NewReader(output))
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			visit(strings.TrimRight(line, "\r\n"))
		}
		if err != nil {
			if err != io.EOF { /* preserve caller logging outside package */
			}
			return
		}
	}
}
