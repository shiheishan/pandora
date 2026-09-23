package kernel

import (
	"sync"
	"testing"

	M "github.com/sagernet/sing/common/metadata"
)

// testPlaneRecorder 记录假数据面每次收到的目标地址。
//
// 测试用的数据面通常把上游写死、不看调用方传进来的目标，于是协议实现把请求头里
// 的目标地址解析成垃圾也照样绿——trojan 曾按 SOCKS5 格式（VER|CMD|RSV|ATYP）读
// Trojan 请求头（CMD|ATYP|ADDR|PORT），解析出 1.187.127.0:1，单元测试全绿，直到
// 真实 mihomo 客户端对接才暴露。嵌入本类型并在 DialTCP/ListenUDP 里调用
// recordDial/recordListen，回环测试就能断言解析出的目标和客户端写的一致。
//
// 所有方法并发安全：数据面会被多条连接的 goroutine 同时调用。
type testPlaneRecorder struct {
	mu       sync.Mutex
	dialed   []M.Socksaddr
	listened []M.Socksaddr
}

func (r *testPlaneRecorder) recordDial(destination M.Socksaddr) {
	r.mu.Lock()
	r.dialed = append(r.dialed, destination)
	r.mu.Unlock()
}

func (r *testPlaneRecorder) recordListen(destination M.Socksaddr) {
	r.mu.Lock()
	r.listened = append(r.listened, destination)
	r.mu.Unlock()
}

// DialedTargets 返回 DialTCP 收到过的全部目标地址（按调用顺序）。
func (r *testPlaneRecorder) DialedTargets() []M.Socksaddr {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]M.Socksaddr(nil), r.dialed...)
}

// ListenedTargets 返回 ListenUDP 收到过的全部目标地址（按调用顺序）。
func (r *testPlaneRecorder) ListenedTargets() []M.Socksaddr {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]M.Socksaddr(nil), r.listened...)
}

// LastDialedTarget 返回最后一次 DialTCP 的目标，没有调用过则返回空串。
// 返回前做 Unwrap，把 ::ffff:127.0.0.1 这类 4in6 地址还原成 IPv4 写法，
// 断言里就能直接写客户端请求头里的那个地址。
func (r *testPlaneRecorder) LastDialedTarget() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.dialed) == 0 {
		return ""
	}
	return r.dialed[len(r.dialed)-1].Unwrap().String()
}

// LastListenedTarget 返回最后一次 ListenUDP 的目标，没有调用过则返回空串。
func (r *testPlaneRecorder) LastListenedTarget() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.listened) == 0 {
		return ""
	}
	return r.listened[len(r.listened)-1].Unwrap().String()
}

// recordingPlane 是嵌入了 testPlaneRecorder 的假数据面共同暴露的读取接口。
type recordingPlane interface {
	DialedTargets() []M.Socksaddr
	ListenedTargets() []M.Socksaddr
	LastDialedTarget() string
	LastListenedTarget() string
}

// assertDialedTarget 断言协议实现从请求头里解析出的 TCP 目标就是客户端写的那个。
func assertDialedTarget(t *testing.T, plane recordingPlane, want string) {
	t.Helper()
	if got := plane.LastDialedTarget(); got != want {
		t.Fatalf("dial target=%q want %q (all=%v)", got, want, plane.DialedTargets())
	}
}

// assertListenedTarget 断言协议实现解析出的 UDP 目标就是客户端写的那个。
func assertListenedTarget(t *testing.T, plane recordingPlane, want string) {
	t.Helper()
	if got := plane.LastListenedTarget(); got != want {
		t.Fatalf("listen target=%q want %q (all=%v)", got, want, plane.ListenedTargets())
	}
}
