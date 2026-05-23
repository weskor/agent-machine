package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/weskor/pi-symphony/internal/state"
)

func TestContinuousSchedulerRunsMergeLaneWhileWorkLaneIsBusy(t *testing.T) {
	workStarted := make(chan struct{})
	allowWorkDone := make(chan struct{})
	mergeRan := make(chan struct{}, 1)
	var workStartedCount atomic.Int32

	scheduler := continuousScheduler{
		maxCycles: 1,
		lanes: []continuousLane{
			{
				name: "merge",
				run: func() (bool, error) {
					mergeRan <- struct{}{}
					return true, nil
				},
			},
			{
				name: "work",
				run: func() (bool, error) {
					if workStartedCount.Add(1) == 1 {
						close(workStarted)
					}
					<-allowWorkDone
					return true, nil
				},
			},
		},
	}

	done := make(chan error, 1)
	go func() { done <- scheduler.run(context.Background()) }()

	select {
	case <-workStarted:
	case <-time.After(time.Second):
		t.Fatal("work lane did not start")
	}
	select {
	case <-mergeRan:
	case <-time.After(time.Second):
		t.Fatal("merge lane did not run while work lane was busy")
	}
	close(allowWorkDone)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop after max cycles")
	}
}

func TestContinuousSchedulerRecordsLaneHeartbeats(t *testing.T) {
	var mu sync.Mutex
	var heartbeats []continuousHeartbeat
	scheduler := continuousScheduler{
		maxCycles: 1,
		recordHeartbeat: func(heartbeat continuousHeartbeat) {
			mu.Lock()
			defer mu.Unlock()
			heartbeats = append(heartbeats, heartbeat)
		},
		lanes: []continuousLane{
			{name: "merge", run: func() (bool, error) { return true, nil }},
			{name: "work", run: func() (bool, error) { return false, nil }},
		},
	}

	if err := scheduler.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	seen := map[string]bool{}
	for _, heartbeat := range heartbeats {
		if heartbeat.CycleNumber == 1 && heartbeat.Success && heartbeat.Err == nil {
			seen[heartbeat.LaneName] = true
		}
	}
	if !seen["merge"] || !seen["work"] {
		t.Fatalf("heartbeats = %+v; want successful merge and work lane heartbeats", heartbeats)
	}
}

func TestContinuousWorkerTaskWrapsLaneWithClaimAndCompletion(t *testing.T) {
	ctx := context.Background()
	store, err := state.Open(ctx, state.DefaultDBPath(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ran := false
	didWork, err := runContinuousWorkerTask(ctx, store, continuousWorkerTask{TaskKey: "continuous:merge", Role: "merge", LaneName: "merge", LeaseName: "lane:merge"}, func() (bool, error) {
		ran = true
		tasks, err := store.WorkerTasks(ctx, "merge")
		if err != nil {
			t.Fatal(err)
		}
		if len(tasks) != 1 || tasks[0].Status != "claimed" {
			t.Fatalf("worker task during run = %+v; want claimed", tasks)
		}
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ran || !didWork {
		t.Fatalf("ran=%v didWork=%v; want true true", ran, didWork)
	}
	tasks, err := store.WorkerTasks(ctx, "merge")
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Status != "completed" {
		t.Fatalf("worker task after run = %+v; want completed", tasks)
	}
	events, err := store.Events(ctx, state.EventFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	got := eventTypes(events)
	want := []string{state.EventWorkerTaskQueued, state.EventWorkerTaskClaimed, state.EventWorkerTaskCompleted}
	if !equalStrings(got, want) {
		t.Fatalf("events = %v; want %v", got, want)
	}
}

func TestContinuousWorkerTaskRecordsFailures(t *testing.T) {
	ctx := context.Background()
	store, err := state.Open(ctx, state.DefaultDBPath(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runErr := errors.New("lane failed")
	didWork, err := runContinuousWorkerTask(ctx, store, continuousWorkerTask{TaskKey: "continuous:scheduler", Role: "scheduler", LaneName: "work", LeaseName: "lane:work"}, func() (bool, error) {
		return false, runErr
	})
	if !errors.Is(err, runErr) {
		t.Fatalf("error = %v; want %v", err, runErr)
	}
	if didWork {
		t.Fatal("didWork = true; want false")
	}
	tasks, err := store.WorkerTasks(ctx, "scheduler")
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Status != "failed" {
		t.Fatalf("worker task after failure = %+v; want failed", tasks)
	}
	events, err := store.Events(ctx, state.EventFilter{Type: state.EventWorkerTaskFailed, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("failed events = %+v; want one", events)
	}
}

func eventTypes(events []state.Event) []string {
	out := make([]string, 0, len(events))
	for _, event := range events {
		out = append(out, event.Type)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
