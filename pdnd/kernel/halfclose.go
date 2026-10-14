package kernel

import (
	"errors"
	"net"
)

// 包装连接的半关闭。
//
// 内嵌 net.Conn 接口的包装类型不会提升底层 *net.TCPConn / *tls.Conn 的
// CloseWrite，转发对它的类型断言就失败，半关闭传不出去，对端一直等 EOF
// （10 万连接实测里客户端断开后会话不释放的根因之一）。凡是落在数据路径上的
// 包装都经这里把 CloseWrite 转给底层。

var errHalfCloseUnsupported = errors.New("底层连接不支持半关闭")

// closeWriteOf 对 conn 做半关闭；不支持时返回 errHalfCloseUnsupported。
func closeWriteOf(conn net.Conn) error {
	if cw, ok := conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return errHalfCloseUnsupported
}

func (c *probePrefixConn) CloseWrite() error { return closeWriteOf(c.Conn) }
func (c *bufferedNetConn) CloseWrite() error { return closeWriteOf(c.Conn) }
func (c *hijackedNetConn) CloseWrite() error { return closeWriteOf(c.Conn) }

// ssHeaderReadBuffer 是只为解析请求头而建的 bufio 读缓冲大小（ss2022、vmess、
// socks/http）。转发阶段 32KB 的大块读不经它（bufio 对不小于缓冲的读直通底层），
// 2KB 的小块读才过一道。原先 32–64KB 每连接常驻。
const ssHeaderReadBuffer = 4 << 10

// closerFunc 把一个关闭动作包成 io.Closer（QUIC 连接的 CloseWithError 等）。
type closerFunc func() error

func (f closerFunc) Close() error { return f() }
