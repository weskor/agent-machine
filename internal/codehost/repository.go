package codehost

import (
	"fmt"
	"net/url"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

type Repository struct {
	Provider string
	Host     string
	Owner    string
	Name     string
	Path     string
}

type PullRequestURL struct {
	Provider string
	Host     string
	Project  string
	Owner    string
	Repo     string
	Number   int
}

func (r Repository) FullName() string {
	if strings.TrimSpace(r.Path) != "" {
		return r.Path
	}
	if r.Owner == "" {
		return r.Name
	}
	return r.Owner + "/" + r.Name
}

func ParseRepository(value string) (Repository, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return Repository{}, false
	}
	if parts := strings.Split(value, "/"); len(parts) >= 2 && parts[0] != "" && parts[len(parts)-1] != "" && !strings.Contains(parts[0], ":") {
		repo := strings.TrimSuffix(parts[len(parts)-1], ".git")
		path := strings.Join(append(parts[:len(parts)-1], repo), "/")
		return repositoryFromPath("", path), true
	}
	if parsed, err := url.Parse(value); err == nil && parsed.Host != "" {
		path := strings.Trim(strings.TrimSuffix(parsed.Path, ".git"), "/")
		if path != "" {
			return repositoryFromHostPath(parsed.Host, path), true
		}
	}
	if match := regexp.MustCompile(`^git@([^:]+):(.+)$`).FindStringSubmatch(value); len(match) == 3 {
		path := strings.Trim(strings.TrimSuffix(match[2], ".git"), "/")
		if path != "" {
			return repositoryFromHostPath(match[1], path), true
		}
	}
	if match := regexp.MustCompile(`^ssh://git@([^/]+)/(.+)$`).FindStringSubmatch(value); len(match) == 3 {
		path := strings.Trim(strings.TrimSuffix(match[2], ".git"), "/")
		if path != "" {
			return repositoryFromHostPath(match[1], path), true
		}
	}
	return Repository{}, false
}

func ParsePullRequestURL(value string) (PullRequestURL, bool) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return PullRequestURL{}, false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for i := 0; i < len(parts); i++ {
		if parts[i] == "pull" && parsed.Host == "github.com" && i >= 2 && i+1 < len(parts) {
			number, ok := parsePositiveInt(parts[i+1])
			if !ok {
				return PullRequestURL{}, false
			}
			project := strings.Join(parts[:i], "/")
			return PullRequestURL{Provider: ProviderGitHub, Host: parsed.Host, Project: project, Owner: parts[i-2], Repo: parts[i-1], Number: number}, true
		}
		if parts[i] == "-" && i+2 < len(parts) && parts[i+1] == "merge_requests" {
			number, ok := parsePositiveInt(parts[i+2])
			if !ok || i == 0 {
				return PullRequestURL{}, false
			}
			project := strings.Join(parts[:i], "/")
			repoParts := strings.Split(project, "/")
			repo := repoParts[len(repoParts)-1]
			owner := strings.Join(repoParts[:len(repoParts)-1], "/")
			return PullRequestURL{Provider: ProviderGitLab, Host: parsed.Host, Project: project, Owner: owner, Repo: repo, Number: number}, true
		}
	}
	return PullRequestURL{}, false
}

func RepositoryFromDir(dir string) (Repository, error) {
	out, err := exec.Command("git", "-C", dir, "config", "--get", "remote.origin.url").Output()
	if err != nil {
		return Repository{}, err
	}
	remote := strings.TrimSpace(string(out))
	if repo, ok := ParseRepository(remote); ok {
		return repo, nil
	}
	return Repository{}, fmt.Errorf("code host repository could not be parsed from remote origin %q", remote)
}

func ConfigureRepositoryEnvFromConfig(configPath string, setenv func(string, string) error) {
	repo, err := RepositoryFromDir(filepath.Dir(configPath))
	if err != nil {
		return
	}
	if repo.Provider == ProviderGitHub || repo.Provider == "" {
		_ = setenv("GITHUB_REPOSITORY", repo.FullName())
	}
	if repo.Provider == ProviderGitLab {
		_ = setenv("GITLAB_PROJECT", repo.FullName())
		if repo.Host != "" {
			_ = setenv("GITLAB_ENDPOINT", "https://"+repo.Host)
		}
	}
}

func repositoryFromHostPath(host, path string) Repository {
	repo := repositoryFromPath(host, path)
	switch strings.ToLower(host) {
	case "github.com":
		repo.Provider = ProviderGitHub
	case "gitlab.com":
		repo.Provider = ProviderGitLab
	}
	return repo
}

func repositoryFromPath(host, path string) Repository {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	name := strings.TrimSuffix(parts[len(parts)-1], ".git")
	parts[len(parts)-1] = name
	owner := ""
	if len(parts) > 1 {
		owner = strings.Join(parts[:len(parts)-1], "/")
	}
	return Repository{Host: host, Owner: owner, Name: name, Path: strings.Join(parts, "/")}
}

func parsePositiveInt(value string) (int, bool) {
	var number int
	if value == "" {
		return 0, false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, false
		}
		number = number*10 + int(r-'0')
	}
	return number, number > 0
}
