package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCaptureAgentOutputDoesNotPrintRawOutputByDefault(t *testing.T) {
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
	if _, err := os.Stat(filepath.Join(workspace, ".pi-symphony-debug", "implementation-raw.log")); !os.IsNotExist(err) {
		t.Fatalf("debug artifact should not exist by default, stat err=%v", err)
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
	data, err := os.ReadFile(filepath.Join(workspace, ".pi-symphony-debug", "review-raw.log"))
	if err != nil {
		t.Fatalf("read debug artifact: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "truncated to last 5 bytes") || !strings.HasSuffix(text, "56789") {
		t.Fatalf("debug artifact was not capped as expected: %q", text)
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
