package kernel

import (
	"net"
	"testing"

	"github.com/aegispanel/nodeagent/internal/udprecv"
	"github.com/aegispanel/nodeagent/outbound"
	"golang.org/x/net/ipv4"
)

// bufferRecordingBatch 是只记缓冲申请的带检查批量接口替身。
type bufferRecordingBatch struct {
	local      net.Addr
	read, sent int
}

func (b *bufferRecordingBatch) WriteBatch(ms []ipv4.Message, _ int) (int, error) { return len(ms), nil }
func (b *bufferRecordingBatch) Receiver() *udprecv.Receiver                      { return nil }
func (b *bufferRecordingBatch) LocalAddr() net.Addr                              { return b.local }
func (b *bufferRecordingBatch) SetReadBuffer(n int) error                        { b.read = n; return nil }
func (b *bufferRecordingBatch) SetWriteBuffer(n int) error                       { b.sent = n; return nil }

type batchProviderConn struct {
	net.PacketConn
	batch *bufferRecordingBatch
}

func (c batchProviderConn) UDPBatch() outbound.UDPBatchConn { return c.batch }

// 出站 UDP socket 申请 hy2UDPSocketBuffer：默认拦私网时经出站给的带检查接口申请
// （不拿裸 socket），私网放开时直接对裸 socket 申请。
func TestHy2UpstreamRequestsSocketBuffers(t *testing.T) {
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
	if recorder.read != hy2UDPSocketBuffer || recorder.sent != hy2UDPSocketBuffer {
		t.Fatalf("带检查的出站申请的缓冲 收=%d 发=%d，期望各 %d", recorder.read, recorder.sent, hy2UDPSocketBuffer)
	}
}
