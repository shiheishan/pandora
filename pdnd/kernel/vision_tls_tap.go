package kernel

import (
	"crypto/tls"
	"encoding/binary"
	"errors"
	"net"
	"sync"
)

// VLESS + 普通 TLS（crypto/tls）+ Vision 的直通。
//
// REALITY 是我们自己的 fork，直接交出 input / rawInput（TakeBufferedForDirect）；
// 标准库 tls.Conn 不暴露这两个缓冲，Xray 与 sing-box 是用反射加 unsafe 硬读的。
// 这里换个不碰标准库内部的做法：在 tls.Server 之下垫一层 visionTLSTap，Vision
// 会话期间每次只把底层字节交到当前 TLS 记录的末尾为止。tls.Conn 读一条记录只
// 要求「头 5 字节、再到记录末尾」，从不需要下一条记录的字节，于是它的 rawInput
// 永远不会含有对端切直通之后才发的裸字节——那些全留在 tap 的缓冲和套接字里。
// 切直通时把 tap 置为「已切换」，再把 tls.Conn 里已解密未读的明文读干净（它此时
// 只会从 input 交数据，一碰底层就拿到 errVisionTLSTapSwitched），剩下的从 tap
// 缓冲与底层连接接着读。

var errVisionTLSTapSwitched = errors.New("vision: tls 读方向已切到直通")

// visionTLSTapScratch 能装下一整条最大的 TLS 记录（头 + 2^14 + 256）。
const visionTLSTapScratch = 5 + 16384 + 256

type visionTLSTap struct {
	net.Conn

	mu sync.Mutex
	// aligned：按记录边界交付。握手与读 VLESS 请求头期间开着；确定不是 Vision
	// 会话后关掉，此后直接透传，不多一次拷贝。
	aligned  bool
	switched bool
	scratch  []byte
	buf      []byte // 已从底层读到、尚未交给 tls.Conn 的字节
	readErr  error  // 与数据一起读到的错误，数据交完再报
	hdr      [5]byte
	hdrHave  int
	bodyLeft int
}

func newVisionTLSTap(conn net.Conn) *visionTLSTap {
	return &visionTLSTap{Conn: conn, aligned: true}
}

func (t *visionTLSTap) Read(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.switched {
		return 0, errVisionTLSTapSwitched
	}
	if !t.aligned && len(t.buf) == 0 {
		if t.readErr != nil {
			return 0, t.readErr
		}
		return t.Conn.Read(p)
	}
	if len(t.buf) == 0 {
		if t.readErr != nil {
			return 0, t.readErr
		}
		if t.scratch == nil {
			t.scratch = make([]byte, visionTLSTapScratch)
		}
		n, err := t.Conn.Read(t.scratch)
		t.buf, t.readErr = t.scratch[:n], err
		if n == 0 {
			t.readErr = nil
			return 0, err
		}
	}
	limit := len(t.buf)
	if t.aligned {
		limit = t.recordEnd(t.buf)
	}
	if limit > len(p) {
		limit = len(p)
	}
	n := copy(p, t.buf[:limit])
	if t.aligned {
		t.advance(t.buf[:n])
	}
	t.buf = t.buf[n:]
	if len(t.buf) == 0 && !t.aligned {
		t.scratch = nil
	}
	return n, nil
}

// recordEnd 返回 b 里到当前记录末尾为止的字节数（记录不完整时是全部）。
func (t *visionTLSTap) recordEnd(b []byte) int {
	hdr, hdrHave, bodyLeft := t.hdr, t.hdrHave, t.bodyLeft
	for i := 0; i < len(b); {
		if hdrHave < len(hdr) {
			hdr[hdrHave] = b[i]
			hdrHave++
			i++
			if hdrHave == len(hdr) {
				bodyLeft = int(binary.BigEndian.Uint16(hdr[3:5]))
				if bodyLeft == 0 {
					return i
				}
			}
			continue
		}
		take := min(bodyLeft, len(b)-i)
		i += take
		bodyLeft -= take
		if bodyLeft == 0 {
			return i
		}
	}
	return len(b)
}

// advance 把已交给 tls.Conn 的字节记进记录位置（不会越过当前记录末尾）。
func (t *visionTLSTap) advance(b []byte) {
	for i := 0; i < len(b); {
		if t.hdrHave < len(t.hdr) {
			t.hdr[t.hdrHave] = b[i]
			t.hdrHave++
			i++
			if t.hdrHave == len(t.hdr) {
				t.bodyLeft = int(binary.BigEndian.Uint16(t.hdr[3:5]))
				if t.bodyLeft == 0 {
					t.hdrHave = 0
				}
			}
			continue
		}
		take := min(t.bodyLeft, len(b)-i)
		i += take
		t.bodyLeft -= take
		if t.bodyLeft == 0 {
			t.hdrHave = 0
		}
	}
}

// passthrough 关掉按记录交付：会话不是 Vision，记录边界不再有意义。
func (t *visionTLSTap) passthrough() {
	t.mu.Lock()
	t.aligned = false
	t.mu.Unlock()
}

// switchDirect 让 tls.Conn 之后的读拿到 errVisionTLSTapSwitched，并交出 tap 里
// 还没交给 tls.Conn 的底层字节。
func (t *visionTLSTap) switchDirect() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.switched = true
	out := append([]byte(nil), t.buf...)
	t.buf, t.scratch = nil, nil
	return out
}

// visionTLSDirect 把「tls.Conn + 其下的 tap」适配成 visionDirectOuter。
type visionTLSDirect struct {
	tls *tls.Conn
	tap *visionTLSTap
}

func (d *visionTLSDirect) NetConn() net.Conn { return d.tap.Conn }

func (d *visionTLSDirect) TakeBufferedForDirect() (plain, raw []byte) {
	raw = d.tap.switchDirect()
	// tls.Conn 的 input 里可能还有已解密未读的明文：读到它去碰底层为止。
	buf := make([]byte, 4096)
	for {
		n, err := d.tls.Read(buf)
		plain = append(plain, buf[:n]...)
		if err != nil || n == 0 {
			break
		}
	}
	return plain, raw
}

// visionTLSTapOf 取出垫在 tls.Conn 之下的 tap（没有时为 nil）。
func visionTLSTapOf(conn net.Conn) (*tls.Conn, *visionTLSTap) {
	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		return nil, nil
	}
	tap, _ := tlsConn.NetConn().(*visionTLSTap)
	return tlsConn, tap
}
