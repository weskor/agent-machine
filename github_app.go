package main

import (
	"time"

	"github.com/weskor/pi-symphony/internal/ghapi"
)

const githubAppPRAuthorLogin = ghapi.AppPRAuthorLogin
const githubAppRESTPRAuthorLogin = ghapi.AppRESTPRAuthorLogin
const githubAppBotName = ghapi.AppBotName
const githubAppBotEmail = ghapi.AppBotEmail

func githubAppEnvFromEnvironment() (map[string]string, string, error) {
	return ghapi.AppEnvFromEnvironment()
}
func commitAuthorInvariantBlockReason(pr pullRequestSummary) string {
	return ghapi.CommitAuthorInvariantBlockReason(pr)
}
func configureGitHubAppCommitIdentity(workspace string, timeout time.Duration) error {
	return ghapi.ConfigureAppCommitIdentity(workspace, timeout)
}
