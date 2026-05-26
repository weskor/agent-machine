package runledger

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	SchemaVersion = 1
	FileName      = "events.jsonl"
)

var ErrNotFound = errors.New("run ledger not found")

type Event struct {
	SchemaVersion        int       `json:"schema_version"`
	IssueIdentifier      string    `json:"issue_identifier"`
	IssueTitle           string    `json:"issue_title,omitempty"`
	Phase                string    `json:"phase"`
	Status               string    `json:"status,omitempty"`
	Outcome              string    `json:"outcome,omitempty"`
	ChecksStatus         string    `json:"checks_status,omitempty"`
	ReviewStatus         string    `json:"review_status,omitempty"`
	ReviewClassification string    `json:"review_classification,omitempty"`
	PRURL                string    `json:"pr_url,omitempty"`
	Branch               string    `json:"branch,omitempty"`
	ExpectedBranch       string    `json:"expected_branch,omitempty"`
	Workspace            string    `json:"workspace,omitempty"`
	StartedAt            time.Time `json:"started_at,omitempty"`
	ObservedAt           time.Time `json:"observed_at"`
	DurationMS           int64     `json:"duration_ms,omitempty"`
	NextAction           string    `json:"next_action,omitempty"`
	Error                string    `json:"error,omitempty"`
	RunRecordPath        string    `json:"run_record_path,omitempty"`
	EvaluationPath       string    `json:"evaluation_path,omitempty"`
	ProgressPath         string    `json:"progress_path,omitempty"`
	PRHandoffPayloadPath string    `json:"pr_handoff_payload_path,omitempty"`
	ReviewPayloadPath    string    `json:"review_payload_path,omitempty"`
	HandoffPayloadPath   string    `json:"handoff_payload_path,omitempty"`
}

func Root(workspaceRoot string) string {
	if strings.TrimSpace(workspaceRoot) == "" {
		return ""
	}
	clean := filepath.Clean(workspaceRoot)
	if filepath.Base(clean) == "workspaces" && filepath.Base(filepath.Dir(clean)) == ".am" {
		return filepath.Join(filepath.Dir(clean), "state", "run-ledger")
	}
	return filepath.Join(clean, "state", "run-ledger")
}

func Path(workspaceRoot, issueIdentifier string) (string, error) {
	root := Root(workspaceRoot)
	if root == "" {
		return "", errors.New("workspace root is required")
	}
	issue := strings.TrimSpace(issueIdentifier)
	if issue == "" || filepath.Base(filepath.Clean(issue)) != issue {
		return "", fmt.Errorf("issue identifier %q is not a safe run ledger name", issueIdentifier)
	}
	return filepath.Join(root, issue, FileName), nil
}

func Append(workspaceRoot string, event Event) error {
	path, err := Path(workspaceRoot, event.IssueIdentifier)
	if err != nil {
		return err
	}
	if event.SchemaVersion == 0 {
		event.SchemaVersion = SchemaVersion
	}
	if event.ObservedAt.IsZero() {
		event.ObservedAt = time.Now().UTC()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	return file.Sync()
}

func Read(workspaceRoot, issueIdentifier string) ([]Event, string, error) {
	path, err := Path(workspaceRoot, issueIdentifier)
	if err != nil {
		return nil, "", err
	}
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, path, fmt.Errorf("%w for %s at %s", ErrNotFound, issueIdentifier, path)
		}
		return nil, path, err
	}
	defer file.Close()
	var events []Event
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return nil, path, fmt.Errorf("malformed run ledger event in %s: %w", path, err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, path, err
	}
	return events, path, nil
}

func Format(issueIdentifier string, events []Event, path string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "issue=%s ledger=%s events=%d\n", emptyAsUnknown(issueIdentifier), path, len(events))
	for _, event := range events {
		parts := []string{event.ObservedAt.Format(time.RFC3339)}
		parts = append(parts, "phase="+emptyAsUnknown(event.Phase))
		if event.Status != "" {
			parts = append(parts, "status="+event.Status)
		}
		if event.Outcome != "" {
			parts = append(parts, "outcome="+event.Outcome)
		}
		if event.ChecksStatus != "" {
			parts = append(parts, "checks="+event.ChecksStatus)
		}
		if event.PRURL != "" {
			parts = append(parts, "pr="+event.PRURL)
		}
		if event.ReviewStatus != "" {
			parts = append(parts, "review="+event.ReviewStatus)
		}
		if event.ReviewClassification != "" {
			parts = append(parts, "classification="+event.ReviewClassification)
		}
		if event.NextAction != "" {
			parts = append(parts, "next="+event.NextAction)
		}
		if event.Error != "" {
			parts = append(parts, "error="+event.Error)
		}
		if event.ProgressPath != "" {
			parts = append(parts, "progress="+event.ProgressPath)
		}
		if event.RunRecordPath != "" {
			parts = append(parts, "run_record="+event.RunRecordPath)
		}
		if event.EvaluationPath != "" {
			parts = append(parts, "evaluation="+event.EvaluationPath)
		}
		b.WriteString(strings.Join(parts, " "))
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func emptyAsUnknown(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return value
}
