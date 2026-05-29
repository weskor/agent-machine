package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/weskor/agent-machine/internal/codehost"
	"github.com/weskor/agent-machine/internal/ghapi"
	"github.com/weskor/agent-machine/internal/gitlabapi"
)

type githubAPI = codehost.Client

type pullRequestSummary = ghapi.PullRequestSummary
type prAuthor = ghapi.PRAuthor
type prCommit = ghapi.PRCommit
type prCommitAuthor = ghapi.PRCommitAuthor
type statusCheck = ghapi.StatusCheck
type prFeedback = ghapi.PRFeedback
type prHandoffDetails = ghapi.PRHandoffDetails

const githubAppPRAuthorLogin = ghapi.AppPRAuthorLogin
const githubAppRESTPRAuthorLogin = ghapi.AppRESTPRAuthorLogin
const githubAppBotName = ghapi.AppBotName
const githubAppBotEmail = ghapi.AppBotEmail

var newGitHubAPI = func() (githubAPI, error) { return ghapi.NewClient() }
var newGitLabAPI = gitlabapi.NewClient

func currentGitHubRepo() (string, string, error) { return ghapi.CurrentRepository() }

func configureGitHubRepositoryFromConfig(configPath string) {
	ghapi.ConfigureRepositoryFromConfig(configPath)
}

func githubAppEnvFromEnvironment() (map[string]string, string, error) {
	return ghapi.AppEnvFromEnvironment()
}

func githubAppIdentityFromConfig(config runnerConfig) ghapi.AppIdentity {
	return ghapi.AppIdentityFromConfigSlug(config.GitHubAppSlug)
}

func commitAuthorInvariantBlockReason(config runnerConfig, pr pullRequestSummary) string {
	return ghapi.CommitAuthorInvariantBlockReason(githubAppIdentityFromConfig(config), pr)
}

func configureGitHubAppCommitIdentity(config runnerConfig, workspace string, timeout time.Duration) error {
	return ghapi.ConfigureAppCommitIdentity(githubAppIdentityFromConfig(config), workspace, timeout)
}

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

func codeHostClientWithTimeout(config runnerConfig, timeout time.Duration) (githubAPI, context.Context, context.CancelFunc, error) {
	return codeHostClientWithContextTimeout(context.Background(), config, timeout)
}

func codeHostClientWithContextTimeout(parent context.Context, config runnerConfig, timeout time.Duration) (githubAPI, context.Context, context.CancelFunc, error) {
	client, err := newCodeHostAPI(config)
	if err != nil {
		return nil, nil, nil, err
	}
	if timeout <= 0 {
		timeout = defaultGitHubCommandTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	return client, ctx, cancel, nil
}

func codeHostClientForPRURLWithTimeout(prURL string, timeout time.Duration) (githubAPI, context.Context, context.CancelFunc, error) {
	parsed, ok := codehost.ParsePullRequestURL(prURL)
	if !ok {
		return nil, nil, nil, fmt.Errorf("invalid code-host PR URL %q", prURL)
	}
	var (
		client githubAPI
		err    error
	)
	switch parsed.Provider {
	case codehost.ProviderGitLab:
		client, err = newGitLabAPI(codehost.Config{Provider: codehost.ProviderGitLab, Endpoint: "https://" + parsed.Host, Project: parsed.Project})
	default:
		client, err = newGitHubAPI()
	}
	if err != nil {
		return nil, nil, nil, err
	}
	if timeout <= 0 {
		timeout = defaultGitHubCommandTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	return client, ctx, cancel, nil
}

func newCodeHostAPI(config runnerConfig) (githubAPI, error) {
	provider := codeHostProvider(config)
	switch provider {
	case codehost.ProviderGitLab:
		return newGitLabAPI(codehost.Config{Provider: codehost.ProviderGitLab, Endpoint: config.GitLabEndpoint, Project: gitLabProject(config)})
	case codehost.ProviderGitHub:
		return newGitHubAPI()
	default:
		return nil, fmt.Errorf("unsupported repository.provider %q", provider)
	}
}

func codeHostProvider(config runnerConfig) string {
	if provider := strings.ToLower(strings.TrimSpace(config.RepositoryProvider)); provider != "" {
		return provider
	}
	if repo, ok := codehost.ParseRepository(config.RepositoryRemote); ok && repo.Provider != "" {
		return repo.Provider
	}
	return codehost.ProviderGitHub
}

func gitLabProject(config runnerConfig) string {
	if config.GitLabProject != "" {
		return config.GitLabProject
	}
	if repo, ok := codehost.ParseRepository(config.RepositoryRemote); ok && repo.Provider == codehost.ProviderGitLab {
		return repo.FullName()
	}
	return ""
}

func currentCodeHostRepo(config runnerConfig) (codehost.Repository, error) {
	if codeHostProvider(config) == codehost.ProviderGitLab {
		project := gitLabProject(config)
		if project == "" {
			project = codehostEnv("GITLAB_PROJECT")
		}
		if repo, ok := codehost.ParseRepository(project); ok {
			repo.Provider = codehost.ProviderGitLab
			if repo.Host == "" {
				repo.Host = "gitlab.com"
			}
			return repo, nil
		}
	}
	owner, repo, err := currentGitHubRepo()
	if err != nil {
		return codehost.Repository{}, err
	}
	return codehost.Repository{Provider: codehost.ProviderGitHub, Host: "github.com", Owner: owner, Name: repo, Path: owner + "/" + repo}, nil
}

func codehostEnv(name string) string {
	return strings.TrimSpace(os.Getenv(name))
}
