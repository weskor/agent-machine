package main

import "testing"

func TestGitHubAppIdentityFromConfigFallsBackToEnvironment(t *testing.T) {
	config := testRunnerConfig(t.TempDir())
	config.GitHubAppSlug = ""
	t.Setenv("GITHUB_APP_SLUG", "env-bot")

	identity := githubAppIdentityFromConfig(config)
	if identity.BotName() != "env-bot[bot]" || identity.BotEmail() != "env-bot[bot]@users.noreply.github.com" {
		t.Fatalf("unexpected env-derived identity: %+v", identity)
	}
	if got := identity.ExpectedPRAuthorLogins(); len(got) != 2 || got[0] != "app/env-bot" || got[1] != "env-bot[bot]" {
		t.Fatalf("unexpected env-derived PR authors: %#v", got)
	}
}

func TestGitHubAppIdentityFromConfigPrefersConfigSlug(t *testing.T) {
	config := testRunnerConfig(t.TempDir())
	config.GitHubAppSlug = "config-bot"
	t.Setenv("GITHUB_APP_SLUG", "env-bot")

	identity := githubAppIdentityFromConfig(config)
	if identity.BotName() != "config-bot[bot]" || identity.Source != "config github.app_slug" {
		t.Fatalf("unexpected config-derived identity: %+v", identity)
	}
}
