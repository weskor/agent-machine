package main

import (
	"context"
	"strings"
	"sync"
	"time"
)

func runContinuous(client linearClient, wf workflow, config runnerConfig, maxCycles int) error {
	log("mode=continuous; lanes=merge,work; project=%s; states=%s", config.ProjectSlug, strings.Join(config.ActiveStates, ", "))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	scheduler := continuousScheduler{
		maxCycles: maxCycles,
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
	lanes     []continuousLane
	maxCycles int
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
			if err := runContinuousLane(ctx, lane, s.maxCycles); err != nil {
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

func runContinuousLane(ctx context.Context, lane continuousLane, maxCycles int) error {
	cycles := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		log("lane=%s cycle=%d starting", lane.name, cycles+1)
		didWork, err := lane.run()
		if err != nil {
			return err
		}
		cycles++
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

// runOne executes a single Linear issue attempt, including optional review
// handoff. It returns false when there is no eligible issue to process.
