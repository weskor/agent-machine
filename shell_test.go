package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCommandEnvDisablesPagers(t *testing.T) {
	env := strings.Join(commandEnv(nil), "\n")

	for _, want := range []string{
		"GIT_PAGER=cat",
		"GH_PAGER=cat",
		"PAGER=cat",
		"GIT_TERMINAL_PROMPT=0",
		"CI=1",
	} {
		if !strings.Contains(env, want) {
			t.Fatalf("commandEnv missing %s in %q", want, env)
		}
	}
}

func TestShellCaptureTimeout(t *testing.T) {
	_, err := shellCaptureEnvWithOutputTimeout("sleep 1", "", nil, false, 10*time.Millisecond)
	if !errors.Is(err, errCommandTimeout) {
		t.Fatalf("expected timeout error, got %v", err)
	}
}

func TestCommandEnvAllowsExtraOverrides(t *testing.T) {
	env := commandEnv(map[string]string{"PAGER": "custom", "EXTRA": "value"})
	joined := strings.Join(env, "\n")

	if !strings.Contains(joined, "EXTRA=value") {
		t.Fatalf("commandEnv missing extra env in %q", joined)
	}
	lastPager := ""
	for _, item := range env {
		if strings.HasPrefix(item, "PAGER=") {
			lastPager = item
		}
	}
	if lastPager != "PAGER=custom" {
		t.Fatalf("expected extra PAGER override to win, got %q", lastPager)
	}
}
