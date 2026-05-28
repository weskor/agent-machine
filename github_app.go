package main

import (
	"time"

	"github.com/weskor/agent-machine/internal/ghapi"
)

const githubAppPRAuthorLogin = ghapi.AppPRAuthorLogin
const githubAppRESTPRAuthorLogin = ghapi.AppRESTPRAuthorLogin
const githubAppBotName = ghapi.AppBotName
const githubAppBotEmail = ghapi.AppBotEmail

func githubAppEnvFromEnvironment() (map[string]string, string, error) {
	return ghapi.AppEnvFromEnvironment()
}
func githubAppIdentityFromConfig(config runnerConfig) ghapi.AppIdentity {
	if identity, ok := ghapi.NewAppIdentity(config.GitHubAppSlug, "config github.app_slug"); ok {
		return identity
	}
	if identity, ok := ghapi.AppIdentityFromEnvironment(); ok {
		return identity
	}
	return ghapi.AppIdentity{Source: "config github.app_slug or GITHUB_APP_SLUG"}
}
func commitAuthorInvariantBlockReason(config runnerConfig, pr pullRequestSummary) string {
	return ghapi.CommitAuthorInvariantBlockReason(githubAppIdentityFromConfig(config), pr)
}
func configureGitHubAppCommitIdentity(config runnerConfig, workspace string, timeout time.Duration) error {
	return ghapi.ConfigureAppCommitIdentity(githubAppIdentityFromConfig(config), workspace, timeout)
}
