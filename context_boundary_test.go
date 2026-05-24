package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"testing"
)

func TestWorkerContextBoundariesDoNotUseBackgroundContext(t *testing.T) {
	protected := map[string][]string{
		"cleanup.go": {
			"cleanupWorkspacesContext",
			"cleanupDecisions",
			"cleanupDecisionForWorkspace",
			"recordCleanupEventContext",
			"recordCleanupErrorContext",
			"removeDoneWorkspaceContext",
			"cleanupDecisionFromSQLite",
			"currentGitBranchContext",
		},
		"cleanup_task.go": {
			"scheduleCleanupWorkerTasks",
			"enqueueCleanupWorkerTask",
			"claimNextQueuedCleanupWorkerTask",
			"runQueuedCleanupWorkerTaskContext",
			"completeClaimedCleanupWorkerTask",
		},
		"candidate_selection.go": {
			"nextRunnableCandidateContext",
			"nextRunnableCandidateWithOptionsContext",
			"hasActiveImplementationWorkerTask",
			"hasActiveWorkerTask",
			"skipCandidateForSelectionOptionsContext",
			"reconcileCandidateForSelectionContext",
			"retryBackoffDecision",
			"emitCandidateEventContext",
		},
		"continuous.go": {
			"continuousLanes",
			"scheduleContinuousWorkerTasks",
			"recoverStaleWorkerTasks",
			"runClaimedAttemptBatchWithClaimerContext",
			"runContinuousWorkerTask",
			"recordContinuousWorkerResult",
			"acquireWorkerTaskLease",
			"startWorkerTaskSupervision",
			"renewWorkerTaskSupervision",
			"recordContinuousWorkerTaskEvent",
			"runContinuousLane",
			"daemonHeartbeatRecorder",
		},
		"handoff_worker_process.go": {
			"runHandoffPendingAttemptContext",
			"claimNextPRHandoffPendingAttemptContext",
			"executePRHandoffPendingAttempt",
			"claimNextHandoffPendingAttemptContext",
			"executeHandoffPendingAttempt",
		},
		"handoff.go": {
			"ensureRunnerPRHandoffFromInputContext",
			"executePRHandoffPendingPayloadContext",
			"executeRunnerPRHandoffContext",
			"writePRHandoffPendingStateContext",
			"validateAdvisoryPRForHandoff",
			"resolveHandoffPRByBranch",
		},
		"handoff_completion.go": {
			"completeAttemptHandoff",
			"executeHandoffPendingPayload",
			"executeAttemptHandoff",
			"writeHandoffPendingStateContext",
		},
		"implementation_worker_process.go": {
			"runImplementationAttemptBatchContext",
			"runQueuedImplementationAttemptBatchContext",
			"claimNextImplementationAttemptContext",
			"claimNextQueuedImplementationAttemptContext",
			"scheduleImplementationWorkerTasks",
			"scheduleNextImplementationWorkerTask",
			"prepareClaimedImplementationWorkerTask",
		},
		"internal/workspace/locks.go": {
			"AcquireContext",
			"acquireSQLiteLease",
			"reclaimDeadOwnerSQLiteLease",
			"HeartbeatContext",
			"CleanupStaleContext",
			"MirrorAcquireContext",
			"MirrorRenewContext",
			"MirrorReleaseContext",
			"withStateStoreContext",
		},
		"linear.go": {
			"firstCandidateContext",
			"candidatesContext",
			"issueIdentifiersByStateContext",
			"workflowStatesContext",
			"updateIssueStateContext",
			"createCommentContext",
			"issueByIdentifierContext",
		},
		"linear_status_worker.go": {
			"MoveToContext",
			"CommentContext",
		},
		"linear_status_worker_process.go": {
			"queueLinearStatusTransitionTask",
			"runLinearStatusTransitionTaskContext",
			"claimNextLinearStatusTransitionTask",
			"executeLinearStatusTransitionTask",
			"completeLinearStatusTask",
		},
		"merge.go": {
			"mergeApprovedPRsWithStoreContext",
			"scheduleMergeWorkerTasks",
			"runQueuedMergeWorkerTaskContext",
			"recordMergeEventContext",
			"recordMergeErrorContext",
			"feedbackAlreadyAddressedContext",
		},
		"reconciler.go": {
			"reconcileIssueContext",
			"reconcileIssueWithArtifactContext",
			"ReconcileIssueContext",
			"ReconcileIssueWithArtifactContext",
			"runReconciliationScanContext",
			"recordReconciliationNeededEventContext",
			"reconciliationFacts",
			"activeRunLease",
			"reconcileIssuesContext",
			"ReconcileIssuesContext",
		},
		"review.go": {
			"collectReviewEvidenceContext",
			"runReviewWithProviderContext",
		},
		"review_readiness.go": {
			"resumeReviewReadyRunContext",
		},
		"review_worker_process.go": {
			"runReviewReadyAttemptContext",
			"runReviewPendingAttemptContext",
			"claimNextReviewPendingAttemptContext",
			"executeReviewPendingAttempt",
			"claimNextQueuedReviewReadyAttemptContext",
			"scheduleReviewReadyWorkerTasks",
			"prepareClaimedReviewReadyWorkerTask",
			"claimNextReviewReadyAttemptContext",
			"executeReviewReadyAttemptContext",
		},
		"run_one.go": {
			"claimNextRunAttemptContext",
			"claimNextRunAttemptWithOptionsContext",
			"executeClaimedRunAttempt",
			"emitRunAttemptEventContext",
		},
		"selected_worker.go": {
			"runSelectedWorkerContext",
			"runStatusWorkerProcessContext",
			"runPlanWorkerProcessContext",
			"runCleanupWorkerProcessContext",
			"runMergeWorkerProcessContext",
			"runReconciliationWorkerProcessContext",
			"runReviewWorkerProcessContext",
			"runImplementationWorkerProcessContext",
			"runHandoffWorkerProcessContext",
			"runLinearStatusWorkerProcessContext",
			"runWorkWorkerProcessContext",
		},
		"workspace.go": {
			"ensureIsolatedWorkspaceContext",
			"writeRunRecordWithStateContext",
			"writeRunRecordWithCommandStateContext",
			"writeRunRecordWithStateFallbackContext",
			"stateStoreForRunRecordExportContext",
			"recordArtifactExportFailureContext",
			"mirrorRunRecordToStateContext",
		},
	}

	for path, funcs := range protected {
		t.Run(filepath.ToSlash(path), func(t *testing.T) {
			assertNoContextBackgroundInFunctions(t, path, funcs)
		})
	}
}

func assertNoContextBackgroundInFunctions(t *testing.T, path string, funcs []string) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	wanted := map[string]bool{}
	for _, name := range funcs {
		wanted[name] = false
	}
	var violations []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		if _, ok := wanted[fn.Name.Name]; !ok {
			continue
		}
		wanted[fn.Name.Name] = true
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if isContextBackgroundCall(call) {
				violations = append(violations, fset.Position(call.Pos()).String())
			}
			return true
		})
	}
	var missing []string
	for name, found := range wanted {
		if !found {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("protected function(s) missing from %s: %v", path, missing)
	}
	sort.Strings(violations)
	if len(violations) > 0 {
		t.Fatalf("context.Background() used inside protected worker context function(s): %v", violations)
	}
}

func isContextBackgroundCall(call *ast.CallExpr) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Background" {
		return false
	}
	ident, ok := selector.X.(*ast.Ident)
	return ok && ident.Name == "context"
}
