package node

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"
)

// fakeNow 是可拨的时钟，给装失败的退避重试用。
type fakeNow struct{ t time.Time }

func (f *fakeNow) now() time.Time          { return f.t }
func (f *fakeNow) advance(d time.Duration) { f.t = f.t.Add(d) }

func TestApplyRetryDelaySchedule(t *testing.T) {
	for attempts, want := range map[int]time.Duration{
		1: time.Minute, 2: 2 * time.Minute, 3: 4 * time.Minute, 4: 5 * time.Minute, 9: 5 * time.Minute,
	} {
		got := applyRetryDelay(attempts)
		if got < want*9/10 || got > want*11/10 {
			t.Errorf("第 %d 次失败后等 %s，期望 %s ±10%%", attempts, got, want)
		}
	}
}

// 签名通道：新发布装不上、旧发布仍在服务时，按 1、2、4…分钟退避重试，没到点
// 不试；故障消失后的下一次重试装上。
//
// 原先 failedSigned.key == key 就再也不试（审计 E2：端口一度被占、随后释放，节点
// 停在旧端口，面板以为它在新端口，用户全部断线）。
func TestSignedFailedReleaseRetriedWithBackoff(t *testing.T) {
	kernel := &preservingRejectCore{userTableCore: newUserTableCore(), badPorts: map[int]bool{18099: true}}
	n, fake := newSignedFixture(t, kernel)
	clock := &fakeNow{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	n.now = clock.now
	ctx := context.Background()

	n.syncOnce(ctx)
	fake.publish(releaseBad, 2, 18099)
	n.syncOnce(ctx) // 第一次失败
	calls := kernel.applyCalls
	for i := 0; i < 3; i++ {
		clock.advance(15 * time.Second)
		n.syncOnce(ctx)
	}
	if kernel.applyCalls != calls {
		t.Fatalf("一分钟内重试了 %d 次，期望不试", kernel.applyCalls-calls)
	}
	clock.advance(70 * time.Second) // 过了第一档
	n.syncOnce(ctx)
	if kernel.applyCalls != calls+1 {
		t.Fatalf("到点没有重试：ApplyInbound %d 次，期望 %d", kernel.applyCalls, calls+1)
	}
	clock.advance(70 * time.Second) // 第二档是 2 分钟，还没到
	n.syncOnce(ctx)
	if kernel.applyCalls != calls+1 {
		t.Fatal("第二档还没到就重试了")
	}
	delete(kernel.badPorts, 18099) // 故障消失
	clock.advance(80 * time.Second)
	n.syncOnce(ctx)
	if kernel.port != 18099 || n.failedSigned != nil {
		t.Fatalf("故障消失后的重试没有装上：port=%d failed=%v", kernel.port, n.failedSigned)
	}
	if got := countPhase(fake.phases(releaseBad), "failed"); got != 1 {
		t.Fatalf("重试期间 failed 上报 %d 次，期望 1 次", got)
	}
	if got := countPhase(fake.phases(releaseBad), "switched"); got != 1 {
		t.Fatalf("重试成功后 switched 上报 %d 次，期望 1 次", got)
	}
}

// 兼容通道同理：ETag 已记下、旧配置仍在服务时，到点才作废 ETag 重拉重试。
func TestCompatFailedConfigRetriedWithBackoff(t *testing.T) {
	n, kernel, fake := newResyncParts(t)
	rejecting := &rejectingCore{userTableCore: kernel, badPorts: map[int]bool{18099: true}}
	n.kernel = rejecting
	clock := &fakeNow{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	n.now = clock.now
	ctx := context.Background()

	n.syncOnce(ctx)
	fake.setConfig(18099, `"cfg-2"`)
	n.syncOnce(ctx)
	if kernel.port != 18080 || n.compatFailure == nil {
		t.Fatalf("坏配置之后旧配置没有保留：port=%d failure=%v", kernel.port, n.compatFailure)
	}
	if status, reason := n.runtimeHealth(); status != runtimeDegraded || reason != reasonApplyFailed {
		t.Fatalf("旧配置保留时上报 %s/%s，期望 degraded/%s", status, reason, reasonApplyFailed)
	}
	served := fake.configServed
	clock.advance(30 * time.Second)
	n.syncOnce(ctx)
	if fake.configServed != served {
		t.Fatal("没到点就作废 ETag 重拉了")
	}
	delete(rejecting.badPorts, 18099)
	clock.advance(40 * time.Second)
	n.syncOnce(ctx)
	if kernel.port != 18099 || n.compatFailure != nil {
		t.Fatalf("到点重试没有装上：port=%d failure=%v", kernel.port, n.compatFailure)
	}
	if status, _ := n.runtimeHealth(); status != runtimeRunning {
		t.Fatalf("装上之后仍报 %s", status)
	}
}

// 真 NativeCore：新发布的端口被别的进程占着，旧发布继续服务、上报 degraded 并带
// 端口占用原因；对方释放端口后，下一次重试装上。
func TestSignedPortOccupiedThenReleasedRecoversOnRetry(t *testing.T) {
	kernel := newNativeKernel(t)
	n, fake := newSignedFixture(t, kernel)
	clock := &fakeNow{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	n.now = clock.now
	ctx := context.Background()

	oldPort := freeTCPPort(t)
	fake.publish(releaseGood, 1, oldPort)
	n.syncOnce(ctx)
	if !n.started {
		t.Fatal("首个发布没有装上")
	}
	occupier, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	newPort := occupier.Addr().(*net.TCPAddr).Port
	fake.publish(releaseBad, 2, newPort)
	n.syncOnce(ctx)
	if n.failedSigned == nil {
		_ = occupier.Close()
		t.Fatal("端口被占时新发布居然装上了")
	}
	status, reason := n.runtimeHealth()
	if want := "port_in_use:" + strconv.Itoa(newPort) + "/tcp:other"; status != runtimeDegraded || reason != want {
		_ = occupier.Close()
		t.Fatalf("上报 %s/%s，期望 degraded/%s", status, reason, want)
	}
	if conn, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(oldPort)); err != nil {
		t.Fatalf("新发布装不上时旧端口不在服务：%v", err)
	} else {
		_ = conn.Close()
	}

	_ = occupier.Close() // 对方释放
	clock.advance(2 * time.Minute)
	n.syncOnce(ctx)
	if n.failedSigned != nil {
		t.Fatalf("释放后重试仍失败：%v", n.lastApplyErr)
	}
	conn, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(newPort))
	if err != nil {
		t.Fatalf("重试装上后新端口不在监听：%v", err)
	}
	_ = conn.Close()
	if status, _ := n.runtimeHealth(); status != runtimeRunning {
		t.Fatalf("恢复后仍报 %s", status)
	}
}
