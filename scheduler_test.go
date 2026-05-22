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

func TestContinuousSchedulerDefaultsWorkLaneCapacityToOne(t *testing.T) {
	var concurrent atomic.Int32
	var maxConcurrent atomic.Int32
	var calls atomic.Int32

	scheduler := continuousScheduler{
		maxCycles: 1,
		lanes: []continuousLane{
			{
				name: "work",
				run: func() (bool, error) {
					current := concurrent.Add(1)
					for {
						max := maxConcurrent.Load()
						if current <= max || maxConcurrent.CompareAndSwap(max, current) {
							break
						}
					}
					calls.Add(1)
					defer concurrent.Add(-1)
					return true, nil
				},
			},
		},
	}

	if err := scheduler.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("work lane calls = %d, want 1", calls.Load())
	}
	if maxConcurrent.Load() != 1 {
		t.Fatalf("max concurrent work lane calls = %d, want 1", maxConcurrent.Load())
	}
}

func TestContinuousSchedulerRunsConfiguredWorkLaneCapacity(t *testing.T) {
	const capacity = 3
	allStarted := make(chan struct{})
	allowDone := make(chan struct{})
	var started atomic.Int32
	var completed atomic.Int32

	scheduler := continuousScheduler{
		maxCycles: 1,
		lanes: []continuousLane{
			{
				name:          "work",
				maxConcurrent: capacity,
				run: func() (bool, error) {
					if started.Add(1) == capacity {
						close(allStarted)
					}
					<-allowDone
					completed.Add(1)
					return true, nil
				},
			},
		},
	}

	done := make(chan error, 1)
	go func() { done <- scheduler.run(context.Background()) }()

	select {
	case <-allStarted:
	case <-time.After(time.Second):
		t.Fatalf("started %d work lane calls, want %d", started.Load(), capacity)
	}
	close(allowDone)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop after configured capacity completed")
	}
	if completed.Load() != capacity {
		t.Fatalf("completed work lane calls = %d, want %d", completed.Load(), capacity)
	}
}

func TestContinuousSchedulerConfiguredCapacityPreventsDuplicateDispatch(t *testing.T) {
	var lockHeld atomic.Bool
	var didWork atomic.Int32
	var duplicates atomic.Int32
	var secondIssue atomic.Bool
	start := make(chan struct{})
	allowDone := make(chan struct{})

	scheduler := continuousScheduler{
		maxCycles: 1,
		lanes: []continuousLane{
			{
				name:          "work",
				maxConcurrent: 2,
				run: func() (bool, error) {
					<-start
					if !lockHeld.CompareAndSwap(false, true) {
						duplicates.Add(1)
						if secondIssue.CompareAndSwap(false, true) {
							didWork.Add(1)
							return true, nil
						}
						return false, nil
					}
					defer lockHeld.Store(false)
					didWork.Add(1)
					<-allowDone
					return true, nil
				},
			},
		},
	}

	done := make(chan error, 1)
	go func() { done <- scheduler.run(context.Background()) }()
	close(start)

	for deadline := time.After(time.Second); duplicates.Load() == 0; {
		select {
		case <-deadline:
			t.Fatal("second dispatch did not observe authoritative duplicate lock")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	close(allowDone)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop after duplicate was skipped")
	}
	if didWork.Load() != 2 || duplicates.Load() != 1 {
		t.Fatalf("didWork=%d duplicates=%d, want capacity filled after one duplicate skip", didWork.Load(), duplicates.Load())
	}
}
