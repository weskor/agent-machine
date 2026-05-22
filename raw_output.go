package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	sh "github.com/weskor/pi-symphony/internal/shell"
)

const defaultRawAgentOutputLimitBytes = 1024 * 1024

func captureAgentOutput(command, workspace string, env map[string]string, timeout time.Duration, phase string) (string, error) {
	output, err := sh.CaptureEnvWithOutputTimeout(command, workspace, env, false, timeout)
	maybeWriteRawAgentOutput(workspace, phase, output)
	return output, err
}

func maybeWriteRawAgentOutput(workspace, phase, output string) {
	if strings.TrimSpace(os.Getenv("PI_SYMPHONY_DEBUG_RAW_OUTPUT")) != "1" || output == "" {
		return
	}
	path := debugRawArtifactPath(workspace, phase)
	if strings.TrimSpace(path) == "" {
		log("failed to resolve raw %s output debug artifact path", phase)
		return
	}
	limit := rawAgentOutputLimitBytes()
	data := []byte(output)
	truncated := false
	if limit > 0 && len(data) > limit {
		data = data[len(data)-limit:]
		truncated = true
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log("failed to create raw %s output debug directory: %v", phase, err)
		return
	}
	if truncated {
		prefix := fmt.Sprintf("[pi-symphony] raw %s output truncated to last %d bytes; enable a larger PI_SYMPHONY_DEBUG_RAW_OUTPUT_LIMIT_BYTES only for local debugging\n", phase, limit)
		data = append([]byte(prefix), data...)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		log("failed to write raw %s output debug artifact: %v", phase, err)
		return
	}
	log("raw %s output debug artifact: %s (%d bytes, capped at %d bytes)", phase, path, len(data), limit)
}

func debugRawArtifactPath(workspace, phase string) string {
	workspace = strings.TrimSpace(workspace)
	phase = strings.TrimSpace(phase)
	if workspace == "" || phase == "" {
		return ""
	}
	cleanWorkspace := filepath.Clean(workspace)
	issue := filepath.Base(cleanWorkspace)
	issue = strings.TrimSpace(issue)
	if issue == "" {
		return ""
	}
	return filepath.Join(rawArtifactEvidenceRoot(cleanWorkspace), "debug", issue, phase+"-raw.log")
}

func rawArtifactEvidenceRoot(workspace string) string {
	workspaceRoot := filepath.Dir(workspace)
	if filepath.Base(workspaceRoot) == "workspaces" && filepath.Base(filepath.Dir(workspaceRoot)) == ".symphony" {
		return filepath.Dir(workspaceRoot)
	}
	return filepath.Join(workspaceRoot, ".symphony")
}

func rawAgentOutputLimitBytes() int {
	value := strings.TrimSpace(os.Getenv("PI_SYMPHONY_DEBUG_RAW_OUTPUT_LIMIT_BYTES"))
	if value == "" {
		return defaultRawAgentOutputLimitBytes
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return defaultRawAgentOutputLimitBytes
	}
	return parsed
}

func logHandoffRunSummary(issueIdentifier, prURL string, review *reviewResult, validation []string) {
	log("handoff summary: issue=%s pr=%s review=%s validation=%s", emptyAsUnknown(issueIdentifier), emptyAsUnknown(prURL), reviewStatusSummary(review), validationSummary(validation))
}

func reviewStatusSummary(review *reviewResult) string {
	if review == nil || strings.TrimSpace(review.Status) == "" {
		return "not_configured"
	}
	return review.Status
}

func validationSummary(lines []string) string {
	var cleaned []string
	for _, line := range lines {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			cleaned = append(cleaned, trimmed)
		}
	}
	if len(cleaned) == 0 {
		return "not_reported"
	}
	const maxValidationSummaryLines = 3
	if len(cleaned) > maxValidationSummaryLines {
		cleaned = append(cleaned[:maxValidationSummaryLines], fmt.Sprintf("...+%d more", len(cleaned)-maxValidationSummaryLines))
	}
	return strings.Join(cleaned, " | ")
}
