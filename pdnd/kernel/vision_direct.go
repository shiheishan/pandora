package kernel

import (
	"bytes"
	"io"
	"net"
)

// Vision 直通（command=2）的读写切换。帧格式与 TLS 识别见 vision.go，普通 TLS
// 外层的缓冲交接见 vision_tls_tap.go。

// visionDirectOuter 是能真正切到 Vision 直通的外层连接（REALITY）：交出底层连接，
// 以及读方向已缓冲、尚未交给上层的字节。
//
// 直通（command=2）的含义是「从这里起不再经外层加解密」：对端发完 command=2 那
// 一帧就改读写裸 TCP。只改状态、照旧经外层读写，等于把外层密文当内层数据交给
// 对端——10-08 真节点测试里内层 TLS 1.3 全部 bad record mac 就是这么来的。
// 外层本身就是裸 TCP 时（不带安全层）读写 Conn 即是直通，不需要它。
type visionDirectOuter interface {
	NetConn() net.Conn
	TakeBufferedForDirect() (plain, raw []byte)
}

// readSource 是当前该读的连接：读侧切直通后是底层连接（连同外层残留）。
func (c *VisionConn) readSource() io.Reader {
	if c.rawReader != nil {
		return c.rawReader
	}
	return c.Conn
}

// switchReadDirect 在读到对端的 command=2 之后调用（持 readMu）：取走外层已缓冲
// 的明文与原始字节，之后改读底层连接。
func (c *VisionConn) switchReadDirect() {
	if c.rawReader != nil || c.outer == nil {
		return
	}
	plain, raw := c.outer.TakeBufferedForDirect()
	under := c.outer.NetConn()
	c.directUsed.Store(true)
	if len(plain)+len(raw) == 0 {
		c.rawReader = under
		return
	}
	visionTracef("read direct: outer buffered plain=%d raw=%d", len(plain), len(raw))
	c.rawReader = io.MultiReader(bytes.NewReader(append(plain, raw...)), under)
}

// writeTarget 是当前该写的连接：写侧切直通后是底层连接。
func (c *VisionConn) writeTarget() net.Conn {
	if c.rawWriter != nil {
		return c.rawWriter
	}
	return c.Conn
}

// Close 在用过直通时先关底层连接：外层（REALITY）的 Close 会先发一条加密的
// close_notify，这时它已经落不到裸流里了。
func (c *VisionConn) Close() error {
	if c.directUsed.Load() && c.outer != nil {
		_ = c.outer.NetConn().Close()
	}
	return c.Conn.Close()
}

// releaseDirectWatch 在读侧不再可能切直通时调用（持 readMu）：对端已用 command=1
// 结束填充、或已经切过直通。普通 TLS 外层据此让 tap 转为透传，不再逐记录交付。
func (c *VisionConn) releaseDirectWatch() {
	if c.rawReader != nil || c.watchReleased {
		return
	}
	c.watchReleased = true
	if d, ok := c.outer.(*visionTLSDirect); ok {
		d.tap.passthrough()
	}
}
