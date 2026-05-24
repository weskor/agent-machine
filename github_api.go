package main

import (
	"context"
	"time"

	"github.com/weskor/pi-symphony/internal/ghapi"
)

type githubAPI = ghapi.Client

var newGitHubAPI = ghapi.NewClient
var githubAppEnvFromEnvironmentForAPI = ghapi.AppEnvFromEnvironmentForAPI

func githubAPITokenFromEnvironment() (string, error) {
	previous := ghapi.AppEnvFromEnvironmentForAPI
	ghapi.AppEnvFromEnvironmentForAPI = githubAppEnvFromEnvironmentForAPI
	defer func() { ghapi.AppEnvFromEnvironmentForAPI = previous }()
	return ghapi.APITokenFromEnvironment()
}

func currentGitHubRepo() (string, string, error) { return ghapi.CurrentRepository() }

func configureGitHubRepositoryFromConfig(configPath string) {
	ghapi.ConfigureRepositoryFromConfig(configPath)
}

func parseGitHubRepository(value string) (string, string, bool) { return ghapi.ParseRepository(value) }

func githubClientWithTimeout(timeout time.Duration) (githubAPI, context.Context, context.CancelFunc, error) {
	return githubClientWithContextTimeout(context.Background(), timeout)
}

func githubClientWithContextTimeout(parent context.Context, timeout time.Duration) (githubAPI, context.Context, context.CancelFunc, error) {
	client, err := newGitHubAPI()
	if err != nil {
		return nil, nil, nil, err
	}
	if timeout <= 0 {
		timeout = defaultGitHubCommandTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	return client, ctx, cancel, nil
}
