package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weskor/pi-symphony/internal/state"
)

func TestReviewWorkerCollectsEvidenceAndRunsSemanticReview(t *testing.T) {
	t.Cleanup(resetReviewWorkerHooks)
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-156")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	candidate := &issue{ID: "issue-156", Identifier: "CAG-156", Title: "Review worker"}
	started := time.Date(2026, 5, 23, 11, 30, 0, 0, time.UTC)
	prURL := "https://github.com/acme/repo/pull/156"
	var gotValidation []string
	collectReviewEvidenceForWorker = func(ctx context.Context, config runnerConfig, gotCandidate *issue, gotWorkspace, gotPRURL string, scopeResult scopeGuardResult, validation []string) (reviewEvidence, error) {
		if config.WorkspaceRoot != root || gotCandidate.Identifier != candidate.Identifier || gotWorkspace != workspace || gotPRURL != prURL {
			t.Fatalf("unexpected evidence input config=%+v candidate=%+v workspace=%q pr=%q", config, gotCandidate, gotWorkspace, gotPRURL)
		}
		gotValidation = append([]string(nil), validation...)
		return reviewEvidence{ChecksStatus: "success", ChecksSummary: "go-ci=COMPLETED/SUCCESS"}, nil
	}
	runReviewForWorker = func(ctx context.Context, provider, command, gotWorkspace string, gotCandidate *issue, gotPRURL string, env map[string]string, timeout time.Duration, evidence *reviewEvidence) (*reviewResult, error) {
		if provider != "" || command != "pi review" || gotWorkspace != workspace || gotCandidate.Identifier != candidate.Identifier || gotPRURL != prURL || env["GITHUB_TOKEN"] != "token" || timeout != time.Minute {
			t.Fatalf("unexpected review input provider=%q command=%q workspace=%q candidate=%+v pr=%q env=%+v timeout=%s", provider, command, gotWorkspace, gotCandidate, gotPRURL, env, timeout)
		}
		if evidence == nil || evidence.ChecksStatus != "success" {
			t.Fatalf("review evidence = %+v; want success evidence", evidence)
		}
		return &reviewResult{Status: "passed", Usage: &usage{TotalTokens: 6}}, nil
	}

	result, err := reviewWorker{client: linearClient{}, config: runnerConfig{WorkspaceRoot: root, PiCommand: "pi run", ReviewCommand: "pi review", Budget: runBudget{ReviewTimeout: time.Minute}}, stateStore: store, candidate: candidate, workspace: workspace, branch: expectedWorkspaceBranch(candidate.Identifier), progressStarted: started, startedAt: started, prURL: prURL, githubEnv: map[string]string{"GITHUB_TOKEN": "token"}, githubAuth: "github_app_installation", validation: []string{"go test ./..."}}.Execute(context.Background())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Terminal || result.Review == nil || result.Review.Status != "passed" {
		t.Fatalf("result = %+v; want non-terminal passed review", result)
	}
	if len(gotValidation) != 1 || gotValidation[0] != "go test ./..." {
		t.Fatalf("validation passed to evidence = %#v", gotValidation)
	}
	snapshot, err := readRunProgress(root, candidate.Identifier)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Phase != "reviewing" || snapshot.PRURL != prURL || snapshot.ChecksStatus != "success" {
		t.Fatalf("progress = %+v; want reviewing success progress", snapshot)
	}
}

func TestReviewWorkerHonorsCanceledContextBeforeEvidenceCollection(t *testing.T) {
	t.Cleanup(resetReviewWorkerHooks)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var collected bool
	collectReviewEvidenceForWorker = func(context.Context, runnerConfig, *issue, string, string, scopeGuardResult, []string) (reviewEvidence, error) {
		collected = true
		return reviewEvidence{}, nil
	}

	result, err := reviewWorker{
		config:    runnerConfig{WorkspaceRoot: t.TempDir(), ReviewCommand: "pi review"},
		candidate: &issue{ID: "issue-176", Identifier: "CAG-176", Title: "Canceled review"},
		workspace: t.TempDir(),
		prURL:     "https://github.com/acme/repo/pull/176",
	}.Execute(ctx)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Execute() error = %v; want context canceled", err)
	}
	if !result.Terminal {
		t.Fatalf("result = %+v; want terminal canceled result", result)
	}
	if collected {
		t.Fatal("review collected evidence after context cancellation")
	}
}

func TestReviewWorkerExecutesPersistedPayloadBoundary(t *testing.T) {
	t.Cleanup(resetReviewWorkerHooks)
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-177")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	candidate := &issue{ID: "issue-177", Identifier: "CAG-177", Title: "Review payload boundary"}
	candidate.Team.ID = "team-177"
	started := time.Date(2026, 5, 23, 21, 30, 0, 0, time.UTC)
	prURL := "https://github.com/acme/repo/pull/177"
	readReviewPendingPayloadForExecution = func(workspaceRoot, issueIdentifier string) (reviewPendingPayload, error) {
		payload, err := readReviewPendingPayload(workspaceRoot, issueIdentifier)
		if err != nil {
			return reviewPendingPayload{}, err
		}
		payload.Validation = append(payload.Validation, "payload-boundary-read")
		return payload, nil
	}
	collectReviewEvidenceForWorker = func(ctx context.Context, config runnerConfig, gotCandidate *issue, gotWorkspace, gotPRURL string, scopeResult scopeGuardResult, validation []string) (reviewEvidence, error) {
		snapshot, err := readRunProgress(root, candidate.Identifier)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Phase != runProgressPhaseReviewPending || snapshot.Status != runProgressPhaseReviewPending || snapshot.NextAction != "run_semantic_review" || snapshot.PRURL != prURL || snapshot.ReviewPayloadPath == "" {
			t.Fatalf("progress before review side effects = %+v; want review_pending", snapshot)
		}
		if gotCandidate.Identifier != candidate.Identifier || gotCandidate.Team.ID != "team-177" || gotWorkspace != workspace || gotPRURL != prURL {
			t.Fatalf("unexpected review input candidate=%+v workspace=%q pr=%q", gotCandidate, gotWorkspace, gotPRURL)
		}
		if len(validation) != 2 || validation[1] != "payload-boundary-read" {
			t.Fatalf("validation = %#v; want side effects from persisted payload read", validation)
		}
		return reviewEvidence{ChecksStatus: "success", ChecksSummary: "go-ci=COMPLETED/SUCCESS"}, nil
	}
	runReviewForWorker = func(context.Context, string, string, string, *issue, string, map[string]string, time.Duration, *reviewEvidence) (*reviewResult, error) {
		return &reviewResult{Status: "passed"}, nil
	}

	result, err := reviewWorker{
		client:          linearClient{},
		config:          runnerConfig{WorkspaceRoot: root, PiCommand: "pi run", ReviewCommand: "pi review"},
		stateStore:      store,
		candidate:       candidate,
		workspace:       workspace,
		branch:          expectedWorkspaceBranch(candidate.Identifier),
		progressStarted: started,
		startedAt:       started,
		prURL:           prURL,
		githubEnv:       map[string]string{"GITHUB_TOKEN": "token"},
		githubAuth:      "github_app_installation",
		validation:      []string{"input-validation"},
	}.Execute(context.Background())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Terminal || result.Review == nil || result.Review.Status != "passed" {
		t.Fatalf("result = %+v; want non-terminal passed review", result)
	}
}

func TestReviewWorkerRecordsNotReadyWithoutRunningReview(t *testing.T) {
	t.Cleanup(resetReviewWorkerHooks)
	root := t.TempDir()
	workspace := filepath.Join(root, "CAG-156")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(context.Background(), state.DefaultDBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	candidate := &issue{ID: "issue-156", Identifier: "CAG-156", Title: "Review worker"}
	prURL := "https://github.com/acme/repo/pull/156"
	collectReviewEvidenceForWorker = func(context.Context, runnerConfig, *issue, string, string, scopeGuardResult, []string) (reviewEvidence, error) {
		return reviewEvidence{ChecksStatus: "pending", ChecksSummary: "go-ci=IN_PROGRESS"}, nil
	}
	runReviewForWorker = func(context.Context, string, string, string, *issue, string, map[string]string, time.Duration, *reviewEvidence) (*reviewResult, error) {
		t.Fatal("review command should not run when checks are not ready")
		return nil, nil
	}

	started := time.Date(2026, 5, 23, 11, 30, 0, 0, time.UTC)
	result, err := reviewWorker{client: linearClient{}, config: runnerConfig{WorkspaceRoot: root, PiCommand: "pi run", ReviewCommand: "pi review"}, stateStore: store, candidate: candidate, workspace: workspace, branch: expectedWorkspaceBranch(candidate.Identifier), progressStarted: started, startedAt: started, prURL: prURL}.Execute(context.Background())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.Terminal {
		t.Fatalf("result = %+v; want terminal review_not_ready", result)
	}
	snapshot, err := readRunProgress(root, candidate.Identifier)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Phase != "review_not_ready" || snapshot.Outcome != "waiting_for_checks" || snapshot.NextAction != "wait_for_github_checks_then_retry" || !strings.Contains(snapshot.Error, "review not ready") {
		t.Fatalf("progress = %+v; want waiting-for-checks progress", snapshot)
	}
	data, err := os.ReadFile(filepath.Join(workspace, ".pi-symphony-run.json"))
	if err != nil {
		t.Fatal(err)
	}
	var record runRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if record.Status != runAttemptStatusReviewNotReady || !strings.Contains(record.Error, "review not ready") {
		t.Fatalf("run record = %+v; want review_not_ready with not-ready error", record)
	}
}
