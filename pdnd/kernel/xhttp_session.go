package kernel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// xhttpStreamUpChunk 是 stream-up 上行流每次读入、排进上行队列的块大小。
const xhttpStreamUpChunk = 16 << 10

// xhttpUnattachedGrace：会话建起来后这么久还没接上下行 GET 就回收（Xray 同为 30 秒）。
// 只送上行包、始终不来取下行的会话，否则会一直占着上游连接。
var xhttpUnattachedGrace = 30 * time.Second

// xhttpReconnectGrace：显式 packet-up / stream-down 节点的下行 GET 断开后，等客户端
// 重连下行的时长（Pandora 的下行续接能力）。超时不来就回收会话。
const xhttpReconnectGrace = 30 * time.Second

var errXHTTPSessionReaped = errors.New("xhttp 会话已回收：下行没有接上或已断开")

// xhttpDownlinkGrace 是下行 GET 断开后保留会话的时长。auto / stream-up 照 Xray：
// 下行 GET 结束即连接结束，立刻回收；显式 packet-up / stream-down 保留续接窗口。
func xhttpDownlinkGrace(mode XHTTPMode) time.Duration {
	switch mode {
	case XHTTPPacketUp, XHTTPStreamDown:
		return xhttpReconnectGrace
	}
	return 0
}

// xhttpSession 是跨多个 HTTP 请求的一条 XHTTP 连接（packet-up / stream-up 共用），
// vless / vmess 共用。
type xhttpSession struct {
	duplex *XHTTPPacketDuplex
	once   sync.Once
	ctx    context.Context
	cancel context.CancelFunc

	mu        sync.Mutex
	downlinks int
	reap      *time.Timer
	// uplink 是这个会话已认定的上行形态（0 未定、packet-up、stream-up）：一个会话
	// 只许一种上行，stream-up 只许一条。否则知道会话 id 的第三方能往别人的上行
	// 里插数据（审查 X5；Xray 对第二条 stream-up 回 409）。
	uplink XHTTPRequestKind
}

// claimUplink 登记一次上行请求；与已认定的上行形态冲突时返回 errXHTTPConflict。
func (s *xhttpSession) claimUplink(kind XHTTPRequestKind) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.uplink == 0:
		s.uplink = kind
		return nil
	case s.uplink == XHTTPRequestPacket && kind == XHTTPRequestPacket:
		return nil
	default:
		return errXHTTPConflict
	}
}

func newXHTTPSession(duplex *XHTTPPacketDuplex, ctx context.Context, cancel context.CancelFunc) *xhttpSession {
	s := &xhttpSession{duplex: duplex, ctx: ctx, cancel: cancel}
	s.reap = time.AfterFunc(xhttpUnattachedGrace, s.reapIfDetached)
	return s
}

// attachDownlink 登记一个正在服务的下行 GET，返回的函数在 GET 结束时调用。
func (s *xhttpSession) attachDownlink(grace time.Duration) func() {
	s.mu.Lock()
	s.downlinks++
	if s.reap != nil {
		s.reap.Stop()
		s.reap = nil
	}
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		s.downlinks--
		if s.downlinks > 0 {
			s.mu.Unlock()
			return
		}
		if grace <= 0 {
			s.mu.Unlock()
			s.shutdown()
			return
		}
		s.reap = time.AfterFunc(grace, s.reapIfDetached)
		s.mu.Unlock()
	}
}

func (s *xhttpSession) reapIfDetached() {
	s.mu.Lock()
	detached := s.downlinks == 0
	s.mu.Unlock()
	if detached {
		s.shutdown()
	}
}

// shutdown 结束会话：取消会话 worker，并关掉上下行队列唤醒所有等待方。
func (s *xhttpSession) shutdown() {
	s.cancel()
	_ = s.duplex.Uplink.Close(errXHTTPSessionReaped)
	_ = s.duplex.Downlink.Close(errXHTTPSessionReaped)
}

// stopReaper 在会话正常结束时停掉回收定时器。
func (s *xhttpSession) stopReaper() {
	s.mu.Lock()
	if s.reap != nil {
		s.reap.Stop()
		s.reap = nil
	}
	s.mu.Unlock()
}

// serveXHTTPSessionRequest 处理会话型 XHTTP 请求，vless / vmess 共用：
//
//   - 下行 GET：把会话下行队列里的数据按序写回响应体；
//   - packet-up 上行包：整包读完按线上序号排进上行队列；
//   - stream-up 上行流：请求体按块排进上行队列（序号由本端分配），请求体读完即
//     上行半关闭。
//
// open 取（必要时创建并启动）该会话；grace 是下行 GET 断开后保留会话的时长。
func serveXHTTPSessionRequest(ctx context.Context, session XHTTPSession, grace time.Duration, open func() (*xhttpSession, error)) error {
	switch session.Kind {
	case XHTTPRequestDownlink:
		xs, err := open()
		if err != nil {
			return err
		}
		detach := xs.attachDownlink(grace)
		defer detach()
		for {
			packet, readErr := xs.duplex.Downlink.Read(ctx)
			if readErr != nil {
				// 下行响应早已开始，任何结束原因（会话结束、回收、请求取消）都只能
				// 体现为响应体结束；再写 HTTP 错误只会把错误文本混进下行数据。
				return nil
			}
			if _, writeErr := session.Writer.Write(packet.Payload); writeErr != nil {
				return writeErr
			}
			if flusher, ok := session.Writer.(interface{ Flush() }); ok {
				flusher.Flush()
			}
		}
	case XHTTPRequestStreamUp:
		xs, err := open()
		if err != nil {
			return err
		}
		if err := xs.claimUplink(XHTTPRequestStreamUp); err != nil {
			return err
		}
		// 认下这条上行之后才回 200，第二条拿到的是 409。
		if flusher, ok := session.Writer.(http.Flusher); ok {
			flusher.Flush()
		}
		err = pumpXHTTPStreamUp(ctx, session.Body, xs.duplex.Uplink)
		if err == io.EOF {
			return nil
		}
		return err
	case XHTTPRequestPacket:
		if session.Seq == "" {
			return fmt.Errorf("xhttp packet uplink sequence is required")
		}
		seq, err := strconv.ParseUint(session.Seq, 10, 64)
		if err != nil {
			return fmt.Errorf("xhttp packet uplink sequence invalid: %w", err)
		}
		payload := session.Payload
		if payload == nil {
			if payload, err = io.ReadAll(session.Body); err != nil {
				return err
			}
		}
		xs, err := open()
		if err != nil {
			return err
		}
		if err := xs.claimUplink(XHTTPRequestPacket); err != nil {
			return err
		}
		return xs.duplex.Uplink.Push(XHTTPPacket{Seq: seq, Payload: payload})
	default:
		return fmt.Errorf("xhttp 请求类型 %d 不是会话请求", session.Kind)
	}
}

// pumpXHTTPStreamUp 把 stream-up 的请求体按块排进上行队列，直到请求体读完
// （上行半关闭，返回 io.EOF）或出错。无论哪种结束都关上行：中途失败（请求被
// 取消、AppendWait 等位置时被打断）若不关，会话的读端会永远等下一块。
func pumpXHTTPStreamUp(ctx context.Context, body io.Reader, uplink *XHTTPPacketQueue) error {
	buf := make([]byte, xhttpStreamUpChunk)
	for {
		n, readErr := body.Read(buf)
		if n > 0 {
			// AppendWait 会复制负载，buf 可以复用；队列满时在这里等，等于给上行限速。
			if err := uplink.AppendWait(ctx, buf[:n]); err != nil {
				_ = uplink.Close(err)
				return err
			}
		}
		if readErr != nil {
			_ = uplink.Close(readErr)
			return readErr
		}
	}
}
