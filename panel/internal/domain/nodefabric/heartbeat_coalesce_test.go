package nodefabric

import (
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

// 合并的条件：见过、材料字段没变、离上一拍不到 hbCoalesceMaxGap。任何一条不满足都立即写。
func TestHeartbeatCoalescerOffersOnlyUnchangedRecentBeats(t *testing.T) {
	c := newHeartbeatCoalescer()
	key := hbKey("t", "n")
	t0 := time.Unix(1_700_000_000, 0)
	in := HeartbeatInput{AgentVersion: "a", RuntimeStatus: "running", CPUCores: 2, Metrics: &Metrics{CPUBasisPoints: 10}}
	pk := []byte("pk")

	if c.offer(key, in, t0, pk) {
		t.Fatal("first beat of an unseen node was buffered")
	}
	c.written(key, in, t0, pk)

	next := in
	next.Metrics = &Metrics{CPUBasisPoints: 20} // 探针值不算材料字段
	if !c.offer(key, next, t0.Add(30*time.Second), pk) {
		t.Fatal("unchanged beat 30s later was not buffered")
	}
	degraded := in
	degraded.RuntimeStatus, degraded.RuntimeReason = "degraded", "not_started"
	if c.offer(key, degraded, t0.Add(40*time.Second), pk) {
		t.Fatal("runtime status change was buffered")
	}
	for _, change := range []func(*HeartbeatInput){
		func(h *HeartbeatInput) { h.AgentVersion = "b" },
		func(h *HeartbeatInput) { h.ConfigSigningKeyID = "k2" },
		func(h *HeartbeatInput) { h.ConfigVersion = 7 },
		func(h *HeartbeatInput) { h.AppliedGeneration = 3 },
		func(h *HeartbeatInput) { h.MemoryMB = 4096 },
		func(h *HeartbeatInput) { h.MetricsPartial = true },
	} {
		changed := in
		change(&changed)
		if c.offer(key, changed, t0.Add(45*time.Second), pk) {
			t.Fatalf("material change was buffered: %+v", changed)
		}
	}
	// 上一次收下的是 30 秒那拍：90 秒那拍离它 60 秒，立即写
	if c.offer(key, in, t0.Add(90*time.Second), pk) {
		t.Fatal("beat after a gap of hbCoalesceMaxGap was buffered")
	}
}

// 探针点降采样：缓冲路径上至少隔 hbMetricsEvery 一点；批量写取走后清空，失败放回。
func TestHeartbeatCoalescerDownsamplesMetricsAndRequeues(t *testing.T) {
	c := newHeartbeatCoalescer()
	key := hbKey("t", "n")
	t0 := time.Unix(1_700_000_000, 0)
	beat := func(cpu int) HeartbeatInput {
		return HeartbeatInput{AgentVersion: "a", Metrics: &Metrics{CPUBasisPoints: cpu}}
	}
	c.written(key, beat(1), t0, []byte("pk")) // 立即写带了一点
	if !c.offer(key, beat(2), t0.Add(30*time.Second), []byte("pk")) {
		t.Fatal("beat not buffered")
	}
	rows := c.take(t0.Add(31 * time.Second))
	if len(rows) != 1 || rows[0].p.metrics != nil || !rows[0].p.at.Equal(t0.Add(30*time.Second)) {
		t.Fatalf("30s after a metrics point the buffered beat must carry no point: %+v", rows)
	}
	if !c.offer(key, beat(3), t0.Add(60*time.Second), []byte("pk")) {
		t.Fatal("beat not buffered")
	}
	rows = c.take(t0.Add(61 * time.Second))
	if len(rows) != 1 || rows[0].p.metrics == nil || rows[0].p.metrics.CPUBasisPoints != 3 ||
		rows[0].tenantID != "t" || rows[0].nodeID != "n" {
		t.Fatalf("60s after the last point the beat must carry one: %+v", rows)
	}
	if again := c.take(t0.Add(62 * time.Second)); len(again) != 0 {
		t.Fatalf("take returned the same beats twice: %+v", again)
	}
	c.putBack(rows, t0.Add(63*time.Second))
	if back := c.take(t0.Add(63 * time.Second)); len(back) != 1 {
		t.Fatalf("failed batch was not requeued: %+v", back)
	}
	// 失败的那批放回前已有更新的一拍立即写过：不放回
	c.written(key, beat(4), t0.Add(70*time.Second), []byte("pk"))
	c.putBack(rows, t0.Add(63*time.Second))
	if stale := c.take(t0.Add(71 * time.Second)); len(stale) != 0 {
		t.Fatalf("an older failed beat overwrote a newer immediate write: %+v", stale)
	}
	// 久不来的节点被忘掉，下一拍立即写
	c.take(t0.Add(70*time.Second + hbForgetAfter))
	if c.offer(key, beat(5), t0.Add(70*time.Second+hbForgetAfter+time.Second), []byte("pk")) {
		t.Fatal("forgotten node's beat was buffered")
	}
}

// 库里的心跳年龄最坏 = 心跳间隔（30 秒 +10%）+ 合并窗口，必须小于离线判定，健康节点才不会被误判离线。
func TestHeartbeatCoalesceWindowStaysInsideStaleThreshold(t *testing.T) {
	if worst := 33*time.Second + hbFlushInterval + hbFlushTimeout; worst >= NodeStaleAfter {
		t.Fatalf("worst persisted heartbeat age %s reaches NodeStaleAfter %s", worst, NodeStaleAfter)
	}
	if hbCoalesceMaxGap > NodeStaleAfter-hbFlushInterval {
		t.Fatalf("hbCoalesceMaxGap %s lets a buffered beat age past NodeStaleAfter", hbCoalesceMaxGap)
	}
}

// 审查 #1：签名门槛写（监听不健康时走 HeartbeatSigned）也经 Service.heartbeat 更新合并器。
// A→B→A：running 立即写 → degraded 走签名门槛写 → 监听恢复后 running 那拍必须立即写，
// 不能因为合并器还记着最早那份 running 就只进缓冲、让库停在 degraded。
func TestHeartbeatCoalescerFollowsEveryImmediateWrite(t *testing.T) {
	svc := NewService(nil, nil)
	svc.hb = newHeartbeatCoalescer()
	const tenant, node = "t", "0190a5e2-8f00-7000-8000-0000000000aa"
	key, pk := hbKey(tenant, node), []byte("pk")
	t0 := time.Unix(1_700_000_000, 0)
	running := HeartbeatInput{AgentVersion: "pandora-native", RuntimeStatus: "running"}
	degraded := running
	degraded.RuntimeStatus, degraded.RuntimeReason = "degraded", "not_started"

	svc.heartbeatWritten(tenant, node, running, t0, pk, true)                      // Confirmed 立即写
	svc.heartbeatWritten(tenant, node, degraded, t0.Add(27*time.Second), pk, true) // Signed 立即写
	if svc.hb.offer(key, running, t0.Add(54*time.Second), pk) {
		t.Fatal("recovered beat buffered after a signed-path write changed the stored material")
	}
	svc.heartbeatWritten(tenant, node, running, t0.Add(54*time.Second), pk, true)
	if !svc.hb.offer(key, running, t0.Add(84*time.Second), pk) {
		t.Fatal("unchanged beat after the recovery write was not buffered")
	}
	// 写失败（或节点不存在）就忘掉：下一拍立即写
	svc.heartbeatWritten(tenant, node, running, t0.Add(90*time.Second), pk, false)
	if svc.hb.offer(key, running, t0.Add(114*time.Second), pk) {
		t.Fatal("beat buffered after a failed immediate write")
	}
	// 兼容通道 /status 写过运行状态：忘掉
	svc.heartbeatWritten(tenant, node, running, t0.Add(120*time.Second), pk, true)
	svc.forgetHeartbeat(tenant, node)
	if svc.hb.offer(key, running, t0.Add(150*time.Second), pk) {
		t.Fatal("beat buffered after the compat status endpoint rewrote the runtime state")
	}
}

// 换了身份（重新引导、两阶段接入会同时改写版本与资产）之后第一拍立即写；
// 材料一直没变也每 hbMaterialRefresh 立即写一次（兜住没挂上的写入口）。
func TestHeartbeatCoalescerRewritesOnNewIdentityAndPeriodically(t *testing.T) {
	c := newHeartbeatCoalescer()
	key := hbKey("t", "n")
	t0 := time.Unix(1_700_000_000, 0)
	in := HeartbeatInput{AgentVersion: "a"}
	c.written(key, in, t0, []byte("old-key"))
	if c.offer(key, in, t0.Add(30*time.Second), []byte("new-key")) {
		t.Fatal("first beat with a new identity was buffered")
	}
	if c.offer(key, in, t0.Add(30*time.Second), nil) {
		t.Fatal("beat without an identity was buffered")
	}
	at := t0
	for i := 0; ; i++ {
		at = at.Add(30 * time.Second)
		if !c.offer(key, in, at, []byte("old-key")) {
			if at.Sub(t0) < hbMaterialRefresh {
				t.Fatalf("beat %d at +%s written immediately before the refresh point", i, at.Sub(t0))
			}
			break
		}
		if at.Sub(t0) > hbMaterialRefresh+time.Minute {
			t.Fatal("no forced immediate write after hbMaterialRefresh")
		}
	}
}

// 合并器的记录只能由 Service.heartbeat 按写的结果更新：签名门槛写、身份已复核、无签名三条
// 入口都经它，成功与失败两个分支都有。
func TestEveryImmediateHeartbeatWriteUpdatesCoalescer(t *testing.T) {
	pkg := sourcetest.Load(t, ".")
	if src := pkg.Decl("Service.heartbeat"); strings.Count(src, "s.heartbeatWritten(") != 2 {
		t.Fatal("Service.heartbeat must record success and failure in the coalescer")
	}
	for _, decl := range []string{"Service.HeartbeatConfirmed", "Service.HeartbeatSigned", "Service.Heartbeat"} {
		src := pkg.Decl(decl)
		if strings.Contains(src, ".written(") || strings.Contains(src, ".forget(") {
			t.Fatalf("%s updates the coalescer itself; leave it to Service.heartbeat", decl)
		}
	}
	if !strings.Contains(pkg.Decl("Service.ReportRuntimeStatus"), "s.forgetHeartbeat(") {
		t.Fatal("the compat status endpoint must make the coalescer forget the node")
	}
}
