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
func isExpectedGitHubAppPRAuthor(login string) bool { return ghapi.IsExpectedAppPRAuthor(login) }
func expectedGitHubAppPRAuthorLogins() string       { return ghapi.ExpectedAppPRAuthorLogins() }
func isExpectedGitHubAppCommitAuthor(author prCommitAuthor) bool {
	return ghapi.IsExpectedAppCommitAuthor(author)
}
func commitAuthorInvariantBlockReason(pr pullRequestSummary) string {
	return ghapi.CommitAuthorInvariantBlockReason(pr)
}
func configureGitHubAppCommitIdentity(workspace string, timeout time.Duration) error {
	return ghapi.ConfigureAppCommitIdentity(workspace, timeout)
}
func mintGitHubInstallationToken(appID, installationID, privateKeyPath string) (string, error) {
	return ghapi.MintInstallationToken(appID, installationID, privateKeyPath)
}
func githubAppJWT(appID, privateKeyPath string) (string, error) {
	return ghapi.AppJWT(appID, privateKeyPath)
}
