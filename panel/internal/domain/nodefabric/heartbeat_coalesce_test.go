package nodefabric

import (
	"testing"
	"time"
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
	c.written(key, in, t0)

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
	c.written(key, beat(1), t0) // 立即写带了一点
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
	c.putBack(rows)
	if back := c.take(t0.Add(63 * time.Second)); len(back) != 1 {
		t.Fatalf("failed batch was not requeued: %+v", back)
	}
	// 失败的那批放回前已有更新的一拍立即写过：不放回
	c.written(key, beat(4), t0.Add(70*time.Second))
	c.putBack(rows)
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
