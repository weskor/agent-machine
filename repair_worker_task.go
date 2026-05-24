package main

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func repairWorkerTaskReconciliation(workspaceRoot, taskKey string) error {
	taskKey = strings.TrimSpace(taskKey)
	if taskKey == "" {
		return fmt.Errorf("repair worker task: task key is required")
	}
	store, stateDBPath := commandScopedStateStore(context.Background(), workspaceRoot, "repair-worker-task")
	if store == nil {
		return fmt.Errorf("SQLite state store unavailable for worker task repair at %s", stateDBPath)
	}
	defer store.Close()
	task, err := store.RequeueReconciliationNeededWorkerTask(context.Background(), taskKey, "operator_requeued_reconciliation_task", time.Now().UTC())
	if err != nil {
		return err
	}
	log("requeued reconciliation-needed worker task %s role=%s issue=%s", task.TaskKey, task.Role, emptyAsNA(task.IssueKey))
	return nil
}
