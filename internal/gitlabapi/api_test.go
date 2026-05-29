package gitlabapi

import "testing"

func TestGitLabWebBaseStripsAPIPath(t *testing.T) {
	tests := map[string]string{
		"https://gitlab.com":             "https://gitlab.com",
		"https://gitlab.example/api/v4/": "https://gitlab.example",
	}
	for input, want := range tests {
		if got := gitLabWebBase(input); got != want {
			t.Fatalf("gitLabWebBase(%q) = %q, want %q", input, got, want)
		}
	}
}
