package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	sh "github.com/weskor/agent-machine/internal/shell"
)

const defaultRawAgentOutputLimitBytes = 1024 * 1024

func captureAgentOutput(command, workspace string, env map[string]string, timeout time.Duration, phase string) (string, error) {
	output, err := sh.CaptureEnvWithOutputTimeout(command, workspace, env, false, timeout)
	maybeWriteRawAgentOutput(workspace, phase, output)
	return output, err
}

func maybeWriteRawAgentOutput(workspace, phase, output string) {
	if debugRawOutputFlag() != "1" || output == "" {
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
		prefix := fmt.Sprintf("[am] raw %s output truncated to last %d bytes; enable a larger AM_DEBUG_RAW_OUTPUT_LIMIT_BYTES only for local debugging\n", phase, limit)
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
	phase = sanitizeRawArtifactPhase(phase)
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

func sanitizeRawArtifactPhase(phase string) string {
	phase = strings.TrimSpace(phase)
	if phase == "" {
		return ""
	}
	var builder strings.Builder
	lastUnderscore := false
	for _, r := range phase {
		allowed := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_'
		if allowed {
			builder.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			builder.WriteByte('_')
			lastUnderscore = true
		}
	}
	return strings.Trim(builder.String(), "_")
}

func rawArtifactEvidenceRoot(workspace string) string {
	workspaceRoot := filepath.Dir(workspace)
	if filepath.Base(workspaceRoot) == "workspaces" && filepath.Base(filepath.Dir(workspaceRoot)) == ".am" {
		return filepath.Dir(workspaceRoot)
	}
	return filepath.Join(workspaceRoot, ".am")
}

func rawAgentOutputLimitBytes() int {
	value := debugRawOutputLimit()
	if value == "" {
		return defaultRawAgentOutputLimitBytes
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return defaultRawAgentOutputLimitBytes
	}
	return parsed
}

func debugRawOutputFlag() string {
	return strings.TrimSpace(os.Getenv("AM_DEBUG_RAW_OUTPUT"))
}

func debugRawOutputLimit() string {
	return strings.TrimSpace(os.Getenv("AM_DEBUG_RAW_OUTPUT_LIMIT_BYTES"))
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
