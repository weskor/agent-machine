package livesmoke

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type IssueRef struct {
	Identifier string `json:"identifier"`
	URL        string `json:"url"`
	Title      string `json:"title,omitempty"`
	Path       string `json:"path,omitempty"`
}

type Report struct {
	StartedAt      time.Time  `json:"started_at"`
	WorkflowPath   string     `json:"workflow_path"`
	SmokeWorkflow  string     `json:"smoke_workflow"`
	WorkspaceRoot  string     `json:"workspace_root"`
	FakeAgent      bool       `json:"fake_agent"`
	ApplyMerge     bool       `json:"apply_merge"`
	Issues         []IssueRef `json:"issues"`
	Commands       []string   `json:"commands"`
	ReportPath     string     `json:"report_path,omitempty"`
	FinalStatusRan bool       `json:"final_status_ran"`
}

func ValidateEnvironment(env map[string]string, applyMerge bool) error {
	if env["LIVE_LINEAR"] != "1" {
		return errors.New("LIVE_LINEAR=1 is required before the live smoke harness may read or mutate Linear")
	}
	if strings.TrimSpace(env["LINEAR_API_KEY"]) == "" {
		return errors.New("LINEAR_API_KEY is required")
	}
	if applyMerge && env["LIVE_SMOKE_APPLY"] != "1" {
		return errors.New("LIVE_SMOKE_APPLY=1 is required with --apply-merge")
	}
	return nil
}

func DisposableIssueDescription(path string) string {
	return fmt.Sprintf(`## Goal
Add a tiny disposable marker for an opt-in Pi Symphony live smoke run.

## Scope
Allowed paths:
- %s

Out of scope:
- Runner behavior changes.
- Workflow configuration changes.
- Production code changes.
- Changes outside the allowed path.

## Requirements
- Create %s if it does not exist.
- The file must state that it was created by the opt-in live smoke harness.
- Do not modify any other file.

## Acceptance Criteria
- Exactly one tracked file is changed: %s.
- Validation passes.
- The issue can move through the normal Symphony handoff flow to Human Review.

## Validation
- mise exec go -- go test ./...
- git diff --check`, path, path, path)
}

func SmokeMarkerContent(identifier, path string) string {
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	title := strings.ReplaceAll(name, "-", " ")
	if strings.TrimSpace(identifier) == "" {
		identifier = "unknown"
	}
	return fmt.Sprintf("# %s\n\nCreated by the opt-in Pi Symphony live smoke harness for %s.\n", titleCase(title), identifier)
}

func PromptPath(args []string) (string, error) {
	for _, arg := range args {
		if strings.HasPrefix(arg, "@") && len(arg) > 1 {
			return strings.TrimPrefix(arg, "@"), nil
		}
	}
	return "", errors.New("missing @prompt path argument")
}

func IssueIdentifierFromPrompt(prompt string) string {
	re := regexp.MustCompile(`(?m)(?:Identifier:\s*|Issue:\s*)(CAG-[0-9]+)\b`)
	match := re.FindStringSubmatch(prompt)
	if len(match) == 2 {
		return match[1]
	}
	return ""
}

func AllowedPathFromPrompt(prompt string) (string, error) {
	lines := strings.Split(prompt, "\n")
	inAllowed := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.EqualFold(trimmed, "Allowed paths:") {
			inAllowed = true
			continue
		}
		if !inAllowed {
			continue
		}
		if strings.HasSuffix(trimmed, ":") && !strings.EqualFold(trimmed, "Allowed paths:") {
			break
		}
		if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") {
			path := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(trimmed, "- "), "* "))
			path = strings.Trim(path, "`")
			if path == "" {
				continue
			}
			if !strings.HasPrefix(path, "docs/smoke/") || strings.Contains(path, "..") || filepath.IsAbs(path) {
				return "", fmt.Errorf("fake smoke agent refuses non-docs/smoke allowed path %q", path)
			}
			return path, nil
		}
	}
	return "", errors.New("no docs/smoke allowed path found in prompt")
}

func titleCase(value string) string {
	words := strings.Fields(value)
	for i, word := range words {
		if word == "" {
			continue
		}
		words[i] = strings.ToUpper(word[:1]) + word[1:]
	}
	return strings.Join(words, " ")
}
