package kernel

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/sagernet/sing/common/auth"
	M "github.com/sagernet/sing/common/metadata"
)

// recordingStream 冒充 AnyTLS 子流：记下适配器回的 SYNACK（成功 / 失败），Close
// 时关 done。另一端由测试拿着。
type recordingStream struct {
	net.Conn
	mu        sync.Mutex
	successes int
	failures  []error
	closeOnce sync.Once
	done      chan struct{}
}

func newRecordingStream(t *testing.T) (*recordingStream, net.Conn) {
	t.Helper()
	local, peer := net.Pipe()
	t.Cleanup(func() { _ = peer.Close() })
	return &recordingStream{Conn: local, done: make(chan struct{})}, peer
}

func (r *recordingStream) HandshakeSuccess() error {
	r.mu.Lock()
	r.successes++
	r.mu.Unlock()
	return nil
}

func (r *recordingStream) HandshakeFailure(err error) error {
	r.mu.Lock()
	r.failures = append(r.failures, err)
	r.mu.Unlock()
	return nil
}

func (r *recordingStream) Close() error {
	r.closeOnce.Do(func() { close(r.done) })
	return r.Conn.Close()
}

func (r *recordingStream) waitClosed(t *testing.T) {
	t.Helper()
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		t.Fatal("子流 5 秒内没有被关")
	}
}

// replies 返回 (成功次数, 失败列表)。
func (r *recordingStream) replies() (int, []error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.successes, append([]error(nil), r.failures...)
}

// assertRefused：只回过一次失败、是那句中性文字、没回成功。
func assertRefused(t *testing.T, r *recordingStream, exit string) {
	t.Helper()
	r.waitClosed(t)
	ok, failures := r.replies()
	if ok != 0 || len(failures) != 1 || !errors.Is(failures[0], errAnyTLSStreamRefused) {
		t.Fatalf("%s：成功 %d 次、失败 %v，应只回一次中性失败 %q", exit, ok, failures, errAnyTLSStreamRefused)
	}
}

func startAnyTLSExitAdapter(t *testing.T, users ...core.User) *anyTLSAdapter {
	t.Helper()
	port := reserveTCPAndUDPPort(t)
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "anytls", Listen: "127.0.0.1", Port: port, Raw: map[string]any{}}}
	created, err := newAnyTLSAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	a := created.(*anyTLSAdapter)
	if err := a.Start(context.Background(), spec, AdapterHooks{DataPlane: &hysteriaEchoPlane{}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	if err := a.AddUsers(users); err != nil {
		t.Fatal(err)
	}
	return a
}

var anyTLSExitTarget = M.ParseSocksaddr("127.0.0.1:80")

func anyTLSExitSource(ip string) M.Socksaddr { return M.ParseSocksaddr(ip + ":40000") }

// 各失败出口都经 cmdSYNACK 回同一句中性失败（拨号失败与线上文字见 interop 的
// dial-failure-reported）；拨号成功只回成功。「会话已撤销」只在认证与登记之间
// 用户被删的竞态窗口里出现，没有确定性触发办法，与这里的出口共用同一个 defer。
func TestAnyTLSStreamExitsReplySYNACK(t *testing.T) {
	user := core.User{ID: 6401, UUID: "anytls-exit-secret", DeviceLimit: 1}
	userCtx := auth.ContextWithUser(context.Background(), user.UUID)

	t.Run("no-authenticated-user", func(t *testing.T) {
		a := startAnyTLSExitAdapter(t, user)
		stream, _ := newRecordingStream(t)
		a.NewConnectionEx(context.Background(), stream, anyTLSExitSource("192.0.2.1"), anyTLSExitTarget, nil)
		assertRefused(t, stream, "没有认证用户")
	})

	t.Run("user-removed", func(t *testing.T) {
		a := startAnyTLSExitAdapter(t, user)
		if err := a.DelUsers([]string{user.UUID}); err != nil {
			t.Fatal(err)
		}
		// 会话认证时用户还在，开流时已被删（空闲会话不在登记表里，删用户不会关它）。
		stream, _ := newRecordingStream(t)
		a.NewConnectionEx(userCtx, stream, anyTLSExitSource("192.0.2.1"), anyTLSExitTarget, nil)
		assertRefused(t, stream, "用户已删")
	})

	t.Run("device-limit", func(t *testing.T) {
		a := startAnyTLSExitAdapter(t, user)
		held, heldPeer := newRecordingStream(t)
		a.NewConnectionEx(userCtx, held, anyTLSExitSource("192.0.2.1"), anyTLSExitTarget, nil)
		// 第一台设备的流已拨号成功、在转发：等它回了成功再开第二台。
		deadline := time.Now().Add(5 * time.Second)
		for ok, _ := held.replies(); ok == 0; ok, _ = held.replies() {
			if time.Now().After(deadline) {
				t.Fatal("第一台设备的流 5 秒内没回成功")
			}
			time.Sleep(10 * time.Millisecond)
		}
		second, _ := newRecordingStream(t)
		a.NewConnectionEx(userCtx, second, anyTLSExitSource("198.51.100.1"), anyTLSExitTarget, nil)
		assertRefused(t, second, "设备超限")
		if ok, failures := held.replies(); ok != 1 || len(failures) != 0 {
			t.Fatalf("在用的流成功 %d 次、失败 %v，应只回一次成功", ok, failures)
		}
		_ = heldPeer.Close()
		held.waitClosed(t)
		_ = a.Close() // 等全部子流的 goroutine 退出，defer 都已执行
		// 转发结束后的出口不再补发失败：这里冒充的流不像 fork 的 Stream 那样只报
		// 一次，要求适配器自己只在回成功之前的出口回失败。
		if _, failures := held.replies(); len(failures) != 0 {
			t.Fatalf("转发结束后又回了失败 %v", failures)
		}
	})

	t.Run("adapter-closed", func(t *testing.T) {
		a := startAnyTLSExitAdapter(t, user)
		_ = a.Close()
		stream, _ := newRecordingStream(t)
		a.NewConnectionEx(userCtx, stream, anyTLSExitSource("192.0.2.1"), anyTLSExitTarget, nil)
		assertRefused(t, stream, "入站已关")
	})
}
