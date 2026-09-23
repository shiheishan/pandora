package main

import (
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAdminWorkersShareSignalContextAndJoinBeforeCleanup(t *testing.T) {
	sourceBytes, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(sourceBytes)
	for _, required := range []string{
		"signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)",
		"serverErr := server.RunContext(ctx",
		"workers.Add(4)",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("admin process lifecycle contract missing %q", required)
		}
	}

	workersAt := strings.Index(source, "var workers sync.WaitGroup")
	serverAt := strings.Index(source, "serverErr := server.RunContext(ctx")
	if workersAt < 0 || serverAt <= workersAt {
		t.Fatal("admin worker/server lifecycle region is malformed")
	}
	workers := source[workersAt:serverAt]
	if got := strings.Count(workers, "defer workers.Done()"); got != 4 {
		t.Fatalf("worker Done count=%d want=4", got)
	}
	if got := strings.Count(workers, "context.WithTimeout(ctx,"); got != 4 {
		t.Fatalf("worker child-context count=%d want=4", got)
	}
	if got := strings.Count(workers, "case <-ctx.Done():"); got != 4 {
		t.Fatalf("worker cancellation branch count=%d want=4", got)
	}
	if strings.Contains(workers, "context.Background()") {
		t.Fatal("admin workers must not detach from the signal context")
	}

	afterServer := source[serverAt:]
	stop := strings.Index(afterServer, "stop()")
	wait := strings.Index(afterServer, "drainErr := waitForAdminWorkers(&workers, cfg.ShutdownTimeout)")
	skipClose := strings.Index(afterServer, "closeResourcesOnReturn = false")
	ret := strings.Index(afterServer, "return errors.Join(serverErr, drainErr)")
	if stop < 0 || wait < 0 || skipClose < 0 || ret < 0 ||
		!(stop < wait && wait < skipClose && skipClose < ret) {
		t.Fatal("admin process must cancel, bounded-drain workers, disable unsafe timeout cleanup, then return")
	}
	if !strings.Contains(afterServer, "admin background workers did not stop before shutdown deadline") {
		t.Fatal("admin process must report a clear worker drain timeout")
	}

	closeCalls := []string{"pool.Close()", "rdb.Close()", "rtHub.Close()"}
	closePositions := make([]int, 0, len(closeCalls))
	for _, closeCall := range closeCalls {
		at := strings.Index(source, closeCall)
		if at < 0 {
			t.Fatalf("admin resource cleanup missing %q", closeCall)
		}
		start := at - 120
		if start < 0 {
			start = 0
		}
		if !strings.Contains(source[start:at], "if closeResourcesOnReturn") {
			t.Fatalf("admin resource cleanup %q is not guarded on drain timeout", closeCall)
		}
		closePositions = append(closePositions, at)
	}
	if !(closePositions[0] < closePositions[1] && closePositions[1] < closePositions[2]) {
		t.Fatal("normal cleanup defers must remain registered pool, Redis, then hub for reverse close order")
	}
}

func TestWaitForAdminWorkersCompletes(t *testing.T) {
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		time.Sleep(5 * time.Millisecond)
	}()

	if err := waitForAdminWorkers(&workers, time.Second); err != nil {
		t.Fatalf("worker drain returned an error: %v", err)
	}
}

func TestWaitForAdminWorkersTimesOut(t *testing.T) {
	var workers sync.WaitGroup
	workers.Add(1)
	err := waitForAdminWorkers(&workers, 10*time.Millisecond)
	workers.Done()

	if !errors.Is(err, errAdminWorkerDrainTimeout) {
		t.Fatalf("worker drain error = %v, want %v", err, errAdminWorkerDrainTimeout)
	}
}
