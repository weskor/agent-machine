package linear

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCandidatesPostsGraphQLRequestAndDecodesIssues(t *testing.T) {
	var requestBody struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("Content-Type = %q, want application/json", got)
		}
		if got := r.Header.Get("Authorization"); got != "test-key" {
			t.Fatalf("Authorization = %q, want test-key", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if !strings.Contains(requestBody.Query, "issues(first: 10") {
			t.Fatalf("query = %q, want candidates query", requestBody.Query)
		}
		if got := requestBody.Variables["projectSlug"]; got != "am" {
			t.Fatalf("projectSlug = %v, want am", got)
		}
		states, ok := requestBody.Variables["states"].([]any)
		if !ok || len(states) != 2 || states[0] != "Ready for Agent" || states[1] != "Human Review" {
			t.Fatalf("states = %#v, want Ready for Agent and Human Review", requestBody.Variables["states"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"issues":{"nodes":[{"id":"issue-id","identifier":"CAG-77","title":"Move Linear adapter","url":"https://linear.app/acme/issue/CAG-77","description":"Adapter extraction","priority":2,"createdAt":"2026-05-23T10:00:00Z","team":{"id":"team-id","key":"CAG","name":"Agent Machine Runner"},"state":{"name":"Ready for Agent"},"labels":{"nodes":[{"name":"runner"}]}}]}}}`))
	}))
	defer server.Close()

	client := NewClient("test-key", server.URL)
	issues, err := client.Candidates("am", []string{"Ready for Agent", "Human Review"})
	if err != nil {
		t.Fatalf("Candidates returned error: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("len(issues) = %d, want 1", len(issues))
	}
	issue := issues[0]
	if issue.Identifier != "CAG-77" || issue.Team.ID != "team-id" || issue.State.Name != "Ready for Agent" {
		t.Fatalf("decoded issue = %#v", issue)
	}
	if len(issue.Labels.Nodes) != 1 || issue.Labels.Nodes[0].Name != "runner" {
		t.Fatalf("decoded labels = %#v", issue.Labels.Nodes)
	}
}

func TestCandidatesContextHonorsCanceledContextBeforeRequest(t *testing.T) {
	requested := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = true
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := NewClient("test-key", server.URL).CandidatesContext(ctx, "am", []string{"Ready for Agent"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("CandidatesContext() error = %v; want context.Canceled", err)
	}
	if requested {
		t.Fatal("CandidatesContext issued HTTP request after context cancellation")
	}
}

func TestMutationsReturnErrorsWhenLinearReportsFailure(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		call    func(Client) error
		wantErr string
	}{
		{
			name:    "issue update",
			body:    `{"data":{"issueUpdate":{"success":false}}}`,
			call:    func(client Client) error { return client.UpdateIssueState("issue-id", "state-id") },
			wantErr: "linear issueUpdate returned success=false",
		},
		{
			name:    "comment create",
			body:    `{"data":{"commentCreate":{"success":false}}}`,
			call:    func(client Client) error { return client.CreateComment("issue-id", "body") },
			wantErr: "linear commentCreate returned success=false",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			err := tt.call(NewClient("test-key", server.URL))
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}
