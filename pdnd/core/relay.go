package core

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
)

// 双向转发（Relay）。
//
// 所有协议的「客户端 ↔ 上游」字节搬运都走这里，原先每个适配器各写一对
// io.Copy goroutine，问题也各有一份（10 万连接实测）：
//
//   - 客户端断开不释放：上行结束后对上游的 CloseWrite 断言在包装连接上
//     失败，下行一直阻塞在读上游，连接、goroutine、fd 全部泄漏。
//   - 每连接常驻两份 32KB 拷贝缓冲：空闲连接也一直拿着，10 万连接就是 6GB。
//   - 每连接 3 个 goroutine（连接本身 + 两个拷贝），GC 扫栈随之线性增长。
//   - 流量要到连接结束才入账：长连接跨多少个上报周期都不计，进程被强杀就丢。
//
// 这里的做法：
//
//   - 上行在调用方的 goroutine 里跑，只另起一个下行 goroutine：每连接 2 个。
//   - 缓冲按需借还：源是裸 TCP 时（上游几乎总是）等到可读才从池里借缓冲，
//     读完即还，空闲时一个字节缓冲都不占；源是 TLS 这类有内部状态的连接时，
//     空闲阻塞读只拿一份 2KB 小缓冲，读满了才换池里的 32KB 大缓冲，读短了还回去。
//   - 每搬一块就把字节数原子加到调用方给的计数器上，上报周期随时可取增量。
//   - 一个方向正常结束（EOF）：把半关闭传给对端，另一方向进入「单向收尾」，
//     空闲超过 HalfCloseTimeout（默认 1 秒）即收尾；任一方向出错：立刻关两端。
//   - 两个方向都开着时，空闲超过 IdleTimeout（默认 300 秒）回收。两个值与 Xray
//     的 uplinkOnly/downlinkOnly、connIdle 同一含义。

// 默认值，进程级，可经 SetRelayTimeouts 改（pdnd 配置 connection_idle_seconds /
// half_close_seconds）。
var (
	relayIdleTimeout      atomic.Int64
	relayHalfCloseTimeout atomic.Int64
)

const (
	// DefaultRelayIdleTimeout 是两个方向都开着时的空闲回收时间。
	DefaultRelayIdleTimeout = 300 * time.Second
	// DefaultRelayHalfCloseTimeout 是一侧结束后、另一侧的空闲收尾时间。
	DefaultRelayHalfCloseTimeout = time.Second
)

func init() {
	relayIdleTimeout.Store(int64(DefaultRelayIdleTimeout))
	relayHalfCloseTimeout.Store(int64(DefaultRelayHalfCloseTimeout))
}

// SetRelayTimeouts 设置进程级的空闲回收与单向收尾时间。idle 为 0 表示不按空闲回收；
// halfClose 不能为 0（那等于不收尾，正是泄漏的来源），传 0 时用默认值。
func SetRelayTimeouts(idle, halfClose time.Duration) {
	if idle < 0 {
		idle = 0
	}
	if halfClose <= 0 {
		halfClose = DefaultRelayHalfCloseTimeout
	}
	relayIdleTimeout.Store(int64(idle))
	relayHalfCloseTimeout.Store(int64(halfClose))
}

// RelayTimeouts 返回当前生效的空闲回收与单向收尾时间。
func RelayTimeouts() (idle, halfClose time.Duration) {
	return time.Duration(relayIdleTimeout.Load()), time.Duration(relayHalfCloseTimeout.Load())
}

// RelayStream 是转发的一端。net.Conn 天然满足；读写不在同一个对象上的承载
// （VMess 的分块读写、mux 子流）用 SplitStream 拼一个。
//
// 可选能力按类型断言取用：CloseWrite() error 用来传半关闭。
type RelayStream interface {
	io.Reader
	io.Writer
	io.Closer
}

// RelayOptions 是一次转发的参数，零值可用（不限速、不计数）。
type RelayOptions struct {
	// Limiter 是这个用户的令牌桶，上下行共用；nil 不限速。
	Limiter *rate.Limiter
	// Up / Down 是上行（客户端 → 上游）与下行的原子计数，每搬一块累加一次。
	Up, Down *atomic.Int64
	// NoHalfClose 为真时任一方向结束就关两端。QUIC 流、AnyTLS 子流这类承载
	// 的 Close 语义与 TCP 半关闭不同，沿用它们原来「一侧结束即全关」的收尾。
	NoHalfClose bool
}

// SplitStream 把分开的读端、写端和负责关闭的对象拼成一个 RelayStream。
// 它不支持半关闭：写端结束后另一方向按单向收尾计时收尾。
type SplitStream struct {
	R io.Reader
	W io.Writer
	C io.Closer
}

func (s *SplitStream) Read(p []byte) (int, error)  { return s.R.Read(p) }
func (s *SplitStream) Write(p []byte) (int, error) { return s.W.Write(p) }
func (s *SplitStream) Close() error                { return s.C.Close() }

// TransparentConn 由「不缓冲、不改字节」的透明包装实现（出站租约连接等），
// 转发据此取到底下的裸 TCP，走无缓冲等待。会缓冲的包装（TLS、bufio）不能实现它。
type TransparentConn interface {
	TransparentConn() net.Conn
}

// Relay 在 client 与 upstream 之间双向转发，直到两个方向都结束，返回上下行字节数。
// 上行在当前 goroutine 里跑。返回前两端不一定已关闭（正常半关闭收尾时不关），
// 调用方照旧负责 Close。
func Relay(client, upstream RelayStream, opt RelayOptions) (up, down int64) {
	r := newRelayState(client, upstream, opt)
	downDone := make(chan struct{})
	go func() {
		defer close(downDone)
		defer r.recoverPanic()
		var err error
		down, err = r.copy(client, upstream, opt.Down)
		r.directionDone(client, err)
	}()
	func() {
		defer r.recoverPanic()
		var err error
		up, err = r.copy(upstream, client, opt.Up)
		r.directionDone(upstream, err)
	}()
	<-downDone
	r.stop()
	return up, down
}

type relayState struct {
	client, upstream RelayStream
	opt              RelayOptions

	ctx    context.Context
	cancel context.CancelFunc

	// last 是最近一次搬到字节的时刻（UnixNano）。
	last atomic.Int64
	// finished 是已结束的方向数；halfClosed 表示已进入单向收尾。
	finished   atomic.Int32
	halfClosed atomic.Bool

	mu        sync.Mutex
	timer     *time.Timer
	stopped   bool
	torndown  bool
	idle      time.Duration
	halfClose time.Duration
}

func newRelayState(client, upstream RelayStream, opt RelayOptions) *relayState {
	idle, halfClose := RelayTimeouts()
	r := &relayState{client: client, upstream: upstream, opt: opt, idle: idle, halfClose: halfClose}
	if opt.Limiter != nil {
		// 只有限速时才需要：令牌桶的等待要能被收尾打断，否则低速用户被踢、
		// 进程关停时会卡在 WaitN 里几分钟。
		r.ctx, r.cancel = context.WithCancel(context.Background())
	}
	r.touch()
	if idle > 0 {
		r.timer = time.AfterFunc(idle, r.onTimer)
	}
	return r
}

func (r *relayState) touch() { r.last.Store(time.Now().UnixNano()) }

// onTimer 检查空闲：超时即收尾，没到就按剩余时间重排。
func (r *relayState) onTimer() {
	r.mu.Lock()
	if r.stopped || r.torndown {
		r.mu.Unlock()
		return
	}
	limit := r.idle
	if r.halfClosed.Load() {
		limit = r.halfClose
	}
	elapsed := time.Duration(time.Now().UnixNano() - r.last.Load())
	if limit > 0 && elapsed < limit {
		r.timer.Reset(limit - elapsed)
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()
	r.teardown()
}

// directionDone 处理一个方向的结束。dst 是这个方向的写端。
func (r *relayState) directionDone(dst RelayStream, err error) {
	finished := r.finished.Add(1)
	if err != nil || r.opt.NoHalfClose {
		r.teardown()
		return
	}
	// 正常 EOF：把半关闭传给对端，对端才知道「这边说完了」。
	if cw, ok := dst.(interface{ CloseWrite() error }); ok {
		if cwErr := cw.CloseWrite(); cwErr != nil && !isClosedErr(cwErr) {
			r.teardown()
			return
		}
	}
	if finished >= 2 {
		return
	}
	// 另一个方向进入单向收尾：空闲超过 halfClose 即收尾。
	r.halfClosed.Store(true)
	r.touch()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped || r.torndown {
		return
	}
	if r.timer == nil {
		r.timer = time.AfterFunc(r.halfClose, r.onTimer)
	} else {
		r.timer.Reset(r.halfClose)
	}
}

// teardown 关两端并打断令牌桶等待，两个方向都会随之结束。只做一次。
func (r *relayState) teardown() {
	r.mu.Lock()
	if r.torndown {
		r.mu.Unlock()
		return
	}
	r.torndown = true
	if r.timer != nil {
		r.timer.Stop()
	}
	r.mu.Unlock()
	if r.cancel != nil {
		r.cancel()
	}
	_ = r.client.Close()
	_ = r.upstream.Close()
}

func (r *relayState) stop() {
	r.mu.Lock()
	r.stopped = true
	if r.timer != nil {
		r.timer.Stop()
	}
	r.mu.Unlock()
	if r.cancel != nil {
		r.cancel()
	}
}

// recoverPanic 兜住一个方向里的 panic（多半是某个协议的 Write 被畸形状态打出来的）：
// 只断这一条连接，不带走进程。
func (r *relayState) recoverPanic() {
	if v := recover(); v != nil {
		relayPanics.Add(1)
		r.finished.Add(1)
		r.teardown()
		slog.Error("转发发生 panic，已断开该连接", "panic", v, "stack", string(debug.Stack()))
	}
}

var relayPanics atomic.Int64

// RelayPanics 返回进程启动以来被兜住的转发 panic 次数。
func RelayPanics() int64 { return relayPanics.Load() }

// copy 把 src 搬到 dst，返回字节数与非 EOF 的错误。
func (r *relayState) copy(dst io.Writer, src io.Reader, counter *atomic.Int64) (int64, error) {
	if rc := rawConnOf(src); rc != nil {
		return r.copyRaw(dst, rc, counter)
	}
	return r.copyAdaptive(dst, src, counter)
}

// emit 写出一块：限速、写、计数、记活动时间。
func (r *relayState) emit(dst io.Writer, p []byte, counter *atomic.Int64) (int, error) {
	if r.opt.Limiter != nil {
		if err := waitLimiter(r.ctx, r.opt.Limiter, len(p)); err != nil {
			return 0, err
		}
	}
	n, err := dst.Write(p)
	if n > 0 {
		if counter != nil {
			counter.Add(int64(n))
		}
		r.touch()
	}
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	return n, err
}

// waitLimiter 等令牌；一次要的超过桶容量时分段等（桶容量下限 64KB，正常不会触发）。
func waitLimiter(ctx context.Context, limiter *rate.Limiter, n int) error {
	burst := limiter.Burst()
	for n > 0 {
		k := n
		if burst > 0 && k > burst {
			k = burst
		}
		if err := limiter.WaitN(ctx, k); err != nil {
			return err
		}
		n -= k
	}
	return nil
}

// copyAdaptive 是有内部状态的源（TLS、Vision、AEAD 流）的拷贝：空闲时只拿小缓冲，
// 读满小缓冲说明有成块数据，换池里的大缓冲；读短了说明这一波结束，大缓冲还回去。
func (r *relayState) copyAdaptive(dst io.Writer, src io.Reader, counter *atomic.Int64) (int64, error) {
	small := getSmallBuf()
	defer putSmallBuf(small)
	var big *[]byte
	defer func() {
		if big != nil {
			putBigBuf(big)
		}
	}()
	buf := *small
	var written int64
	for {
		nr, readErr := src.Read(buf)
		if nr > 0 {
			nw, writeErr := r.emit(dst, buf[:nr], counter)
			written += int64(nw)
			if writeErr != nil {
				return written, writeErr
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				return written, nil
			}
			return written, readErr
		}
		switch {
		case big == nil && nr == len(buf):
			big = getBigBuf()
			buf = *big
		case big != nil && nr < relaySmallBufSize:
			putBigBuf(big)
			big = nil
			buf = *small
		}
	}
}

// rawConnOf 穿过透明包装取裸 TCP 的 RawConn；不是裸 TCP 返回 nil。
func rawConnOf(src io.Reader) rawReader {
	if !rawCopySupported {
		return nil
	}
	for depth := 0; depth < 8; depth++ {
		switch v := src.(type) {
		case *net.TCPConn:
			rc, err := v.SyscallConn()
			if err != nil {
				return nil
			}
			return rc
		case TransparentConn:
			src = v.TransparentConn()
		default:
			return nil
		}
	}
	return nil
}

// rawReader 是 syscall.RawConn 的读半边，便于平台文件实现。
type rawReader interface {
	Read(func(fd uintptr) bool) error
}

func isClosedErr(err error) bool {
	return errors.Is(err, net.ErrClosed) || errors.Is(err, os.ErrDeadlineExceeded)
}

//------------------------------------------------------------------------------
// 缓冲池
//------------------------------------------------------------------------------

const (
	relaySmallBufSize = 2 << 10
	relayBigBufSize   = 32 << 10
)

var (
	smallBufPool = sync.Pool{New: func() any { b := make([]byte, relaySmallBufSize); return &b }}
	bigBufPool   = sync.Pool{New: func() any { b := make([]byte, relayBigBufSize); return &b }}
)

func getSmallBuf() *[]byte  { return smallBufPool.Get().(*[]byte) }
func putSmallBuf(b *[]byte) { smallBufPool.Put(b) }
func getBigBuf() *[]byte    { return bigBufPool.Get().(*[]byte) }
func putBigBuf(b *[]byte)   { bigBufPool.Put(b) }

// GetCopyBuffer / PutCopyBuffer 把 32KB 缓冲池借给 core 之外的拷贝循环（mux 子流、
// UDP 中继），用完必须归还，归还后不能再碰。
func GetCopyBuffer() *[]byte  { return getBigBuf() }
func PutCopyBuffer(b *[]byte) { putBigBuf(b) }
