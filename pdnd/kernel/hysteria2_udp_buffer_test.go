package kernel

import (
	"net"
	"testing"

	"github.com/aegispanel/nodeagent/outbound"
	"golang.org/x/net/ipv4"
)

// bufferRecordingBatch 是只记缓冲申请的带检查批量接口替身。
type bufferRecordingBatch struct {
	local      net.Addr
	read, sent int
}

func (b *bufferRecordingBatch) WriteBatch(ms []ipv4.Message, _ int) (int, error) { return len(ms), nil }
func (b *bufferRecordingBatch) ReadBatch([]ipv4.Message, int) (int, error)       { return 0, net.ErrClosed }
func (b *bufferRecordingBatch) LocalAddr() net.Addr                              { return b.local }
func (b *bufferRecordingBatch) SetReadBuffer(n int) error                        { b.read = n; return nil }
func (b *bufferRecordingBatch) SetWriteBuffer(n int) error                       { b.sent = n; return nil }

type batchProviderConn struct {
	net.PacketConn
	batch *bufferRecordingBatch
}

func (c batchProviderConn) UDPBatch() outbound.UDPBatchConn { return c.batch }

// 出站 UDP socket 的收发缓冲与 QUIC 监听同口径：默认拦私网时经出站给的带检查
// 接口申请（不拿裸 socket），私网放开时直接对裸 socket 申请。
func TestHy2UpstreamRequestsQUICSizedBuffers(t *testing.T) {
	if hy2UDPSocketBuffer != quicSocketBufferWant {
		t.Fatalf("出站缓冲 %d 与 QUIC 监听 %d 不同口径", hy2UDPSocketBuffer, quicSocketBufferWant)
	}
	pc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	recorder := &bufferRecordingBatch{local: pc.LocalAddr()}
	u := newHy2UDPUpstream(batchProviderConn{PacketConn: pc, batch: recorder})
	if u.raw != nil {
		t.Fatal("带检查的出站不该交出裸 socket")
	}
	if recorder.read != quicSocketBufferWant || recorder.sent != quicSocketBufferWant {
		t.Fatalf("带检查的出站申请的缓冲 收=%d 发=%d，期望各 %d", recorder.read, recorder.sent, quicSocketBufferWant)
	}
}
