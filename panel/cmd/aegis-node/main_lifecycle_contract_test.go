package main

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

// aegis-node 的停机顺序与 public / admin 一致（w5retain，按 w3core 报告）：信号只让 HTTP 停接新请求、
// 等在途请求跑完；后台循环（nonce 清理）的 ctx 在 RunContext 返回之后才取消、限时 join，
// 再交给 defer 关资源。原先循环直接挂在信号 ctx 上，一收到信号就与在途请求同时被拆。
func TestNodeWorkersCancelAfterServerDrains(t *testing.T) {
	run := sourcetest.Load(t, ".").Decl("run")
	for _, want := range []string{
		"sigCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)",
		"ctx, stop := context.WithCancel(context.Background())",
		"stopOnEarlySignal := context.AfterFunc(sigCtx, stop)",
		"runNoncePurge(ctx, nodeService, log)",
	} {
		if !strings.Contains(run, want) {
			t.Fatalf("aegis-node lifecycle missing %q", want)
		}
	}
	if strings.Contains(run, "ctx, stop := signal.NotifyContext(") {
		t.Fatal("aegis-node background workers must not hang directly off the signal context")
	}
	early := strings.Index(run, "stopOnEarlySignal()")
	serve := strings.Index(run, "serverErr := server.RunContext(sigCtx")
	if early < 0 || serve < 0 || early > serve {
		t.Fatal("aegis-node must hand worker cancellation to the shutdown order before serving on sigCtx")
	}
	after := run[serve:]
	stop := strings.Index(after, "stop()")
	wait := strings.Index(after, "drainErr := waitForNodeWorkers(&workers, cfg.ShutdownTimeout)")
	skip := strings.Index(after, "closeResourcesOnReturn = false")
	ret := strings.Index(after, "return errors.Join(serverErr, drainErr)")
	if stop < 0 || wait < 0 || skip < 0 || ret < 0 || !(stop < wait && wait < skip && skip < ret) {
		t.Fatal("aegis-node must cancel, bounded-drain workers, skip unsafe cleanup on timeout, then return")
	}
}

func TestWaitForNodeWorkersTimesOut(t *testing.T) {
	var workers sync.WaitGroup
	workers.Add(1)
	err := waitForNodeWorkers(&workers, 1)
	workers.Done()
	if !errors.Is(err, errNodeWorkerDrainTimeout) {
		t.Fatalf("drain error = %v, want %v", err, errNodeWorkerDrainTimeout)
	}
}
