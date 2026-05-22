package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	cfg "github.com/weskor/pi-symphony/internal/config"
	"github.com/weskor/pi-symphony/internal/livesmoke"
)

type issueList []string

func (l *issueList) String() string { return strings.Join(*l, ",") }
func (l *issueList) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("empty issue identifier")
	}
	*l = append(*l, value)
	return nil
}

type options struct {
	workflow      string
	count         int
	concurrency   int
	workspaceRoot string
	reportPath    string
	fakeAgent     bool
	applyMerge    bool
	issues        issueList
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "pi-symphony-live-smoke: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, environ []string) error {
	opts := parseOptions(args)
	env := envMap(environ)
	if err := livesmoke.ValidateEnvironment(env, opts.applyMerge); err != nil {
		return err
	}
	if opts.count < 1 {
		return errors.New("--count must be at least 1")
	}
	if opts.concurrency < 1 {
		return errors.New("--concurrency must be at least 1")
	}
	if !opts.fakeAgent {
		return errors.New("only --fake-agent=true is supported by this harness slice")
	}

	workflow, err := cfg.ReadWorkflow(opts.workflow)
	if err != nil {
		return err
	}
	config, err := cfg.ParseConfig(workflow.YAML)
	if err != nil {
		return err
	}
	if opts.workspaceRoot == "" {
		opts.workspaceRoot, err = os.MkdirTemp("", "pi-symphony-live-smoke-*")
		if err != nil {
			return err
		}
	}
	if opts.reportPath == "" {
		opts.reportPath = filepath.Join(".symphony", "live-smoke", fmt.Sprintf("live-smoke-%s.json", time.Now().UTC().Format("20060102T150405Z")))
	}

	client := linearClient{endpoint: config.Tracker.Endpoint, apiKey: env["LINEAR_API_KEY"]}
	project, err := client.project(ctx, config.Tracker.ProjectSlug)
	if err != nil {
		return err
	}
	readyID, err := client.stateID(ctx, project.TeamID, "Ready for Agent")
	if err != nil {
		return err
	}

	issueRefs, err := prepareIssues(ctx, client, config.Tracker.ProjectSlug, project, readyID, opts)
	if err != nil {
		return err
	}
	smokeWorkflow, err := writeSmokeWorkflow(opts, config)
	if err != nil {
		return err
	}

	report := livesmoke.Report{StartedAt: time.Now().UTC(), WorkflowPath: opts.workflow, SmokeWorkflow: smokeWorkflow, WorkspaceRoot: opts.workspaceRoot, FakeAgent: opts.fakeAgent, ApplyMerge: opts.applyMerge, Issues: issueRefs, ReportPath: opts.reportPath}
	fmt.Printf("smoke_workflow=%s\n", smokeWorkflow)
	fmt.Printf("workspace_root=%s\n", opts.workspaceRoot)
	for _, issue := range issueRefs {
		fmt.Printf("issue=%s url=%s path=%s\n", issue.Identifier, issue.URL, issue.Path)
	}

	for range issueRefs {
		command := fmt.Sprintf("go run . --once %s", shellQuote(smokeWorkflow))
		report.Commands = append(report.Commands, command)
		if err := runCommand(ctx, command, "."); err != nil {
			_ = writeReport(report)
			return err
		}
	}
	statusCommand := fmt.Sprintf("go run . --status %s", shellQuote(smokeWorkflow))
	report.Commands = append(report.Commands, statusCommand)
	if err := runCommand(ctx, statusCommand, "."); err != nil {
		_ = writeReport(report)
		return err
	}
	report.FinalStatusRan = true
	if opts.applyMerge {
		mergeCommand := fmt.Sprintf("go run . --merge-approved %s", shellQuote(smokeWorkflow))
		report.Commands = append(report.Commands, mergeCommand)
		if err := runCommand(ctx, mergeCommand, "."); err != nil {
			_ = writeReport(report)
			return err
		}
	}
	if err := writeReport(report); err != nil {
		return err
	}
	fmt.Printf("report=%s\n", opts.reportPath)
	return nil
}

func parseOptions(args []string) options {
	opts := options{workflow: "WORKFLOW.md", count: 1, concurrency: 1, fakeAgent: true}
	flags := flag.NewFlagSet("pi-symphony-live-smoke", flag.ExitOnError)
	flags.StringVar(&opts.workflow, "workflow", opts.workflow, "source workflow path")
	flags.IntVar(&opts.count, "count", opts.count, "number of disposable issues to create when --issue is not supplied")
	flags.IntVar(&opts.concurrency, "concurrency", opts.concurrency, "agent.max_concurrent_agents value written to the generated workflow")
	flags.StringVar(&opts.workspaceRoot, "workspace-root", "", "isolated workspace root for the smoke workflow")
	flags.StringVar(&opts.reportPath, "report", "", "JSON report path")
	flags.BoolVar(&opts.fakeAgent, "fake-agent", opts.fakeAgent, "use the deterministic fake smoke agent")
	flags.BoolVar(&opts.applyMerge, "apply-merge", false, "also run merge-approved; requires LIVE_SMOKE_APPLY=1")
	flags.Var(&opts.issues, "issue", "existing disposable Linear issue identifier to use; repeatable")
	_ = flags.Parse(args)
	if len(opts.issues) > 0 {
		opts.count = len(opts.issues)
	}
	return opts
}

type linearClient struct {
	endpoint string
	apiKey   string
}

type projectInfo struct {
	ID     string
	Name   string
	TeamID string
}

func prepareIssues(ctx context.Context, client linearClient, projectSlug string, project projectInfo, readyID string, opts options) ([]livesmoke.IssueRef, error) {
	ready, err := client.issuesByState(ctx, projectSlug, "Ready for Agent")
	if err != nil {
		return nil, err
	}
	if len(opts.issues) == 0 && len(ready) > 0 {
		return nil, fmt.Errorf("refusing to create smoke issues while Ready for Agent already has %d issue(s): %s", len(ready), issueIdentifiers(ready))
	}
	if len(opts.issues) > 0 {
		allowed := map[string]bool{}
		for _, identifier := range opts.issues {
			allowed[identifier] = true
		}
		for _, issue := range ready {
			if !allowed[issue.Identifier] {
				return nil, fmt.Errorf("refusing to run while unrelated Ready issue exists: %s", issue.Identifier)
			}
		}
		refs := make([]livesmoke.IssueRef, 0, len(opts.issues))
		for _, identifier := range opts.issues {
			issue, err := client.issue(ctx, identifier)
			if err != nil {
				return nil, err
			}
			refs = append(refs, issue)
		}
		return refs, nil
	}

	refs := make([]livesmoke.IssueRef, 0, opts.count)
	for i := 1; i <= opts.count; i++ {
		path := fmt.Sprintf("docs/smoke/live-smoke-%s-%02d.md", time.Now().UTC().Format("20060102T150405Z"), i)
		title := fmt.Sprintf("Pi Symphony: disposable live smoke %s", strings.TrimSuffix(filepath.Base(path), ".md"))
		issue, err := client.createIssue(ctx, project.TeamID, project.ID, readyID, title, livesmoke.DisposableIssueDescription(path), path)
		if err != nil {
			return nil, err
		}
		refs = append(refs, issue)
	}
	return refs, nil
}

func writeSmokeWorkflow(opts options, config cfg.Config) (string, error) {
	if err := os.MkdirAll(opts.workspaceRoot, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(opts.workspaceRoot, "WORKFLOW.live-smoke.md")
	cloneCommand := strings.TrimSpace(config.Pi.AfterCreate)
	if cloneCommand == "" {
		cloneCommand = strings.TrimSpace(config.Hooks.AfterCreate)
	}
	if cloneCommand == "" {
		cloneCommand = "git clone --branch " + config.Workspace.BaseBranch + " git@github.com:weskor/pi-symphony.git ."
	}
	content := fmt.Sprintf(`---
tracker:
  kind: linear
  endpoint: %s
  api_key: $LINEAR_API_KEY
  project_slug: %s
  active_states:
    - Ready for Agent
  needs_info_state: %s
  terminal_states:
    - Done
    - Canceled
    - Cancelled
    - Duplicate
polling:
  interval_ms: 30000
workspace:
  root: %s
  base_branch: %s
hooks:
  timeout_ms: %s
agent:
  max_concurrent_agents: %d
  max_turns: 1
  max_retry_backoff_ms: %s
pi:
  command: >-
    go run ./cmd/pi-symphony-live-smoke-agent --role implementation
  review_command: >-
    go run ./cmd/pi-symphony-live-smoke-agent --role review
  after_create: |
%s
  before_run: mise exec go -- go test ./...
  after_run: mise exec go -- go test ./... && git diff --check
budgets:
  wall_clock: 2h
  max_tokens: 0
  max_cost: 0
  command_timeout: 10m
  pi_timeout: 20m
  review_timeout: 10m
  merge_timeout: 10m
  github_timeout: 2m
github:
  app_slug: %s
compound:
  handoff_state: %s
  running_state: %s
  needs_info_state: %s
  done_state: %s
  auto_merge: false
  required_validation:
    - mise exec go -- go test ./...
    - git diff --check
---

# Pi Symphony Live Smoke Workflow

Generated by cmd/pi-symphony-live-smoke.

Issue context:

- Identifier: {{issue.identifier}}
- Title: {{issue.title}}
- URL: {{issue.url}}
- State: {{issue.state}}
- Attempt: {{attempt}}
`, yamlScalar(config.Tracker.Endpoint), yamlScalar(config.Tracker.ProjectSlug), yamlScalar(config.Tracker.NeedsInfoState), yamlScalar(opts.workspaceRoot), yamlScalar(config.Workspace.BaseBranch), config.Hooks.TimeoutText, opts.concurrency, config.Agent.MaxRetryBackoffText, indentBlock(cloneCommand, 4), yamlScalar(config.GitHub.AppSlug), yamlScalar(config.Compound.HandoffState), yamlScalar(config.Compound.RunningState), yamlScalar(config.Compound.NeedsInfoState), yamlScalar(config.Compound.DoneState))
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func (c linearClient) project(ctx context.Context, slug string) (projectInfo, error) {
	var out struct {
		Projects struct {
			Nodes []struct {
				ID    string `json:"id"`
				Name  string `json:"name"`
				Teams struct {
					Nodes []struct {
						ID string `json:"id"`
					} `json:"nodes"`
				} `json:"teams"`
			} `json:"nodes"`
		} `json:"projects"`
	}
	err := c.query(ctx, `query($slug: String!) { projects(first: 1, filter: { slugId: { eq: $slug } }) { nodes { id name teams(first: 1) { nodes { id } } } } }`, map[string]any{"slug": slug}, &out)
	if err != nil {
		return projectInfo{}, err
	}
	if len(out.Projects.Nodes) == 0 || len(out.Projects.Nodes[0].Teams.Nodes) == 0 {
		return projectInfo{}, fmt.Errorf("Linear project %q not found or has no team", slug)
	}
	project := out.Projects.Nodes[0]
	return projectInfo{ID: project.ID, Name: project.Name, TeamID: project.Teams.Nodes[0].ID}, nil
}

func (c linearClient) stateID(ctx context.Context, teamID, name string) (string, error) {
	var out struct {
		WorkflowStates struct {
			Nodes []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"nodes"`
		} `json:"workflowStates"`
	}
	err := c.query(ctx, `query($teamId: ID!) { workflowStates(first: 50, filter: { team: { id: { eq: $teamId } } }) { nodes { id name } } }`, map[string]any{"teamId": teamID}, &out)
	if err != nil {
		return "", err
	}
	for _, state := range out.WorkflowStates.Nodes {
		if state.Name == name {
			return state.ID, nil
		}
	}
	return "", fmt.Errorf("Linear state %q not found", name)
}

func (c linearClient) issuesByState(ctx context.Context, projectSlug, state string) ([]livesmoke.IssueRef, error) {
	var out struct {
		Issues struct {
			Nodes []struct {
				Identifier string `json:"identifier"`
				URL        string `json:"url"`
				Title      string `json:"title"`
			} `json:"nodes"`
		} `json:"issues"`
	}
	err := c.query(ctx, `query($projectSlug: String!, $state: String!) { issues(first: 50, filter: { project: { slugId: { eq: $projectSlug } }, state: { name: { eq: $state } } }) { nodes { identifier url title } } }`, map[string]any{"projectSlug": projectSlug, "state": state}, &out)
	if err != nil {
		return nil, err
	}
	refs := make([]livesmoke.IssueRef, 0, len(out.Issues.Nodes))
	for _, issue := range out.Issues.Nodes {
		refs = append(refs, livesmoke.IssueRef{Identifier: issue.Identifier, URL: issue.URL, Title: issue.Title})
	}
	return refs, nil
}

func (c linearClient) issue(ctx context.Context, identifier string) (livesmoke.IssueRef, error) {
	var out struct {
		Issue struct {
			Identifier string `json:"identifier"`
			URL        string `json:"url"`
			Title      string `json:"title"`
		} `json:"issue"`
	}
	err := c.query(ctx, `query($id: String!) { issue(id: $id) { identifier url title } }`, map[string]any{"id": identifier}, &out)
	if err != nil {
		return livesmoke.IssueRef{}, err
	}
	if out.Issue.Identifier == "" {
		return livesmoke.IssueRef{}, fmt.Errorf("Linear issue %s not found", identifier)
	}
	return livesmoke.IssueRef{Identifier: out.Issue.Identifier, URL: out.Issue.URL, Title: out.Issue.Title}, nil
}

func (c linearClient) createIssue(ctx context.Context, teamID, projectID, stateID, title, description, path string) (livesmoke.IssueRef, error) {
	var out struct {
		IssueCreate struct {
			Success bool `json:"success"`
			Issue   struct {
				Identifier string `json:"identifier"`
				URL        string `json:"url"`
				Title      string `json:"title"`
			} `json:"issue"`
		} `json:"issueCreate"`
	}
	err := c.query(ctx, `mutation($input: IssueCreateInput!) { issueCreate(input: $input) { success issue { identifier url title } } }`, map[string]any{"input": map[string]any{"teamId": teamID, "projectId": projectID, "stateId": stateID, "title": title, "description": description, "priority": 4}}, &out)
	if err != nil {
		return livesmoke.IssueRef{}, err
	}
	if !out.IssueCreate.Success {
		return livesmoke.IssueRef{}, errors.New("Linear issueCreate returned success=false")
	}
	return livesmoke.IssueRef{Identifier: out.IssueCreate.Issue.Identifier, URL: out.IssueCreate.Issue.URL, Title: out.IssueCreate.Issue.Title, Path: path}, nil
}

func (c linearClient) query(ctx context.Context, query string, variables map[string]any, out any) error {
	body, _ := json.Marshal(map[string]any{"query": query, "variables": variables})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", c.apiKey)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	var envelope struct {
		Data   json.RawMessage   `json:"data"`
		Errors []json.RawMessage `json:"errors"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return err
	}
	if res.StatusCode >= 300 || len(envelope.Errors) > 0 {
		return fmt.Errorf("Linear API error: %s", string(data))
	}
	return json.Unmarshal(envelope.Data, out)
}

func runCommand(ctx context.Context, command, dir string) error {
	fmt.Printf("$ %s\n", command)
	cmd := exec.CommandContext(ctx, "sh", "-lc", command)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.Env = os.Environ()
	return cmd.Run()
}

func writeReport(report livesmoke.Report) error {
	if err := os.MkdirAll(filepath.Dir(report.ReportPath), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(report.ReportPath, append(data, '\n'), 0o600)
}

func envMap(environ []string) map[string]string {
	env := map[string]string{}
	for _, entry := range environ {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			env[key] = value
		}
	}
	return env
}

func issueIdentifiers(issues []livesmoke.IssueRef) string {
	ids := make([]string, 0, len(issues))
	for _, issue := range issues {
		ids = append(ids, issue.Identifier)
	}
	return strings.Join(ids, ", ")
}

func yamlScalar(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
}

func indentBlock(value string, spaces int) string {
	prefix := strings.Repeat(" ", spaces)
	lines := strings.Split(value, "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
