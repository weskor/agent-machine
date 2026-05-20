package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sh "github.com/weskor/pi-symphony/internal/shell"
)

func rawOutputPath(workspace, phase string) string {
	return filepath.Join(filepath.Dir(workspace), ".symphony", "debug", filepath.Base(workspace), phase+"-raw.log")
}

func TestCaptureAgentOutputDoesNotPrintRawOutputByDefault(t *testing.T) {
	t.Setenv("PI_SYMPHONY_DEBUG_RAW_OUTPUT", "")
	t.Setenv("PI_SYMPHONY_DEBUG_RAW_OUTPUT_LIMIT_BYTES", "")

	workspace := t.TempDir()
	stdout := captureStdout(t, func() {
		output, err := captureAgentOutput("printf %s \"$RAW_AGENT_OUTPUT\"", workspace, map[string]string{"RAW_AGENT_OUTPUT": "raw-jsonl-stream"}, 0, "implementation")
		if err != nil {
			t.Fatalf("captureAgentOutput returned error: %v", err)
		}
		if output != "raw-jsonl-stream" {
			t.Fatalf("output = %q", output)
		}
	})
	if strings.Contains(stdout, "raw-jsonl-stream") {
		t.Fatalf("primary log included raw agent output: %q", stdout)
	}
	if _, err := os.Stat(rawOutputPath(workspace, "implementation")); !os.IsNotExist(err) {
		t.Fatalf("debug artifact should not exist by default, stat err=%v", err)
	}
}

func TestCaptureAgentOutputCharacterizesCommandCwdEnvAndTimeout(t *testing.T) {
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
			output, err := captureAgentOutput(tc.command, workspace, tc.env, tc.timeout, "implementation")
			if tc.wantTimeout {
				if !errors.Is(err, sh.ErrCommandTimeout) {
					t.Fatalf("expected timeout, got %v", err)
				}
			} else if err != nil {
				t.Fatalf("captureAgentOutput returned error: %v", err)
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

func TestCaptureAgentOutputWritesCappedDebugArtifactWhenEnabled(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("PI_SYMPHONY_DEBUG_RAW_OUTPUT", "1")
	t.Setenv("PI_SYMPHONY_DEBUG_RAW_OUTPUT_LIMIT_BYTES", "5")
	stdout := captureStdout(t, func() {
		output, err := captureAgentOutput("printf %s \"$RAW_AGENT_OUTPUT\"", workspace, map[string]string{"RAW_AGENT_OUTPUT": "0123456789"}, 0, "review")
		if err != nil {
			t.Fatalf("captureAgentOutput returned error: %v", err)
		}
		if output != "0123456789" {
			t.Fatalf("output = %q", output)
		}
	})
	if strings.Contains(stdout, "0123456789") {
		t.Fatalf("primary log included raw agent output: %q", stdout)
	}
	if !strings.Contains(stdout, "raw review output debug artifact:") {
		t.Fatalf("primary log did not include debug artifact pointer: %q", stdout)
	}
	data, err := os.ReadFile(rawOutputPath(workspace, "review"))
	if err != nil {
		t.Fatalf("read debug artifact: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "truncated to last 5 bytes") || !strings.HasSuffix(text, "56789") {
		t.Fatalf("debug artifact was not capped as expected: %q", text)
	}
}

func TestHandoffRunSummaryIncludesConcisePRReviewAndValidation(t *testing.T) {
	stdout := captureStdout(t, func() {
		logHandoffRunSummary("CAG-86", "https://github.com/weskor/pi-symphony/pull/25", &reviewResult{Status: "passed"}, []string{"make ci passed", "git diff --check passed"})
	})

	for _, expected := range []string{"handoff summary:", "issue=CAG-86", "pr=https://github.com/weskor/pi-symphony/pull/25", "review=passed", "validation=make ci passed | git diff --check passed"} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("expected %q in concise handoff summary %q", expected, stdout)
		}
	}
}

func TestWriteRunRecordLogsConciseFinalSummary(t *testing.T) {
	workspace := t.TempDir()
	record := runRecord{
		IssueIdentifier: "CAG-86",
		Workspace:       workspace,
		WorkspaceRoot:   workspace,
		Status:          "success",
		PRURL:           "https://github.com/weskor/pi-symphony/pull/25",
		ReviewStatus:    "passed",
		DurationMS:      1234,
	}

	stdout := captureStdout(t, func() {
		writeRunRecord(workspace, record)
	})

	for _, expected := range []string{"run summary:", "issue=CAG-86", "status=success", "pr=https://github.com/weskor/pi-symphony/pull/25", "review=passed", "duration_ms=1234", ".pi-symphony-run.json", ".pi-symphony-evaluation.json"} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("expected %q in concise run summary %q", expected, stdout)
		}
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stdout: %v", err)
	}
	os.Stdout = w
	fn()
	if err := w.Close(); err != nil {
		t.Fatalf("close stdout pipe: %v", err)
	}
	os.Stdout = old
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read stdout pipe: %v", err)
	}
	return string(data)
}
