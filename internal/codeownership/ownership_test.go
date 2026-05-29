package codeownership

import (
	"strings"
	"testing"

	"github.com/weskor/agent-machine/internal/codehost"
)

func TestNewPolicyUsesGitHubOverride(t *testing.T) {
	policy := NewPolicy(Input{GitHubPRAuthorOverride: "bot-one, bot-two", GitHubAppExpectedLogins: []string{"app/config-bot"}, GitHubAppSource: "config github.app_slug"})

	if !policy.AllowsPRAuthor("bot-two") || policy.AllowsPRAuthor("app/config-bot") {
		t.Fatalf("unexpected override policy behavior")
	}
	if got := policy.ExpectedPRAuthorSource(); !strings.Contains(got, "bot-one or bot-two") || !strings.Contains(got, "config github.pr_author_override") {
		t.Fatalf("unexpected source: %q", got)
	}
}

func TestNewPolicyUsesGitHubAppIdentity(t *testing.T) {
	policy := NewPolicy(Input{GitHubAppExpectedLogins: []string{"app/config-bot", "config-bot[bot]"}, GitHubAppSource: "config github.app_slug"})

	if !policy.AllowsPRAuthor("app/config-bot") || !policy.AllowsPRAuthor("config-bot[bot]") {
		t.Fatalf("expected GitHub app logins to be allowed")
	}
	if got := policy.ExpectedPRAuthorSource(); !strings.Contains(got, "config github.app_slug") {
		t.Fatalf("unexpected source: %q", got)
	}
}

func TestNewPolicyUsesGitLabConfigThenEnvironmentOverride(t *testing.T) {
	configPolicy := NewPolicy(Input{Provider: codehost.ProviderGitLab, GitLabPRAuthorOverride: "config-bot", GitLabEnvPRAuthorOverride: "env-bot"})
	if !configPolicy.AllowsPRAuthor("config-bot") || configPolicy.AllowsPRAuthor("env-bot") {
		t.Fatalf("config override should win over env override")
	}

	envPolicy := NewPolicy(Input{Provider: codehost.ProviderGitLab, GitLabEnvPRAuthorOverride: "env-bot"})
	if !envPolicy.AllowsPRAuthor("env-bot") {
		t.Fatalf("expected env override to be allowed")
	}
}

func TestNewPolicyReportsMissingIdentity(t *testing.T) {
	policy := NewPolicy(Input{GitHubAppSource: "config github.app_slug or GITHUB_APP_SLUG"})

	if policy.AllowsPRAuthor("app/missing") {
		t.Fatal("missing identity should not allow authors")
	}
	if got := policy.ExpectedPRAuthorSource(); !strings.Contains(got, "no expected GitHub App PR author") {
		t.Fatalf("unexpected missing source: %q", got)
	}
}
