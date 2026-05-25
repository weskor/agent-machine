package shell

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

var ErrCommandTimeout = errors.New("command timed out")

func Run(command, cwd string) error {
	return RunWithTimeout(command, cwd, 0)
}

func RunWithTimeout(command, cwd string, timeout time.Duration) error {
	return RunWithContextTimeout(context.Background(), command, cwd, timeout)
}

func RunWithContext(ctx context.Context, command, cwd string) error {
	return RunWithContextTimeout(ctx, command, cwd, 0)
}

func RunWithContextTimeout(ctx context.Context, command, cwd string, timeout time.Duration) error {
	log(command)
	if ctx == nil {
		ctx = context.Background()
	}
	var cancel context.CancelFunc
	commandCtx := ctx
	if timeout > 0 {
		commandCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(commandCtx, "sh", "-lc", command)
	cmd.Dir = cwd
	cmd.Env = CommandEnv(nil)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	err := cmd.Run()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if commandCtx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("%w after %s", ErrCommandTimeout, timeout)
	}
	return err
}

func Capture(command, cwd string) (string, error) {
	return CaptureEnvWithOutput(command, cwd, nil, true)
}

func CaptureQuiet(command, cwd string) (string, error) {
	return CaptureEnvWithOutput(command, cwd, nil, false)
}

func CaptureEnv(command, cwd string, env map[string]string) (string, error) {
	return CaptureEnvWithOutput(command, cwd, env, true)
}

func CaptureEnvWithOutput(command, cwd string, env map[string]string, printOutput bool) (string, error) {
	return CaptureEnvWithOutputTimeout(command, cwd, env, printOutput, 0)
}

func CaptureEnvWithOutputTimeout(command, cwd string, env map[string]string, printOutput bool, timeout time.Duration) (string, error) {
	return CaptureEnvWithOutputContextTimeout(context.Background(), command, cwd, env, printOutput, timeout)
}

func CaptureQuietContext(ctx context.Context, command, cwd string) (string, error) {
	return CaptureEnvWithOutputContextTimeout(ctx, command, cwd, nil, false, 0)
}

func CaptureEnvWithOutputContextTimeout(ctx context.Context, command, cwd string, env map[string]string, printOutput bool, timeout time.Duration) (string, error) {
	log(command)
	if ctx == nil {
		ctx = context.Background()
	}
	var cancel context.CancelFunc
	commandCtx := ctx
	if timeout > 0 {
		commandCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(commandCtx, "sh", "-lc", command)
	cmd.Dir = cwd
	cmd.Env = CommandEnv(env)
	output, err := cmd.CombinedOutput()
	text := string(output)
	if printOutput && text != "" {
		fmt.Print(text)
	}
	if ctx.Err() != nil {
		return text, ctx.Err()
	}
	if commandCtx.Err() == context.DeadlineExceeded {
		return text, fmt.Errorf("%w after %s", ErrCommandTimeout, timeout)
	}
	return text, err
}

func CommandEnv(extra map[string]string) []string {
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

func Quote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func log(command string) {
	fmt.Printf("[am] $ %s\n", command)
}
