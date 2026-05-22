package main

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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
