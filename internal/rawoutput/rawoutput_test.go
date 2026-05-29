package rawoutput

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sh "github.com/weskor/agent-machine/internal/shell"
)

func TestArtifactPathUsesRepoAgentMachineRootForStandardWorkspaceLayout(t *testing.T) {
	repo := t.TempDir()
	workspace := filepath.Join(repo, ".am", "workspaces", "CAG-123")

	got := ArtifactPath(workspace, "implementation")
	want := filepath.Join(repo, ".am", "debug", "CAG-123", "implementation-raw.log")
	if got != want {
		t.Fatalf("ArtifactPath() = %q, want %q", got, want)
	}
}

func TestArtifactPathPreservesParentRootFallbackForNonstandardWorkspaceLayout(t *testing.T) {
	repo := t.TempDir()
	workspace := filepath.Join(repo, "runner-workspaces", "CAG-123")

	got := ArtifactPath(workspace, "review")
	want := filepath.Join(repo, "runner-workspaces", ".am", "debug", "CAG-123", "review-raw.log")
	if got != want {
		t.Fatalf("ArtifactPath() = %q, want %q", got, want)
	}
}

func TestArtifactPathSanitizesPhase(t *testing.T) {
	repo := t.TempDir()
	workspace := filepath.Join(repo, ".am", "workspaces", "CAG-123")
	got := ArtifactPath(workspace, "../review/raw")
	want := filepath.Join(repo, ".am", "debug", "CAG-123", "review_raw-raw.log")
	if got != want {
		t.Fatalf("ArtifactPath() = %q, want sanitized path %q", got, want)
	}
}

func TestCaptureDoesNotLogRawOutputByDefault(t *testing.T) {
	t.Setenv("AM_DEBUG_RAW_OUTPUT", "")
	t.Setenv("AM_DEBUG_RAW_OUTPUT_LIMIT_BYTES", "")

	workspace := t.TempDir()
	logs := captureLogs(func(logger Logger) {
		output, err := Capture(context.Background(), "printf %s \"$RAW_AGENT_OUTPUT\"", workspace, map[string]string{"RAW_AGENT_OUTPUT": "raw-jsonl-stream"}, 0, "implementation", logger)
		if err != nil {
			t.Fatalf("Capture returned error: %v", err)
		}
		if output != "raw-jsonl-stream" {
			t.Fatalf("output = %q", output)
		}
	})
	if strings.Contains(logs, "raw-jsonl-stream") {
		t.Fatalf("primary log included raw agent output: %q", logs)
	}
	if _, err := os.Stat(ArtifactPath(workspace, "implementation")); !os.IsNotExist(err) {
		t.Fatalf("debug artifact should not exist by default, stat err=%v", err)
	}
}

func TestCaptureCharacterizesCommandCwdEnvAndTimeout(t *testing.T) {
	for _, tc := range []struct {
		name        string
		command     string
		env         map[string]string
		timeout     time.Duration
		wantOutput  []string
		wantTimeout bool
	}{
		{
			name:       "runs through sh in workspace with merged environment",
			command:    `printf 'pwd=%s env=%s ci=%s pager=%s' "$PWD" "$RUNTIME_ENV" "$CI" "$GIT_PAGER"`,
			env:        map[string]string{"RUNTIME_ENV": "from-test"},
			wantOutput: []string{"pwd=WORKSPACE", "env=from-test", "ci=1", "pager=cat"},
		},
		{
			name:        "returns timeout error",
			command:     `sleep 1`,
			timeout:     time.Millisecond,
			wantTimeout: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			output, err := Capture(context.Background(), tc.command, workspace, tc.env, tc.timeout, "implementation", nil)
			if tc.wantTimeout {
				if !errors.Is(err, sh.ErrCommandTimeout) {
					t.Fatalf("expected timeout, got %v", err)
				}
			} else if err != nil {
				t.Fatalf("Capture returned error: %v", err)
			}
			for _, want := range tc.wantOutput {
				want = strings.ReplaceAll(want, "WORKSPACE", workspace)
				if !strings.Contains(output, want) {
					t.Fatalf("output %q missing %q", output, want)
				}
			}
		})
	}
}

func TestCaptureWritesCappedDebugArtifactWhenEnabled(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("AM_DEBUG_RAW_OUTPUT", "1")
	t.Setenv("AM_DEBUG_RAW_OUTPUT_LIMIT_BYTES", "5")
	logs := captureLogs(func(logger Logger) {
		output, err := Capture(context.Background(), "printf %s \"$RAW_AGENT_OUTPUT\"", workspace, map[string]string{"RAW_AGENT_OUTPUT": "0123456789"}, 0, "review", logger)
		if err != nil {
			t.Fatalf("Capture returned error: %v", err)
		}
		if output != "0123456789" {
			t.Fatalf("output = %q", output)
		}
	})
	if strings.Contains(logs, "0123456789") {
		t.Fatalf("primary log included raw agent output: %q", logs)
	}
	if !strings.Contains(logs, "raw review output debug artifact:") {
		t.Fatalf("primary log did not include debug artifact pointer: %q", logs)
	}
	data, err := os.ReadFile(ArtifactPath(workspace, "review"))
	if err != nil {
		t.Fatalf("read debug artifact: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "truncated to last 5 bytes") || !strings.HasSuffix(text, "56789") {
		t.Fatalf("debug artifact was not capped as expected: %q", text)
	}
}

func captureLogs(fn func(Logger)) string {
	var logs strings.Builder
	logger := func(format string, args ...any) {
		logs.WriteString(fmt.Sprintf(format, args...))
		logs.WriteByte('\n')
	}
	fn(logger)
	return logs.String()
}
