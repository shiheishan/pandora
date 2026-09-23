package mkcp

import (
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// 监听器与拨号器：把 Connection 接到真的 UDP socket 上。
//
// mKCP 是多路复用的——服务端只开一个 UDP 端口，所有客户端的所有连接都从
// 这个端口进来，靠 (来源地址, 会话号) 区分。这和 TCP 一个 accept 一个 fd
// 的模型完全不同，会话表得自己维护。

// maxSessions 是单个监听器的会话数上限。取值远高于任何正常业务量，
// 它是防内存耗尽的兜底，不是容量规划。
const maxSessions = 65536

var (
	ErrListenerClosed = errors.New("mkcp: 监听器已关闭")
	// ErrSegmentTooLarge 表示单个段序列化后超过了 MTU。真发生了说明 MSS
	// 算错了，属于内部不变量被破坏，不该让它悄悄截断。
	ErrSegmentTooLarge = errors.New("mkcp: 段长度超过 MTU")
)

// flusher 让底层写入方知道一批段结束了，可以发出去。
//
// Connection 每次 flush 是一个天然的批次边界：那一轮要发的 ACK、数据、
// 心跳都在里面。攒成一个 UDP 包发比一段一个包省得多——ACK 段只有十几
// 字节，单独成包的话包头开销比载荷还大。
type flusher interface {
	Flush() error
}

//------------------------------------------------------------------------------
// 打包写入
//------------------------------------------------------------------------------

// packetWriter 把段攒进一个 MTU 大小的缓冲，满了或收到 Flush 就发出去。
type packetWriter struct {
	mu  sync.Mutex
	buf []byte
	n   int
	// send 把攒好的一个 UDP 包发出去。
	send func([]byte) error
}

func newPacketWriter(mtu uint32, send func([]byte) error) *packetWriter {
	return &packetWriter{buf: make([]byte, mtu), send: send}
}

func (w *packetWriter) Write(seg Segment) error {
	size := int(seg.ByteSize())
	if size > len(w.buf) {
		return ErrSegmentTooLarge
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	// 装不下了先把已有的发走，再装这个。
	if w.n+size > len(w.buf) {
		if err := w.flushLocked(); err != nil {
			return err
		}
	}
	if err := seg.Serialize(w.buf[w.n : w.n+size]); err != nil {
		return err
	}
	w.n += size
	return nil
}

func (w *packetWriter) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.flushLocked()
}

func (w *packetWriter) flushLocked() error {
	if w.n == 0 {
		return nil
	}
	err := w.send(w.buf[:w.n])
	// 不管发没发出去都要清空。UDP 上发失败就是丢包，重传机制本来就要
	// 处理这种情况；留着不清会让下一批和这一批黏在一起。
	w.n = 0
	return err
}

//------------------------------------------------------------------------------
// 监听器
//------------------------------------------------------------------------------

// sessionID 唯一标识服务端上的一条会话。
//
// 必须带来源地址：不同客户端各自随机选会话号，只用会话号会撞。也必须带
// 会话号：同一个 NAT 后面的多条连接来源地址完全相同。
type sessionID struct {
	addr string
	conv uint16
}

type Listener struct {
	conn   net.PacketConn
	config *Config

	mu       sync.Mutex
	sessions map[sessionID]*Connection
	closed   bool

	accepted chan *Connection
	done     chan struct{}
}

// Listen 在给定地址上开始接受 mKCP 连接。
func Listen(network, address string, config *Config) (*Listener, error) {
	if config == nil {
		config = DefaultConfig()
	}
	pc, err := net.ListenPacket(network, address)
	if err != nil {
		return nil, err
	}
	tuneSocketBuffer(pc, config.SocketBuffer)
	return NewListener(pc, config), nil
}

// NewListener 在一个已有的 PacketConn 上接受连接。分开一层是为了让上面
// 能套 udp mask 之类的伪装层——那和 mKCP 正交，不该塞进这里。
func NewListener(pc net.PacketConn, config *Config) *Listener {
	if config == nil {
		config = DefaultConfig()
	}
	l := &Listener{
		conn:     pc,
		config:   config,
		sessions: make(map[sessionID]*Connection),
		accepted: make(chan *Connection, 64),
		done:     make(chan struct{}),
	}
	go l.readLoop()
	return l
}

func (l *Listener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.accepted:
		return conn, nil
	case <-l.done:
		return nil, ErrListenerClosed
	}
}

func (l *Listener) Addr() net.Addr { return l.conn.LocalAddr() }

func (l *Listener) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	sessions := make([]*Connection, 0, len(l.sessions))
	for _, c := range l.sessions {
		sessions = append(sessions, c)
	}
	l.mu.Unlock()

	close(l.done)
	err := l.conn.Close()
	// 已建立的连接跟着一起收场。留着它们只会对着一个已关闭的 socket
	// 重传到超时。
	for _, c := range sessions {
		_ = c.Close()
	}
	return err
}

func (l *Listener) readLoop() {
	buf := make([]byte, l.config.MTU+512) // 留点余量，防止对端 MTU 配得比我们大
	for {
		n, addr, err := l.conn.ReadFrom(buf)
		if n > 0 {
			l.dispatch(buf[:n], addr)
		}
		if err != nil {
			select {
			case <-l.done:
			default:
				// 读失败通常意味着 socket 废了，没法继续服务。
				_ = l.Close()
			}
			return
		}
	}
}

// dispatch 把一个 UDP 包里的所有段投给对应的会话，必要时建新会话。
func (l *Listener) dispatch(payload []byte, addr net.Addr) {
	segments := ReadSegments(payload)
	if len(segments) == 0 {
		// 解析不出任何段：可能是扫描探测，也可能是别的协议打错了端口。
		// 直接丢弃，不回任何东西——回了就等于告诉探测方这里有服务。
		return
	}
	id := sessionID{addr: addr.String(), conv: segments[0].Conversation()}

	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	conn, found := l.sessions[id]
	if !found {
		// 会话数上限。段格式没有认证，随机 UDP 包仍有约 1/64 的概率被
		// 解析成合法段；没有上限的话，一个来源换着端口发就能把内存打满。
		if len(l.sessions) >= maxSessions {
			l.mu.Unlock()
			return
		}
		// 会话不存在时收到 Terminate：这是上一条连接的收尾包打过来了。
		// 为它建新会话的话，会立刻因为对端已经走了而超时销毁，纯属浪费。
		if segments[0].Command() == CommandTerminate {
			l.mu.Unlock()
			return
		}
		conn = l.newSession(id, addr)
		l.sessions[id] = conn
	}
	l.mu.Unlock()

	if !found {
		select {
		case l.accepted <- conn:
		case <-l.done:
			_ = conn.Close()
			return
		default:
			// 上层来不及 Accept。丢掉这条新连接而不是无限排队——
			// 排队只会让积压越来越长，客户端那边早就超时重连了。
			_ = conn.Close()
			return
		}
	}
	conn.Input(segments)
}

// newSession 建一条服务端连接。调用方须持锁。
func (l *Listener) newSession(id sessionID, addr net.Addr) *Connection {
	writer := newPacketWriter(l.config.MTU, func(b []byte) error {
		_, err := l.conn.WriteTo(b, addr)
		return err
	})
	return NewConnection(ConnMetadata{
		LocalAddr:    l.conn.LocalAddr(),
		RemoteAddr:   addr,
		Conversation: id.conv,
	}, writer, closerFunc(func() error {
		l.mu.Lock()
		delete(l.sessions, id)
		l.mu.Unlock()
		return nil
	}), l.config)
}

// SessionCount 是当前活跃会话数。给监控用——它涨着不降通常意味着
// 会话没被正确回收。
func (l *Listener) SessionCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.sessions)
}

type closerFunc func() error

func (f closerFunc) Close() error { return f() }

//------------------------------------------------------------------------------
// 拨号器
//------------------------------------------------------------------------------

// globalConv 给本进程发起的每条连接分配会话号。
//
// 从随机值起步而不是从 0：固定起点会让每次重启后的头几条连接用同样的
// 号，对着同一个服务端时容易撞上它那边还没回收干净的旧会话。
var globalConv = func() *atomic.Uint32 {
	var v atomic.Uint32
	v.Store(uint32(time.Now().UnixNano()) & 0xFFFF)
	return &v
}()

// Dial 向远端发起一条 mKCP 连接。
func Dial(network, address string, config *Config) (*Connection, error) {
	if config == nil {
		config = DefaultConfig()
	}
	udpConn, err := net.Dial(network, address)
	if err != nil {
		return nil, err
	}
	tuneSocketBuffer(udpConn, config.SocketBuffer)
	return NewClientConn(udpConn, config), nil
}

// NewClientConn 在一个已连接的 UDP socket 上跑客户端侧 mKCP。
func NewClientConn(udpConn net.Conn, config *Config) *Connection {
	if config == nil {
		config = DefaultConfig()
	}
	conv := uint16(globalConv.Add(1))
	writer := newPacketWriter(config.MTU, func(b []byte) error {
		_, err := udpConn.Write(b)
		return err
	})
	conn := NewConnection(ConnMetadata{
		LocalAddr:    udpConn.LocalAddr(),
		RemoteAddr:   udpConn.RemoteAddr(),
		Conversation: conv,
	}, writer, udpConn, config)

	go func() {
		buf := make([]byte, config.MTU+512)
		for {
			n, err := udpConn.Read(buf)
			if n > 0 {
				if segments := ReadSegments(buf[:n]); len(segments) > 0 {
					conn.Input(segments)
				}
			}
			if err != nil {
				return
			}
		}
	}()
	return conn
}

// ReadSegments 把一个 UDP 包拆成它承载的所有段。
//
// 一个包里可能有多个段（发送侧会攒批）。中途解析失败就停下并保留已解析
// 出来的——剩下的字节没法信任，但前面的是完整的，丢掉它们等于白白制造
// 一次重传。
func ReadSegments(payload []byte) []Segment {
	var segments []Segment
	for len(payload) > 0 {
		seg, rest := ReadSegment(payload)
		if seg == nil {
			break
		}
		segments = append(segments, seg)
		payload = rest
	}
	return segments
}

// tuneSocketBuffer 放大 UDP socket 的收发缓冲区。
//
// 失败不算错误：很多系统会把请求值静默截到 net.core.rmem_max，容器里
// 甚至可能完全改不动。缓冲区小只是慢，不是不能用。
func tuneSocketBuffer(conn any, size int) {
	if size <= 0 {
		return
	}
	type bufferedConn interface {
		SetReadBuffer(int) error
		SetWriteBuffer(int) error
	}
	if c, ok := conn.(bufferedConn); ok {
		_ = c.SetReadBuffer(size)
		_ = c.SetWriteBuffer(size)
	}
}
