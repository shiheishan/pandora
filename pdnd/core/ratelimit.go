package core

import (
	"context"
	"io"
	"net"
	"sync"

	"golang.org/x/time/rate"
)

// 按用户限速。
//
// 面板早就在下发 speed_limit（User.SpeedLimit，单位 kbps），但内核
// 一直没用它——拉下来存着，然后谁也不看。套餐里写的限速对用户毫无约束。
//
// # 一个桶，不是一条连接一个桶
//
// 限的是「这个用户」的带宽，不是「这条连接」的带宽。每条连接各发一个桶
// 的话，开十条连接就能跑十倍速率，限速形同虚设。所以令牌桶按用户 ID 存，
// 同一用户的所有连接、上行和下行共用一个——这也是 V2bX 那类后端的做法。
//
// # 为什么不在 DataPlane 上做
//
// DataPlane 只看得到 route.Meta（目标地址那些），看不到是哪个用户。
// 用户身份只有协议层解出请求头之后才知道，所以限速只能在各 adapter 里
// 拿到 User 的那一刻套上去。

// SpeedLimiters 保存每个用户的令牌桶。零值可用。
type SpeedLimiters struct {
	mu sync.Mutex
	m  map[int64]*speedLimiterEntry
}

type speedLimiterEntry struct {
	limiter *rate.Limiter
	kbps    int
}

// For 返回该用户的限速器；未限速时返回 nil。
//
// 限速值变了就重建桶：面板随时可能改套餐，改完之后还按老速率跑就是错的。
func (s *SpeedLimiters) For(user User) *rate.Limiter {
	if user.SpeedLimit <= 0 {
		// 曾经限过速、现在改成不限，得把桶丢掉，否则永远按老值限。
		s.Remove(user.ID)
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = make(map[int64]*speedLimiterEntry)
	}
	if entry, ok := s.m[user.ID]; ok && entry.kbps == user.SpeedLimit {
		return entry.limiter
	}
	entry := &speedLimiterEntry{
		limiter: rate.NewLimiter(rate.Limit(SpeedLimitBytesPerSecond(user.SpeedLimit)),
			SpeedLimitBurst(user.SpeedLimit)),
		kbps: user.SpeedLimit,
	}
	s.m[user.ID] = entry
	return entry.limiter
}

// Remove 清掉某个用户的桶。用户被删掉时要调，否则 map 只增不减。
func (s *SpeedLimiters) Remove(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m != nil {
		delete(s.m, id)
	}
}

// SpeedLimitBytesPerSecond 把面板的 kbps 换算成字节每秒。
// 1 kbps = 1000 bit/s，和存储的 KiB 不是一回事，这里按比特率算。
func SpeedLimitBytesPerSecond(kbps int) int {
	return kbps * 1000 / 8
}

// SpeedLimitBurst 是令牌桶的容量。
//
// 必须不小于单次读写可能的字节数，否则 WaitN 会因为「要的比桶还大」直接
// 报错而不是等待，连接会莫名其妙断掉。取一秒的量和 64 KiB 里的大者：
// 前者保证低速率下也能放过一个完整的读缓冲，后者是常见读缓冲的上界。
func SpeedLimitBurst(kbps int) int {
	const minBurst = 64 * 1024
	perSecond := SpeedLimitBytesPerSecond(kbps)
	if perSecond < minBurst {
		return minBurst
	}
	return perSecond
}

// SpeedLimitedCopy 是带限速的 io.Copy；limiter 为 nil 时就是 io.Copy。
//
// 为什么在搬运处限速，而不是把连接包一层：
// 转发两端的连接都带着调用方要靠类型断言取用的能力——upstream 那侧有
// `interface{ CloseWrite() error }`，包一层之后断言就失败了，半关闭语义
// 直接丢掉；客户端那侧 Vision 和 ShadowTLS 要拿到裸连接做记录层操作，
// 同样怕被裹住。搬运处只碰字节，不碰类型。
func SpeedLimitedCopy(dst io.Writer, src io.Reader, limiter *rate.Limiter) (int64, error) {
	if limiter == nil {
		return io.Copy(dst, src)
	}
	// 与 io.Copy 的默认缓冲一致。桶容量下限是 64 KiB，装得下一整个缓冲，
	// 不会出现「要的比桶还大」导致 WaitN 直接报错的情况。
	buf := make([]byte, 32*1024)
	var written int64
	for {
		nr, readErr := src.Read(buf)
		if nr > 0 {
			// 先收下再等：数据已经在内核缓冲里，拖着不读只会让对端重传。
			if waitErr := limiter.WaitN(context.Background(), nr); waitErr != nil {
				return written, waitErr
			}
			nw, writeErr := dst.Write(buf[:nr])
			written += int64(nw)
			if writeErr != nil {
				return written, writeErr
			}
			if nw != nr {
				return written, io.ErrShortWrite
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				return written, nil
			}
			return written, readErr
		}
	}
}

// SpeedLimitedConn 在读写两个方向上共用一个令牌桶。
//
// 优先用 SpeedLimitedCopy——包装连接会挡住调用方的类型断言。这个类型是
// 给「搬运不在我们手里」的场景留的：Mieru 的双向拷贝在上游库内部
// （mcommon.BidiCopy），我们只能把连接交出去，没有插手每一段字节的机会。
// 那条路径上连接本来就被流量计数包过一层，再包一层限速不改变性质。
type SpeedLimitedConn struct {
	net.Conn
	limiter *rate.Limiter
}

// NewSpeedLimitedConn 给连接套上限速；limiter 为 nil 时原样返回，
// 不限速的用户不该为此多绕一层。
func NewSpeedLimitedConn(conn net.Conn, limiter *rate.Limiter) net.Conn {
	if limiter == nil {
		return conn
	}
	return &SpeedLimitedConn{Conn: conn, limiter: limiter}
}

func (c *SpeedLimitedConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		// 先收下再等：数据已经在内核缓冲里，拖着不读只会让对端重传。
		if waitErr := c.limiter.WaitN(context.Background(), n); waitErr != nil && err == nil {
			err = waitErr
		}
	}
	return n, err
}

func (c *SpeedLimitedConn) Write(p []byte) (int, error) {
	// 写方向可以先等再发，不存在丢数据的问题。
	if len(p) > 0 {
		if err := c.limiter.WaitN(context.Background(), len(p)); err != nil {
			return 0, err
		}
	}
	return c.Conn.Write(p)
}

// NetConn 暴露底层连接，供需要穿透包装的调用方使用。
func (c *SpeedLimitedConn) NetConn() net.Conn { return c.Conn }
