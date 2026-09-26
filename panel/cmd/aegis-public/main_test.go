// [INPUT]: 依赖 startReservationExpiryWorker，依赖 platform/sourcetest 按名取 startReservationExpiryWorker 与 run 的源码
// [OUTPUT]: 对外提供 TestReservationExpiryWorkerStopsAndJoinsOnCancellation、TestPublicProcessCancelsExpiryWorkerBeforeResourceCleanup
// [POS]: cmd/aegis-public 的进程生命周期契约：预留过期循环可取消可 join，停机次序为取消、join、返回
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package main

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
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
	pkg := sourcetest.Load(t, ".")
	worker := pkg.Decl("startReservationExpiryWorker")
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

	source := pkg.Decl("run")
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
