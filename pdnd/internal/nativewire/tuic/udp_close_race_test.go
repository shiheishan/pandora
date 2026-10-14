package tuic

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// countingHandler 只数被交出的 UDP 会话。
type countingHandler struct{ udp atomic.Int32 }

func (*countingHandler) NewConnectionEx(context.Context, net.Conn, M.Socksaddr, M.Socksaddr, N.CloseHandlerFunc) {
}

func (h *countingHandler) NewPacketConnectionEx(_ context.Context, conn N.PacketConn, _ M.Socksaddr, _ M.Socksaddr, _ N.CloseHandlerFunc) {
	h.udp.Add(1)
}

// 连接断开、closeUDPSessions 拷走会话列表之后再到的 UDP 包（TUIC quic 中继模式在
// 单向流 goroutine 里建会话）不能再建会话：没人会关它，要等 udpTimeout 才收尾。
// 时序用顺序调用固定：先关、后到包。
func TestNoUDPSessionAfterClose(t *testing.T) {
	handler := &countingHandler{}
	s := &serverSession[string]{
		Service:    &Service[string]{handler: handler, udpTimeout: time.Minute},
		ctx:        context.Background(),
		udpConnMap: make(map[uint16]*udpPacketConn),
	}
	// 对照：关之前已有的会话被关掉。（这里不经 handleUDPMessage 建：建会话要真 QUIC
	// 连接取对端地址，测试里是 nil，正好用来断言关之后不再走到建会话那一步。）
	before := newUDPPacketConn(context.Background(), nil, false, true, func() {}, 0)
	s.udpConnMap[7] = before
	s.closeUDPSessions()
	if !isDone(before) {
		t.Fatal("closeUDPSessions 没关掉已有会话")
	}
	s.udpAccess.Lock()
	delete(s.udpConnMap, 7) // 测试里 onDestroy 是空函数，手动摘掉
	s.udpAccess.Unlock()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("关之后到的包仍去建会话：%v", r)
		}
	}()
	message := &udpMessage{sessionID: 7, fragmentTotal: 1, destination: M.ParseSocksaddr("203.0.113.1:53"), data: buf.As([]byte("x"))}
	s.handleUDPMessage(message, false)
	s.udpAccess.RLock()
	n := len(s.udpConnMap)
	s.udpAccess.RUnlock()
	if n != 0 {
		t.Fatalf("关之后到的包又建了 %d 个会话", n)
	}
	time.Sleep(50 * time.Millisecond)
	if got := handler.udp.Load(); got != 0 {
		t.Fatalf("关之后仍交给上层 %d 个 UDP 会话", got)
	}
}

func isDone(c *udpPacketConn) bool {
	select {
	case <-c.ctx.Done():
		return true
	default:
		return false
	}
}
