package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

var errCommandTimeout = errors.New("command timed out")

var defaultGitHubCommandTimeout = 2 * time.Minute

func isEmpty(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) == 0
}

func shell(command, cwd string) error {
	return shellWithTimeout(command, cwd, 0)
}

func shellWithTimeout(command, cwd string, timeout time.Duration) error {
	log("$ %s", command)
	ctx := context.Background()
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, "sh", "-lc", command)
	cmd.Dir = cwd
	cmd.Env = commandEnv(nil)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("%w after %s", errCommandTimeout, timeout)
	}
	return err
}

func shellCapture(command, cwd string) (string, error) {
	return shellCaptureEnvWithOutput(command, cwd, nil, true)
}

func shellCaptureQuiet(command, cwd string) (string, error) {
	return shellCaptureEnvWithOutput(command, cwd, nil, false)
}

func shellCaptureEnv(command, cwd string, env map[string]string) (string, error) {
	return shellCaptureEnvWithOutput(command, cwd, env, true)
}

func shellCaptureEnvWithOutput(command, cwd string, env map[string]string, printOutput bool) (string, error) {
	return shellCaptureEnvWithOutputTimeout(command, cwd, env, printOutput, 0)
}

func shellCaptureEnvWithOutputTimeout(command, cwd string, env map[string]string, printOutput bool, timeout time.Duration) (string, error) {
	log("$ %s", command)
	ctx := context.Background()
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, "sh", "-lc", command)
	cmd.Dir = cwd
	cmd.Env = commandEnv(env)
	output, err := cmd.CombinedOutput()
	text := string(output)
	if printOutput && text != "" {
		fmt.Print(text)
	}
	if ctx.Err() == context.DeadlineExceeded {
		return text, fmt.Errorf("%w after %s", errCommandTimeout, timeout)
	}
	return text, err
}

func commandEnv(extra map[string]string) []string {
	env := os.Environ()
	// The runner is often executed as a daemon with an attached TTY. Force common
	// CLIs to behave non-interactively so commands such as `git diff --check` and
	// `gh pr view` cannot block forever inside less or another pager.
	defaults := map[string]string{
		"GIT_PAGER":           "cat",
		"GH_PAGER":            "cat",
		"PAGER":               "cat",
		"LESS":                "-F -X",
		"CI":                  "1",
		"GIT_TERMINAL_PROMPT": "0",
	}
	for key, value := range defaults {
		env = append(env, key+"="+value)
	}
	for key, value := range extra {
		env = append(env, key+"="+value)
	}
	return env
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func log(format string, args ...any) {
	fmt.Printf("[pi-symphony] "+format+"\n", args...)
}
