package kernel

import (
	"context"
	"errors"
	"net"

	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// anyTLSNegotiatedH2 是 context 键：这条 AnyTLS 连接的外层 TLS 是否协商出 h2，
// 中性页面据此决定讲 HTTP/2 还是 HTTP/1.x。
type anyTLSNegotiatedH2 struct{}

// anyTLSFallback 接 sing-anytls 的 FallbackHandler：口令不对、首包太短时，
// service 把「已读字节可重放」的连接交过来。必须同步处理完再返回——service
// 在 NewConnection 返回时回收那段缓冲。
type anyTLSFallback struct{ adapter *anyTLSAdapter }

var _ N.TCPConnectionHandlerEx = anyTLSFallback{}

func (f anyTLSFallback) NewConnectionEx(ctx context.Context, conn net.Conn, _ M.Socksaddr, _ M.Socksaddr, onClose N.CloseHandlerFunc) {
	if onClose != nil {
		defer onClose(nil)
	}
	a := f.adapter
	a.mu.RLock()
	fallback := a.fallback
	a.mu.RUnlock()
	// 认证失败照常进观测链，只是连接不再立刻断开。
	a.connErr.conn(StageSession, conn, markConnError(connErrAuth, errors.New("anytls authentication failed")))
	h2, _ := ctx.Value(anyTLSNegotiatedH2{}).(bool)
	fallback.serveConn(ctx, conn, nil, h2)
}
