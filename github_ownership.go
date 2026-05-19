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
		return githubAppOwnershipPolicy{logins: override, source: "workflow github.pr_author_override"}
	}
	slug := strings.TrimSpace(config.GitHubAppSlug)
	source := "workflow github.app_slug"
	if slug == "" {
		slug = strings.TrimSpace(os.Getenv("GITHUB_APP_SLUG"))
		source = "GITHUB_APP_SLUG"
	}
	if slug == "" {
		return githubAppOwnershipPolicy{source: "missing workflow github.app_slug or GITHUB_APP_SLUG"}
	}
	return githubAppOwnershipPolicy{
		logins: []string{fmt.Sprintf("app/%s", slug), fmt.Sprintf("%s[bot]", slug)},
		source: source,
	}

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
