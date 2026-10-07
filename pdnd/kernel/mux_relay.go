package kernel

import (
	"io"
	"net"
	"sync/atomic"

	"github.com/aegispanel/nodeagent/core"
)

// relayMuxTCP 转发 VLESS / VMess mux 里的一条 TCP 子流。
//
// 原先这里是两个 SpeedLimitedCopy goroutine 加一个 WaitGroup：字节数直接丢弃
// （mux 里的 TCP 流量一字节都没计费），上游先结束时客户端收不到 End、要等它自己
// 关，上游不结束时则永远挂着。现在走 core.Relay：随搬随计到用户计数器；上游
// 结束即给客户端发 End（mux 的 End 就是这条子流的「半关闭」）；客户端 End 后
// 上游按单向收尾计时收尾。
func relayMuxTCP(sessions *userSessions, user core.User, opt core.RelayOptions, pipeR *io.PipeReader, w io.Writer, end func() error, upstream net.Conn) {
	counter := sessions.retain(user.ID)
	defer sessions.release(counter)
	client := &muxRelayStream{r: pipeR, w: w, end: end}
	opt.Up, opt.Down = &counter.up, &counter.down
	core.Relay(client, upstream, opt)
	_ = client.CloseWrite()
}

// muxRelayStream 是 mux 子流在转发里的客户端一端：读子流的管道、写 mux 帧，
// CloseWrite 发一次 End。
type muxRelayStream struct {
	r     *io.PipeReader
	w     io.Writer
	end   func() error
	ended atomic.Bool
}

func (m *muxRelayStream) Read(p []byte) (int, error)  { return m.r.Read(p) }
func (m *muxRelayStream) Write(p []byte) (int, error) { return m.w.Write(p) }

// Close 只断开读管道（会话循环往里写会失败，随即移除子流），End 由 CloseWrite 发。
func (m *muxRelayStream) Close() error { return m.r.CloseWithError(net.ErrClosed) }

// CloseWrite 给客户端发 End，只发一次。
func (m *muxRelayStream) CloseWrite() error {
	if m.ended.CompareAndSwap(false, true) {
		return m.end()
	}
	return nil
}
