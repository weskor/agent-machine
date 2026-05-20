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

	"github.com/weskor/pi-symphony/internal/domain"
	"github.com/weskor/pi-symphony/internal/state"
)

const (
	RunRecordName  = ".pi-symphony-run.json"
	EvaluationName = ".pi-symphony-evaluation.json"
	FeedbackName   = ".pi-symphony-feedback.md"
)

type EvaluationArtifact struct {
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
	SnapshotFunc   func(workspace string, record domain.RunRecord, evaluation EvaluationArtifact) state.RunArtifactSnapshot
	PRStateForURL  func(prURL string) (string, bool, error)
	TerminalStatus func(status string) bool
}

func RunRecordPath(workspace string) string  { return filepath.Join(workspace, RunRecordName) }
func EvaluationPath(workspace string) string { return filepath.Join(workspace, EvaluationName) }
func FeedbackPath(workspace string) string   { return filepath.Join(workspace, FeedbackName) }

func (m Manager) WriteRunRecord(workspace string, record domain.RunRecord) (string, string, EvaluationArtifact, error) {
	runPath := RunRecordPath(workspace)
	if err := writeJSON(runPath, record); err != nil {
		return runPath, "", EvaluationArtifact{}, err
	}
	evalPath, evaluation, err := m.WriteEvaluation(workspace, record)
	return runPath, evalPath, evaluation, err
}

func (m Manager) WriteEvaluation(workspace string, record domain.RunRecord) (string, EvaluationArtifact, error) {
	evaluation := m.Evaluate(workspace, record)
	path := EvaluationPath(workspace)
	return path, evaluation, writeJSON(path, evaluation)
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
	if evalData, err := os.ReadFile(EvaluationPath(workspace)); err == nil {
		if err := json.Unmarshal(evalData, &evaluation); err != nil {
			return domain.RunRecord{}, EvaluationArtifact{}, time.Time{}, fmt.Errorf("malformed %s: %w", EvaluationName, err)
		}
	} else if err != nil && !os.IsNotExist(err) {
		return domain.RunRecord{}, EvaluationArtifact{}, time.Time{}, fmt.Errorf("read %s: %w", EvaluationName, err)
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

func (m Manager) Snapshot(workspace string, record domain.RunRecord, evaluation EvaluationArtifact) state.RunArtifactSnapshot {
	return m.SnapshotFunc(workspace, record, evaluation)
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
	if err := writeJSON(path, record); err != nil {
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
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "{") {
			continue
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
	}
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
