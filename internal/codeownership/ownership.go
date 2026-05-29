package codeownership

import (
	"fmt"
	"strings"

	"github.com/weskor/agent-machine/internal/codehost"
)

type Policy interface {
	AllowsPRAuthor(login string) bool
	ExpectedPRAuthorSource() string
}

type Input struct {
	Provider                  string
	GitHubPRAuthorOverride    string
	GitHubAppExpectedLogins   []string
	GitHubAppSource           string
	GitLabPRAuthorOverride    string
	GitLabEnvPRAuthorOverride string
}

type policy struct {
	logins []string
	source string
}

func NewPolicy(input Input) Policy {
	if strings.EqualFold(strings.TrimSpace(input.Provider), codehost.ProviderGitLab) {
		if override := splitAuthorLogins(input.GitLabPRAuthorOverride); len(override) > 0 {
			return policy{logins: override, source: "config gitlab.pr_author_override"}
		}
		if override := splitAuthorLogins(input.GitLabEnvPRAuthorOverride); len(override) > 0 {
			return policy{logins: override, source: "GITLAB_PR_AUTHOR_OVERRIDE"}
		}
		return policy{source: "missing config gitlab.pr_author_override or GITLAB_PR_AUTHOR_OVERRIDE"}
	}
	if override := splitAuthorLogins(input.GitHubPRAuthorOverride); len(override) > 0 {
		return policy{logins: override, source: "config github.pr_author_override"}
	}
	if len(input.GitHubAppExpectedLogins) == 0 {
		source := strings.TrimSpace(input.GitHubAppSource)
		if source == "" {
			source = "config github.app_slug or GITHUB_APP_SLUG"
		}
		return policy{source: source}
	}
	return policy{logins: append([]string{}, input.GitHubAppExpectedLogins...), source: input.GitHubAppSource}
}

func splitAuthorLogins(value string) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' })
	var out []string
	for _, field := range fields {
		if trimmed := strings.TrimSpace(field); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func (p policy) AllowsPRAuthor(login string) bool {
	trimmed := strings.TrimSpace(login)
	if trimmed == "" || len(p.logins) == 0 {
		return false
	}
	for _, expected := range p.logins {
		if trimmed == expected {
			return true
		}
	}
	return false
}

func (p policy) ExpectedPRAuthorSource() string {
	if len(p.logins) == 0 {
		return "no expected GitHub App PR author could be derived from " + p.source
	}
	return fmt.Sprintf("%s (from %s)", strings.Join(p.logins, " or "), p.source)
}
