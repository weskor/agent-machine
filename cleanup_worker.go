package main

import (
	"context"
	"os"
	"strings"

	orchstate "github.com/weskor/pi-symphony/internal/state"
)

type cleanupWorker struct {
	workspaceRoot string
	safeRoot      string
	options       cleanupOptions
	store         *orchstate.Store
	hasChanges    workspaceChangeChecker
}

func (w cleanupWorker) Execute(ctx context.Context) error {
	hasChanges := w.hasChanges
	if hasChanges == nil {
		hasChanges = workspaceHasChanges
	}
	log("mode=cleanup-workspaces; workspace_root=%s; policy=linear_done; apply=%t", w.workspaceRoot, w.options.Apply)
	recordCleanupEventContext(ctx, w.store, orchstate.EventCleanupStarted, cleanupResult{}, map[string]any{"workspace_root": w.safeRoot, "apply": w.options.Apply})
	entries, err := os.ReadDir(w.safeRoot)
	if err != nil {
		recordCleanupErrorContext(ctx, w.store, cleanupResult{}, err)
		return err
	}
	removed := 0
	kept := 0
	eligible := 0
	categories := map[string]int{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if entry.Name() == "state" {
			continue
		}
		workspace, err := safeWorkspacePath(w.safeRoot, entry.Name())
		if err != nil {
			recordCleanupErrorContext(ctx, w.store, cleanupResult{IssueIdentifier: entry.Name()}, err)
			return err
		}
		decision, err := cleanupDecisionForWorkspace(ctx, w.safeRoot, workspace, w.options.DoneIssues, w.store, hasChanges)
		if err != nil {
			recordCleanupErrorContext(ctx, w.store, decision, err)
			return err
		}
		recordCleanupEventContext(ctx, w.store, orchstate.EventCleanupCandidateFound, decision, map[string]any{"reason": decision.Reason, "category": decision.Category, "delete": decision.Delete})
		categories[decision.Category]++
		if !decision.Delete {
			kept++
			mirrorCleanupState(w.store, w.safeRoot, decision, false, cleanupDeletionResult(decision, "kept"), true)
			log("keep %s [%s]: %s", workspace, decision.Category, decision.Reason)
			continue
		}
		eligible++
		if !w.options.Apply {
			mirrorCleanupState(w.store, w.safeRoot, decision, true, "dry_run", true)
			log("would delete %s [%s]: %s", workspace, decision.Category, decision.Reason)
			continue
		}
		recordCleanupEventContext(ctx, w.store, orchstate.EventCleanupDeletionAttempted, decision, map[string]any{"reason": decision.Reason, "category": decision.Category})
		if err := assertSafeDeletePath(w.safeRoot, workspace); err != nil {
			mirrorCleanupState(w.store, w.safeRoot, decision, true, "failed", true)
			recordCleanupErrorContext(ctx, w.store, decision, err)
			return err
		}
		if err := os.RemoveAll(workspace); err != nil {
			mirrorCleanupState(w.store, w.safeRoot, decision, true, "failed", true)
			recordCleanupErrorContext(ctx, w.store, decision, err)
			return err
		}
		removed++
		mirrorCleanupState(w.store, w.safeRoot, decision, true, "deleted", false)
		recordCleanupEventContext(ctx, w.store, orchstate.EventCleanupDeletionSucceeded, decision, map[string]any{"reason": decision.Reason, "category": decision.Category})
		log("deleted %s [%s]: %s", workspace, decision.Category, decision.Reason)
	}
	if w.options.Apply {
		log("cleanup summary: deleted=%d eligible=%d kept=%d categories=%s", removed, eligible, kept, formatCleanupCategories(categories))
	} else {
		log("cleanup summary: eligible=%d kept=%d categories=%s; dry run only; pass --apply to delete workspaces for Done issues", eligible, kept, formatCleanupCategories(categories))
	}
	return nil
}
