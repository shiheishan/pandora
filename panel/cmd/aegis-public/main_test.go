package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

func TestReservationExpiryWorkerStopsAndJoinsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	wait := startReservationExpiryWorker(ctx, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	cancel()

	done := make(chan struct{})
	go func() {
		wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reservation expiry worker did not stop and join after cancellation")
	}
}

func TestPublicProcessCancelsExpiryWorkerBeforeResourceCleanup(t *testing.T) {
	sourceBytes, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(sourceBytes)
	workerStart := strings.Index(source, "func startReservationExpiryWorker(")
	if workerStart < 0 {
		t.Fatal("reservation expiry worker helper is missing")
	}
	worker := source[workerStart:]
	for _, required := range []string{
		"select {",
		"case <-ctx.Done():",
		"context.WithTimeout(ctx, 20*time.Second)",
		"return workers.Wait",
	} {
		if !strings.Contains(worker, required) {
			t.Fatalf("expiry worker cancellation contract missing %q", required)
		}
	}

	runServer := strings.Index(source, "serverErr := server.RunContext(ctx")
	if runServer < 0 {
		t.Fatal("public process must run the server with the signal context")
	}
	afterServer := source[runServer:]
	stop := strings.Index(afterServer, "stop()")
	wait := strings.Index(afterServer, "waitReservationExpiry()")
	ret := strings.Index(afterServer, "return serverErr")
	if stop < 0 || wait < 0 || ret < 0 || !(stop < wait && wait < ret) {
		t.Fatal("public process must cancel, join expiry worker, then return for deferred cleanup")
	}
}
