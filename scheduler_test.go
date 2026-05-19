package main

import (
	"context"
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
