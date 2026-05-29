package main

import (
	"fmt"
	"os"
	"strings"
)

type githubOwnershipPolicy interface {
	AllowsPRAuthor(login string) bool
	ExpectedPRAuthorSource() string
}

type githubAppOwnershipPolicy struct {
	logins []string
	source string
}

func newGitHubOwnershipPolicy(config runnerConfig) githubOwnershipPolicy {
	if override := splitAuthorLogins(config.GitHubPRAuthorOverride); len(override) > 0 {
		return githubAppOwnershipPolicy{logins: override, source: "config github.pr_author_override"}
	}
	identity := githubAppIdentityFromConfig(config)
	logins := identity.ExpectedPRAuthorLogins()
	if len(logins) == 0 {
		return githubAppOwnershipPolicy{source: "config github.app_slug or GITHUB_APP_SLUG"}
	}
	return githubAppOwnershipPolicy{
		logins: logins,
		source: identity.Source,
	}

}

func newCodeHostOwnershipPolicy(config runnerConfig) githubOwnershipPolicy {
	if codeHostProvider(config) == "gitlab" {
		if override := splitAuthorLogins(config.GitLabPRAuthorOverride); len(override) > 0 {
			return githubAppOwnershipPolicy{logins: override, source: "config gitlab.pr_author_override"}
		}
		if override := splitAuthorLogins(os.Getenv("GITLAB_PR_AUTHOR_OVERRIDE")); len(override) > 0 {
			return githubAppOwnershipPolicy{logins: override, source: "GITLAB_PR_AUTHOR_OVERRIDE"}
		}
		return githubAppOwnershipPolicy{source: "missing config gitlab.pr_author_override or GITLAB_PR_AUTHOR_OVERRIDE"}
	}
	return newGitHubOwnershipPolicy(config)
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

func (p githubAppOwnershipPolicy) AllowsPRAuthor(login string) bool {
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

func (p githubAppOwnershipPolicy) ExpectedPRAuthorSource() string {
	if len(p.logins) == 0 {
		return "no expected GitHub App PR author could be derived from " + p.source
	}
	return fmt.Sprintf("%s (from %s)", strings.Join(p.logins, " or "), p.source)
}
