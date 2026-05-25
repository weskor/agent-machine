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

func TestSyntheticGitLabCommentIDRoundTrip(t *testing.T) {
	id := syntheticGitLabCommentID(42, 99)
	mr, note, ok := splitGitLabCommentID(id)
	if !ok || mr != 42 || note != 99 {
		t.Fatalf("splitGitLabCommentID(%d) = mr=%d note=%d ok=%t", id, mr, note, ok)
	}
}
