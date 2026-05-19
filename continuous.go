package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/weskor/pi-symphony/internal/state"
)

func runContinuous(client linearClient, wf workflow, config runnerConfig, maxCycles int) error {
	log("mode=continuous; lanes=merge,work; project=%s; states=%s", config.ProjectSlug, strings.Join(config.ActiveStates, ", "))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	recordHeartbeat := daemonHeartbeatRecorder(ctx, config)

	scheduler := continuousScheduler{
		maxCycles:       maxCycles,
		recordHeartbeat: recordHeartbeat,
		lanes: []continuousLane{
			{
				name:       "merge",
				idleDelay:  30 * time.Second,
				continuous: true,
				run: func() (bool, error) {
					doneIssues, err := client.issueIdentifiersByState(config.ProjectSlug, config.DoneState)
					if err != nil {
						return false, err
					}
					if err := cleanupWorkspaces(config.WorkspaceRoot, cleanupOptions{Apply: true, DoneIssues: doneIssues}); err != nil {
						return false, err
					}
					return true, mergeApprovedPRs(client, config)
				},
			},
			{
				name:      "work",
				idleDelay: 60 * time.Second,
				run: func() (bool, error) {
					return runOne(client, wf, config)
				},
			},
		},
	}
	return scheduler.run(ctx)
}

type continuousLane struct {
	name       string
	idleDelay  time.Duration
	continuous bool
	run        func() (bool, error)
}

type continuousScheduler struct {
	lanes           []continuousLane
	maxCycles       int
	recordHeartbeat func(continuousHeartbeat)
}

func (s continuousScheduler) run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errs := make(chan error, len(s.lanes))
	var wg sync.WaitGroup
	for _, lane := range s.lanes {
		lane := lane
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := runContinuousLane(ctx, lane, s.maxCycles, s.recordHeartbeat); err != nil {
				errs <- err
				cancel()
			}
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		select {
		case err := <-errs:
			return err
		default:
			return nil
		}
	case err := <-errs:
		cancel()
		<-done
		return err
	}
}

type continuousHeartbeat struct {
	LaneName    string
	CycleNumber int
	Success     bool
	Err         error
	At          time.Time
}

func runContinuousLane(ctx context.Context, lane continuousLane, maxCycles int, recordHeartbeat func(continuousHeartbeat)) error {
	cycles := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		log("lane=%s cycle=%d starting", lane.name, cycles+1)
		didWork, err := lane.run()
		cycleNumber := cycles + 1
		if err != nil {
			recordContinuousHeartbeat(recordHeartbeat, continuousHeartbeat{LaneName: lane.name, CycleNumber: cycleNumber, Err: err, At: time.Now().UTC()})
			return err
		}
		cycles++
		recordContinuousHeartbeat(recordHeartbeat, continuousHeartbeat{LaneName: lane.name, CycleNumber: cycles, Success: true, At: time.Now().UTC()})
		if maxCycles > 0 && cycles >= maxCycles {
			log("lane=%s completed %d continuous cycle(s)", lane.name, cycles)
			return nil
		}

		delay := time.Duration(0)
		if lane.continuous || !didWork {
			delay = lane.idleDelay
		}
		if delay > 0 {
			if !didWork {
				log("lane=%s idle; sleeping %s", lane.name, delay)
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(delay):
			}
		}
	}
}

func recordContinuousHeartbeat(recordHeartbeat func(continuousHeartbeat), heartbeat continuousHeartbeat) {
	if recordHeartbeat != nil {
		recordHeartbeat(heartbeat)
	}
}

func daemonHeartbeatRecorder(ctx context.Context, config runnerConfig) func(continuousHeartbeat) {
	dbPath := state.DefaultDBPath(config.WorkspaceRoot)
	if dbPath == "" {
		return nil
	}
	store, err := state.Open(ctx, dbPath)
	if err != nil {
		log("SQLite daemon heartbeat degraded: open path=%s error=%q", dbPath, err.Error())
		return nil
	}
	processID := daemonProcessID()
	return func(heartbeat continuousHeartbeat) {
		lastError := ""
		if heartbeat.Err != nil {
			lastError = heartbeat.Err.Error()
		}
		var lastSuccessAt time.Time
		if heartbeat.Success {
			lastSuccessAt = heartbeat.At
		}
		if err := store.UpsertDaemonHeartbeat(ctx, state.DaemonHeartbeat{ProcessID: processID, LaneName: heartbeat.LaneName, WorkflowPath: config.WorkflowPath, CycleNumber: heartbeat.CycleNumber, LastSuccessAt: lastSuccessAt, LastError: lastError, RecoveryRequired: heartbeat.Err != nil, UpdatedAt: heartbeat.At}); err != nil {
			log("SQLite daemon heartbeat degraded: lane=%s cycle=%d error=%q", heartbeat.LaneName, heartbeat.CycleNumber, err.Error())
		}
	}
}

func daemonProcessID() string {
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = "unknown-host"
	}
	return fmt.Sprintf("%s:%d", hostname, os.Getpid())
}

// runOne executes a single Linear issue attempt, including optional review
// handoff. It returns false when there is no eligible issue to process.
