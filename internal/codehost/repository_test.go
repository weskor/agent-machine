package codehost

import "testing"

func TestParseRepositorySupportsGitHubAndGitLab(t *testing.T) {
	tests := []struct {
		value    string
		provider string
		fullName string
	}{
		{"git@github.com:acme/rocket.git", ProviderGitHub, "acme/rocket"},
		{"https://gitlab.com/acme/platform/runner.git", ProviderGitLab, "acme/platform/runner"},
		{"acme/runner", "", "acme/runner"},
	}
	for _, tt := range tests {
		got, ok := ParseRepository(tt.value)
		if !ok {
			t.Fatalf("ParseRepository(%q) did not parse", tt.value)
		}
		if got.Provider != tt.provider || got.FullName() != tt.fullName {
			t.Fatalf("ParseRepository(%q) = provider=%q full=%q, want provider=%q full=%q", tt.value, got.Provider, got.FullName(), tt.provider, tt.fullName)
		}
	}
}

func TestParsePullRequestURLSupportsGitHubPRsAndGitLabMRs(t *testing.T) {
	github, ok := ParsePullRequestURL("https://github.com/acme/rocket/pull/17")
	if !ok || github.Provider != ProviderGitHub || github.Project != "acme/rocket" || github.Number != 17 {
		t.Fatalf("unexpected GitHub PR parse: %#v ok=%t", github, ok)
	}

	gitlab, ok := ParsePullRequestURL("https://gitlab.example.com/acme/platform/runner/-/merge_requests/42")
	if !ok || gitlab.Provider != ProviderGitLab || gitlab.Project != "acme/platform/runner" || gitlab.Number != 42 || gitlab.Host != "gitlab.example.com" {
		t.Fatalf("unexpected GitLab MR parse: %#v ok=%t", gitlab, ok)
	}

	if _, ok := ParsePullRequestURL("https://example.com/acme/rocket/pull/17"); ok {
		t.Fatal("expected non-GitHub pull URL to be ignored")
	}
}
