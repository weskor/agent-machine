package doctor

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/weskor/agent-machine/internal/agentruntime"
)

const (
	StatusOK   = "ok"
	StatusWarn = "warn"
	StatusFail = "fail"
)

type Config struct {
	ConfigPath         string
	ProjectSlug        string
	LinearAPIKey       string
	RepositoryProvider string
	RepositoryRemote   string
	WorkspaceRoot      string
	PromptPath         string
	RuntimeProvider    string
	RuntimeCommand     string
	ReviewCommand      string
	GitLabProject      string
}

type Runtime interface {
	Preflight(context.Context, agentruntime.PreflightInput) (agentruntime.PreflightResult, error)
}

type RuntimeFactory func(string) (Runtime, error)
type EnvLookup func(string) (string, bool)

type Check struct {
	Name    string
	Status  string
	Message string
}

type Report struct {
	Checks []Check
}

func Evaluate(ctx context.Context, config Config, runtimeFactory RuntimeFactory, lookup EnvLookup) Report {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	report := Report{}
	report.add(fileCheck("config", config.ConfigPath))
	report.add(promptCheck(config.PromptPath))
	report.add(workspaceRootCheck(config.WorkspaceRoot))
	report.add(requiredValueCheck("linear api key", config.LinearAPIKey, "LINEAR_API_KEY is configured", "LINEAR_API_KEY is required for Linear-backed runner modes"))
	report.add(codeHostCredentialCheck(config, lookup))
	report.add(runtimeChecks(ctx, config, runtimeFactory)...)
	return report
}

func (r Report) Failed() int {
	count := 0
	for _, check := range r.Checks {
		if check.Status == StatusFail {
			count++
		}
	}
	return count
}

func (r Report) Err() error {
	if failed := r.Failed(); failed > 0 {
		return fmt.Errorf("doctor found %d failed check(s)", failed)
	}
	return nil
}

func (r Report) Write(w io.Writer) error {
	if _, err := fmt.Fprintln(w, "Agent Machine doctor"); err != nil {
		return err
	}
	for _, check := range r.Checks {
		if _, err := fmt.Fprintf(w, "[%s] %s: %s\n", check.Status, check.Name, check.Message); err != nil {
			return err
		}
	}
	return nil
}

func (r *Report) add(checks ...Check) {
	r.Checks = append(r.Checks, checks...)
}

func fileCheck(name, path string) Check {
	if strings.TrimSpace(path) == "" {
		return Check{Name: name, Status: StatusFail, Message: "path is empty"}
	}
	info, err := os.Stat(path)
	if err != nil {
		return Check{Name: name, Status: StatusFail, Message: fmt.Sprintf("%s is not readable: %v", path, err)}
	}
	if info.IsDir() {
		return Check{Name: name, Status: StatusFail, Message: fmt.Sprintf("%s is a directory, not a file", path)}
	}
	return Check{Name: name, Status: StatusOK, Message: fmt.Sprintf("%s is readable", path)}
}

func promptCheck(path string) Check {
	check := fileCheck("agent prompt", path)
	if check.Status == StatusFail && strings.TrimSpace(path) == "" {
		check.Message = "agent.prompt_path resolved to an empty path"
	}
	return check
}

func workspaceRootCheck(root string) Check {
	if strings.TrimSpace(root) == "" {
		return Check{Name: "workspace root", Status: StatusFail, Message: "workspace.root is required"}
	}
	info, err := os.Stat(root)
	if err == nil {
		if !info.IsDir() {
			return Check{Name: "workspace root", Status: StatusFail, Message: fmt.Sprintf("%s exists but is not a directory", root)}
		}
		return Check{Name: "workspace root", Status: StatusOK, Message: fmt.Sprintf("%s exists", root)}
	}
	if !os.IsNotExist(err) {
		return Check{Name: "workspace root", Status: StatusFail, Message: fmt.Sprintf("%s cannot be inspected: %v", root, err)}
	}
	ancestor, ancestorErr := nearestExistingDir(root)
	if ancestorErr != nil {
		return Check{Name: "workspace root", Status: StatusFail, Message: ancestorErr.Error()}
	}
	return Check{Name: "workspace root", Status: StatusOK, Message: fmt.Sprintf("%s can be created under existing ancestor %s", root, ancestor)}
}

func requiredValueCheck(name, value, okMessage, failMessage string) Check {
	if strings.TrimSpace(value) == "" {
		return Check{Name: name, Status: StatusFail, Message: failMessage}
	}
	return Check{Name: name, Status: StatusOK, Message: okMessage}
}

func codeHostCredentialCheck(config Config, lookup EnvLookup) Check {
	provider := strings.ToLower(strings.TrimSpace(config.RepositoryProvider))
	switch provider {
	case "", "github":
		if envPresent(lookup, "GITHUB_TOKEN", "GH_TOKEN") {
			return Check{Name: "code host credentials", Status: StatusOK, Message: "GitHub token environment is configured"}
		}
		if allEnvPresent(lookup, "GITHUB_APP_ID", "GITHUB_APP_INSTALLATION_ID", "GITHUB_APP_PRIVATE_KEY_PATH") {
			return Check{Name: "code host credentials", Status: StatusOK, Message: "GitHub App environment is configured"}
		}
		return Check{Name: "code host credentials", Status: StatusFail, Message: "set GITHUB_TOKEN/GH_TOKEN or GitHub App environment before handoff-capable modes"}
	case "gitlab":
		if envPresent(lookup, "GITLAB_TOKEN", "GL_TOKEN") {
			return Check{Name: "code host credentials", Status: StatusOK, Message: "GitLab token environment is configured"}
		}
		return Check{Name: "code host credentials", Status: StatusFail, Message: "set GITLAB_TOKEN or GL_TOKEN before handoff-capable modes"}
	default:
		return Check{Name: "code host credentials", Status: StatusWarn, Message: fmt.Sprintf("unknown repository.provider %q; credential check skipped", config.RepositoryProvider)}
	}
}

func envPresent(lookup EnvLookup, names ...string) bool {
	for _, name := range names {
		value, ok := lookup(name)
		if ok && strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func allEnvPresent(lookup EnvLookup, names ...string) bool {
	for _, name := range names {
		value, ok := lookup(name)
		if !ok || strings.TrimSpace(value) == "" {
			return false
		}
	}
	return true
}

func nearestExistingDir(path string) (string, error) {
	for current := filepath.Clean(path); current != "." && current != string(os.PathSeparator); current = filepath.Dir(current) {
		info, err := os.Stat(current)
		if err == nil {
			if info.IsDir() {
				return current, nil
			}
			return "", fmt.Errorf("%s does not exist and ancestor %s is not a directory", path, current)
		}
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("%s cannot be inspected through ancestor %s: %v", path, current, err)
		}
	}
	if filepath.IsAbs(path) {
		return string(os.PathSeparator), nil
	}
	return ".", nil
}

func runtimeChecks(ctx context.Context, config Config, runtimeFactory RuntimeFactory) []Check {
	if runtimeFactory == nil {
		return []Check{{Name: "runtime provider", Status: StatusFail, Message: "runtime factory is not configured"}}
	}
	runtime, err := runtimeFactory(config.RuntimeProvider)
	if err != nil {
		return []Check{{Name: "runtime provider", Status: StatusFail, Message: err.Error()}}
	}
	result, err := runtime.Preflight(ctx, agentruntime.PreflightInput{ImplementationCommand: config.RuntimeCommand, ReviewCommand: config.ReviewCommand})
	checks := []Check{{Name: "runtime provider", Status: StatusOK, Message: fmt.Sprintf("%s is supported", result.Provider)}}
	for _, preflight := range result.Checks {
		status := StatusOK
		if !preflight.OK {
			status = StatusFail
		}
		message := preflight.Message
		if strings.TrimSpace(message) == "" {
			message = preflight.Command
		}
		checks = append(checks, Check{Name: "runtime " + preflight.Name, Status: status, Message: message})
	}
	if err != nil && len(result.Checks) == 0 {
		checks = append(checks, Check{Name: "runtime preflight", Status: StatusFail, Message: err.Error()})
	}
	return checks
}
