package rawoutput

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	sh "github.com/weskor/agent-machine/internal/shell"
)

const DefaultLimitBytes = 1024 * 1024

type Logger func(format string, args ...any)

func Capture(ctx context.Context, command, workspace string, env map[string]string, timeout time.Duration, phase string, logger Logger) (string, error) {
	output, err := sh.CaptureEnvWithOutputContextTimeout(ctx, command, workspace, env, false, timeout)
	MaybeWrite(workspace, phase, output, logger)
	return output, err
}

func MaybeWrite(workspace, phase, output string, logger Logger) {
	if debugOutputFlag() != "1" || output == "" {
		return
	}
	path := ArtifactPath(workspace, phase)
	if strings.TrimSpace(path) == "" {
		logf(logger, "failed to resolve raw %s output debug artifact path", phase)
		return
	}
	limit := outputLimitBytes()
	data := []byte(output)
	truncated := false
	if limit > 0 && len(data) > limit {
		data = data[len(data)-limit:]
		truncated = true
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		logf(logger, "failed to create raw %s output debug directory: %v", phase, err)
		return
	}
	if truncated {
		prefix := fmt.Sprintf("[am] raw %s output truncated to last %d bytes; enable a larger AM_DEBUG_RAW_OUTPUT_LIMIT_BYTES only for local debugging\n", phase, limit)
		data = append([]byte(prefix), data...)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		logf(logger, "failed to write raw %s output debug artifact: %v", phase, err)
		return
	}
	logf(logger, "raw %s output debug artifact: %s (%d bytes, capped at %d bytes)", phase, path, len(data), limit)
}

func ArtifactPath(workspace, phase string) string {
	workspace = strings.TrimSpace(workspace)
	phase = sanitizePhase(phase)
	if workspace == "" || phase == "" {
		return ""
	}
	cleanWorkspace := filepath.Clean(workspace)
	issue := strings.TrimSpace(filepath.Base(cleanWorkspace))
	if issue == "" {
		return ""
	}
	return filepath.Join(evidenceRoot(cleanWorkspace), "debug", issue, phase+"-raw.log")
}

func sanitizePhase(phase string) string {
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

func evidenceRoot(workspace string) string {
	workspaceRoot := filepath.Dir(workspace)
	if filepath.Base(workspaceRoot) == "workspaces" && filepath.Base(filepath.Dir(workspaceRoot)) == ".am" {
		return filepath.Dir(workspaceRoot)
	}
	return filepath.Join(workspaceRoot, ".am")
}

func outputLimitBytes() int {
	value := debugOutputLimit()
	if value == "" {
		return DefaultLimitBytes
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return DefaultLimitBytes
	}
	return parsed
}

func debugOutputFlag() string {
	return strings.TrimSpace(os.Getenv("AM_DEBUG_RAW_OUTPUT"))
}

func debugOutputLimit() string {
	return strings.TrimSpace(os.Getenv("AM_DEBUG_RAW_OUTPUT_LIMIT_BYTES"))
}

func logf(logger Logger, format string, args ...any) {
	if logger != nil {
		logger(format, args...)
	}
}
