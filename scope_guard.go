package main

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	sh "github.com/weskor/agent-machine/internal/shell"
)

type scopeGuardResult struct {
	Checked    bool
	Warnings   []string
	Violations []string
}

func (r scopeGuardResult) Blocks() bool {
	return len(r.Violations) > 0
}

func (r scopeGuardResult) Summary() string {
	parts := append([]string{}, r.Violations...)
	parts = append(parts, r.Warnings...)
	return strings.Join(parts, "; ")
}

func checkScopeGuardContext(ctx context.Context, description, workspace, baseBranch string) (scopeGuardResult, error) {
	changed, err := scopeGuardChangedFilesContext(ctx, workspace, baseBranch)
	if err != nil {
		return scopeGuardResult{}, err
	}
	return evaluateScopeGuard(description, changed), nil
}

func scopeGuardChangedFilesContext(ctx context.Context, workspace, baseBranch string) ([]string, error) {
	base := strings.TrimSpace(baseBranch)
	if base == "" {
		base = "main"
	}
	output, err := sh.CaptureQuietContext(ctx, fmt.Sprintf("git diff --name-only --diff-filter=ACMRT %s...HEAD", sh.Quote("origin/"+base)), workspace)
	if err != nil {
		return nil, fmt.Errorf("scope guard changed-file lookup failed: %w", err)
	}
	return nonEmptyLines(output), nil
}

func evaluateScopeGuard(description string, changedFiles []string) scopeGuardResult {
	contract := parseScopeContract(description)
	result := scopeGuardResult{Checked: contract.HasContract}
	if len(changedFiles) == 0 {
		return result
	}
	if !contract.HasContract {
		result.Warnings = append(result.Warnings, "scope guard warning: no machine-readable Allowed paths or Out of scope paths contract found")
		return result
	}
	if len(contract.Allowed) == 0 {
		result.Warnings = append(result.Warnings, "scope guard warning: no machine-readable Allowed paths entries found")
	}
	for _, file := range changedFiles {
		clean := path.Clean(strings.TrimSpace(file))
		if clean == "." || clean == "" {
			continue
		}
		for _, rule := range contract.OutOfScope {
			if rule.Matches(clean) {
				result.Violations = append(result.Violations, fmt.Sprintf("scope guard violation: %s matches Out of scope path %s", clean, rule.Pattern))
				break
			}
		}
		if len(contract.Allowed) == 0 {
			continue
		}
		allowed := false
		for _, rule := range contract.Allowed {
			if rule.Matches(clean) {
				allowed = true
				break
			}
		}
		if !allowed {
			result.Violations = append(result.Violations, fmt.Sprintf("scope guard violation: %s is outside Allowed paths", clean))
		}
	}
	sort.Strings(result.Violations)
	return result
}

type scopeContract struct {
	HasContract bool
	Allowed     []scopePathRule
	OutOfScope  []scopePathRule
}

type scopePathRule struct {
	Pattern string
}

func (r scopePathRule) Matches(file string) bool {
	pattern := strings.TrimSpace(r.Pattern)
	if pattern == "" {
		return false
	}
	if strings.HasSuffix(pattern, "/") {
		return strings.HasPrefix(file, pattern)
	}
	if strings.HasSuffix(pattern, "/*") {
		return strings.HasPrefix(file, strings.TrimSuffix(pattern, "*"))
	}
	if strings.ContainsAny(pattern, "*?[{") {
		matched, err := path.Match(pattern, file)
		return err == nil && matched
	}
	return file == pattern
}

func parseScopeContract(description string) scopeContract {
	var contract scopeContract
	section := ""
	for _, line := range strings.Split(description, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(strings.Trim(trimmed, "# :"))
		switch lower {
		case "allowed paths", "allowed path":
			section = "allowed"
			contract.HasContract = true
			continue
		case "out of scope", "out-of-scope", "out of scope paths", "out-of-scope paths", "out of scope path", "out-of-scope path":
			section = "out"
			contract.HasContract = true
			continue
		}
		if strings.HasPrefix(trimmed, "##") && !strings.Contains(lower, "allowed") && !strings.Contains(lower, "scope") {
			section = ""
			continue
		}
		if section == "" || !(strings.HasPrefix(trimmed, "-") || strings.HasPrefix(trimmed, "*")) {
			continue
		}
		for _, pattern := range pathPatternsFromScopeBullet(trimmed) {
			rule := scopePathRule{Pattern: pattern}
			if section == "allowed" {
				contract.Allowed = append(contract.Allowed, rule)
			} else {
				contract.OutOfScope = append(contract.OutOfScope, rule)
			}
		}
	}
	return contract
}

var codeSpanPattern = regexp.MustCompile("`([^`]+)`")

func pathPatternsFromScopeBullet(line string) []string {
	line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "-* "))
	if strings.EqualFold(line, "none") || strings.HasPrefix(strings.ToLower(line), "no ") {
		return nil
	}
	var candidates []string
	for _, match := range codeSpanPattern.FindAllStringSubmatch(line, -1) {
		candidates = append(candidates, match[1])
	}
	if len(candidates) == 0 {
		fields := strings.Fields(line)
		if len(fields) > 0 {
			candidates = append(candidates, fields[0])
		}
	}
	var patterns []string
	for _, candidate := range candidates {
		candidate = strings.Trim(candidate, " ,.;:()[]")
		candidate = strings.TrimPrefix(candidate, "./")
		candidate = strings.ReplaceAll(candidate, "\\", "/")
		if isMachineReadableScopePath(candidate) {
			patterns = append(patterns, path.Clean(candidate))
		}
	}
	return patterns
}

func isMachineReadableScopePath(value string) bool {
	if value == "" || strings.Contains(value, " ") || strings.HasPrefix(value, "http") {
		return false
	}
	return strings.Contains(value, "/") || strings.Contains(value, ".") || strings.ContainsAny(value, "*?[")
}

func nonEmptyLines(output string) []string {
	var lines []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}
