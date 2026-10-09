package main

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

func TestAdminWorkersShareSignalContextAndJoinBeforeCleanup(t *testing.T) {
	source := sourcetest.Load(t, ".").Decl("run")
	for _, required := range []string{
		"signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)",
		"serverErr := server.RunContext(sigCtx",
		"stopOnEarlySignal := context.AfterFunc(sigCtx, stop)",
		"workers.Add(8)",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("admin process lifecycle contract missing %q", required)
		}
	}

	workersAt := strings.Index(source, "var workers sync.WaitGroup")
	serverAt := strings.Index(source, "serverErr := server.RunContext(sigCtx")
	if workersAt < 0 || serverAt <= workersAt {
		t.Fatal("admin worker/server lifecycle region is malformed")
	}
	workers := source[workersAt:serverAt]
	if got := strings.Count(workers, "defer workers.Done()"); got != 8 {
		t.Fatalf("worker Done count=%d want=8", got)
	}
	if got := strings.Count(workers, "context.WithTimeout(ctx,"); got != 8 {
		t.Fatalf("worker child-context count=%d want=8", got)
	}
	if got := strings.Count(workers, "case <-ctx.Done():"); got != 8 {
		t.Fatalf("worker cancellation branch count=%d want=8", got)
	}
	if strings.Contains(workers, "context.Background()") {
		t.Fatal("admin workers must not detach from the signal context")
	}

	// 优雅关停：开服前解除「信号即取消后台」，HTTP 停完（在途请求跑完）才取消后台循环
	if early := strings.Index(source, "stopOnEarlySignal()"); early < 0 || early > serverAt {
		t.Fatal("admin process must hand worker cancellation to the shutdown order before serving")
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

// 工单回复通知（R115）缺的正是这一行装配：support 不接 notifier 时回复照常成功、通知静默不排
func TestAdminWiresTicketReplyNotifier(t *testing.T) {
	if !strings.Contains(sourcetest.Load(t, ".").Decl("run"), "supportSvc.SetReplyNotifier(notifySvc)") {
		t.Fatal("admin gateway must wire the ticket reply notifier into the support service")
	}
}

// 通知收件人哈希用专用盐（⑨）：主密钥不能直接当 HMAC key，且必须与 public 网关同一个盐
func TestAdminNotifyUsesRecipientSalt(t *testing.T) {
	run := sourcetest.Load(t, ".").Decl("run")
	if !strings.Contains(run, "notify.New(pool, log, crypto.NotifyRecipientSalt(cfg.MasterKey))") {
		t.Fatal("admin gateway must hash notification recipients with crypto.NotifyRecipientSalt")
	}
	if strings.Contains(run, "notify.New(pool, log, cfg.MasterKey") {
		t.Fatal("admin gateway must not use the master key itself as the notification salt")
	}
}

// 保留期清理（w1admin）：在线记录、探针点与流量小时汇总必须有定时清理接在 admin 网关的工作循环里
func TestAdminWiresRetentionPurge(t *testing.T) {
	run := sourcetest.Load(t, ".").Decl("run")
	for _, want := range []string{
		"nodeSvc.PurgeStaleAlive(sctx, middleware.DefaultTenantID)",
		"nodeSvc.PurgeMetrics(sctx, middleware.DefaultTenantID, nodefabric.MetricsRetentionHours)",
		"nodeSvc.PurgeTrafficRollups(sctx, middleware.DefaultTenantID)",
	} {
		if !strings.Contains(run, want) {
			t.Fatalf("admin gateway retention worker missing %q", want)
		}
	}
}
