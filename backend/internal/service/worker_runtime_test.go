package service

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWorkerRuntimeStopsLoopsAndWaitsForTasks(t *testing.T) {
	runtime := &workerRuntime{}
	ctx, started := runtime.start()
	if !started {
		t.Fatal("worker runtime should start")
	}
	loopStopped := make(chan struct{})
	if !runtime.goLoop(func(context.Context) {
		<-ctx.Done()
		close(loopStopped)
	}) {
		t.Fatal("worker loop should start")
	}
	taskRelease := make(chan struct{})
	if !runtime.goTask(func() { <-taskRelease }) {
		t.Fatal("worker task should start")
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := runtime.stop(stopCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stop should wait for the active task, got %v", err)
	}
	select {
	case <-loopStopped:
	case <-time.After(time.Second):
		t.Fatal("worker loop did not observe drain cancellation")
	}
	if runtime.activeTaskCount() != 1 {
		t.Fatalf("active task count = %d, want 1", runtime.activeTaskCount())
	}

	close(taskRelease)
	finalCtx, finalCancel := context.WithTimeout(context.Background(), time.Second)
	defer finalCancel()
	if err := runtime.stop(finalCtx); err != nil {
		t.Fatal(err)
	}
	if runtime.activeTaskCount() != 0 {
		t.Fatalf("active task count = %d, want 0", runtime.activeTaskCount())
	}
	if runtime.goTask(func() {}) {
		t.Fatal("drained runtime should reject new tasks")
	}
}

func TestTaskWritesAreRejectedDuringDrain(t *testing.T) {
	runtime := &workerRuntime{}
	runtime.start()
	runtime.beginDrain()
	svc := &Service{workers: runtime}

	if _, err := svc.CreateTask("user-1", CreateTaskRequest{}); !isAppErrorStatus(err, 503) {
		t.Fatalf("CreateTask during drain error = %v, want 503", err)
	}
	if _, err := svc.RetryTask("user-1", "task-1"); !isAppErrorStatus(err, 503) {
		t.Fatalf("RetryTask during drain error = %v, want 503", err)
	}
}

func isAppErrorStatus(err error, status int) bool {
	var appErr *AppError
	return errors.As(err, &appErr) && appErr.Status == status
}
