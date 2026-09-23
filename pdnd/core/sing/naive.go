package sing

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync/atomic"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/inbound"
	"github.com/sagernet/sing-box/common/listener"
	"github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/common/uot"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/transport/v2rayhttp"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/auth"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	aTLS "github.com/sagernet/sing/common/tls"
	sHttp "github.com/sagernet/sing/protocol/http"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/aegispanel/nodeagent/core"
)

// NaiveInbound 是带用户热管理与流量计数的 Naive 入站。
//
// Naive 伪装成普通的 HTTP/2 CONNECT 代理 —— 一个 TLS + h2 的正常网站，
// 中间设备看到的就是常规 HTTPS 流量。它的抗封锁能力来自这份「普通」，
// 因此这里不能加任何自定义握手，只能在既有的 HTTP 语义上做文章。
//
// 只实现 TCP（h2c / h2 over TLS）。HTTP/3 需要 QUIC 监听器，
// 上游也是通过可选钩子挂上去的；机场场景下 Hysteria2/TUIC 已经覆盖了
// 基于 QUIC 的需求，再多一条同类路径收益有限。
type NaiveInbound struct {
	managedBase
	router     adapter.ConnectionRouterEx
	listener   *listener.Listener
	tlsConfig  tls.ServerConfig
	httpServer *http.Server

	// 与 SOCKS/HTTP 同理：Authenticator 构造后不可变，
	// 热更新只能整体替换（见 ProxyInbound.authn 的说明）
	authn atomic.Pointer[auth.Authenticator]

	// 非法请求的伪装回落。为 nil 时退回到「尽量断链」
	masquerade http.Handler

	// 入站被替换时（改配置会重建整个 box）Serve 会以
	// 「listener 已关闭」退出。那是正常收摊，不是故障 ——
	// 有了这个标记才能把它和真正的异常区分开。
	closed atomic.Bool
}

var (
	_ adapter.Inbound = (*NaiveInbound)(nil)
	_ managedInbound  = (*NaiveInbound)(nil)
	_ http.Handler    = (*NaiveInbound)(nil)
)

func RegisterNaive(registry *inbound.Registry) {
	inbound.Register[NaiveOptions](registry, C.TypeNaive, NewNaiveInbound)
}

func NewNaiveInbound(ctx context.Context, router adapter.Router, logger log.ContextLogger,
	tag string, opts NaiveOptions) (adapter.Inbound, error) {

	options := opts.NaiveInboundOptions
	masquerade, err := buildMasquerade(opts.Masquerade)
	if err != nil {
		return nil, err
	}

	in := &NaiveInbound{
		masquerade:  masquerade,
		managedBase: newManagedBase(C.TypeNaive, tag, ctx, logger),
		router:      uot.NewRouter(router, logger),
		listener: listener.New(listener.Options{
			Context: ctx, Logger: logger, Listen: options.ListenOptions,
		}),
	}

	if len(options.Users) > 0 {
		seed := make([]core.User, 0, len(options.Users))
		for _, u := range options.Users {
			seed = append(seed, core.User{UUID: u.Username})
		}
		in.users.Add(seed)
	}
	in.applyUsers()

	if options.TLS != nil {
		tlsConfig, err := tls.NewServer(ctx, logger, common.PtrValueOrDefault(options.TLS))
		if err != nil {
			return nil, err
		}
		in.tlsConfig = tlsConfig
	}
	if masquerade == nil {
		logger.Warn("未配置 masquerade，主动探测可以通过一次 h2 请求识别出这是代理")
	}
	return in, nil
}

func (h *NaiveInbound) applyUsers() {
	list := h.users.Snapshot()
	users := make([]auth.User, 0, len(list))
	for _, u := range list {
		users = append(users, auth.User{Username: u.UUID, Password: u.UUID})
	}
	h.authn.Store(auth.NewAuthenticator(users))
}

func (h *NaiveInbound) AddUsers(users []core.User) error {
	if added := h.users.Add(users); len(added) == 0 {
		return nil
	}
	h.applyUsers()
	return nil
}

func (h *NaiveInbound) UpsertUsers(users []core.User) error {
	if updated := h.users.Upsert(users); len(updated) == 0 {
		return nil
	}
	h.applyUsers()
	return nil
}

func (h *NaiveInbound) DelUsers(uuids []string) error {
	if gone := h.users.Del(uuids); len(gone) == 0 {
		return nil
	}
	h.applyUsers()
	return nil
}

func (h *NaiveInbound) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	if h.tlsConfig != nil {
		if err := h.tlsConfig.Start(); err != nil {
			return E.Cause(err, "启动 TLS")
		}
	}
	tcpListener, err := h.listener.ListenTCP()
	if err != nil {
		return err
	}

	h.httpServer = &http.Server{
		Handler:     h2c.NewHandler(h, &http2.Server{}),
		BaseContext: func(net.Listener) context.Context { return h.ctx },
	}

	var ln net.Listener = tcpListener
	if h.tlsConfig != nil {
		// h2 必须出现在 ALPN 里，否则客户端协商不出 HTTP/2，
		// 而 Naive 的 padding 帧格式依赖 h2 的分帧
		if len(h.tlsConfig.NextProtos()) == 0 {
			h.tlsConfig.SetNextProtos([]string{http2.NextProtoTLS, "http/1.1"})
		} else if !common.Contains(h.tlsConfig.NextProtos(), http2.NextProtoTLS) {
			h.tlsConfig.SetNextProtos(append([]string{http2.NextProtoTLS}, h.tlsConfig.NextProtos()...))
		}
		ln = aTLS.NewListener(tcpListener, h.tlsConfig)
	}

	go func() {
		err := h.httpServer.Serve(ln)
		if err == nil || errors.Is(err, http.ErrServerClosed) || h.closed.Load() {
			return
		}
		h.logger.Error("HTTP 服务异常退出: ", err)
	}()
	return nil
}

func (h *NaiveInbound) Close() error {
	h.closed.Store(true)
	return common.Close(h.listener, common.PtrOrNil(h.httpServer), h.tlsConfig)
}

func (h *NaiveInbound) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := log.ContextWithNewID(r.Context())

	// 非 CONNECT 或缺 Padding 头的请求一律当作探测
	if r.Method != http.MethodConnect || r.Header.Get("Padding") == "" {
		h.reject(w, r, http.StatusBadRequest)
		return
	}

	name, password, ok := sHttp.ParseBasicAuth(r.Header.Get("Proxy-Authorization"))
	if ok {
		if authn := h.authn.Load(); authn != nil {
			ok = authn.Verify(name, password)
		} else {
			ok = false
		}
	}
	if !ok {
		h.reject(w, r, http.StatusProxyAuthRequired)
		return
	}
	u, found := h.users.ByUUID(name)
	if !found {
		// 认证器与用户表来自同一次 applyUsers，正常不会不一致；
		// 真发生了说明中途被替换过，拒掉比放行安全
		h.reject(w, r, http.StatusProxyAuthRequired)
		return
	}

	w.Header().Set("Padding", generatePaddingHeader())
	w.WriteHeader(http.StatusOK)
	w.(http.Flusher).Flush()

	hostPort := r.Header.Get("-connect-authority")
	if hostPort == "" {
		hostPort = r.URL.Host
		if hostPort == "" {
			hostPort = r.Host
		}
	}
	source := sHttp.SourceAddress(r)
	destination := M.ParseSocksaddr(hostPort).Unwrap()

	if hijacker, isHijacker := w.(http.Hijacker); isHijacker {
		conn, _, err := hijacker.Hijack()
		if err != nil {
			h.logger.ErrorContext(ctx, E.Cause(err, "接管连接失败"))
			return
		}
		h.route(ctx, false, &naiveConn{Conn: conn}, u, source, destination)
		return
	}
	h.route(ctx, true, &naiveH2Conn{
		reader: r.Body, writer: w, flusher: w.(http.Flusher), remoteAddress: source,
	}, u, source, destination)
}

// route 把连接交给路由器。
//
// waitForClose 为真时必须阻塞到连接结束：这条连接跑在 HTTP/2 的流上，
// ServeHTTP 一返回 Go 的 http 库就会关掉这个流，代理还没转发完就断了。
func (h *NaiveInbound) route(ctx context.Context, waitForClose bool, conn net.Conn,
	u core.User, source, destination M.Socksaddr) {

	metadata := adapter.InboundContext{
		Inbound: h.Tag(), InboundType: h.Type(),
		Source: source, Destination: destination,
		OriginDestination: M.SocksaddrFromNet(conn.LocalAddr()).Unwrap(),
		User:              u.UUID,
	}
	counted := h.wrapConn(u, conn, source)
	if counted == nil {
		h.logger.InfoContext(ctx, "拒绝连接：用户 ", u.UUID, " 已达设备数上限 ", u.DeviceLimit)
		_ = conn.Close()
		return
	}

	if !waitForClose {
		h.router.RouteConnectionEx(ctx, counted, metadata, nil)
		return
	}
	done := make(chan struct{})
	wrapper := v2rayhttp.NewHTTP2Wrapper(counted)
	h.router.RouteConnectionEx(ctx, counted, metadata, N.OnceClose(func(error) { close(done) }))
	<-done
	wrapper.CloseWrapper()
}

// reject 处理一个不该被代理的请求。
//
// 配了 masquerade 就把它当成一个普通的网站访客 —— 这是唯一能真正
// 挡住主动探测的做法：探测方看到的是一个货真价实的网站响应，
// 与「这台机器上有代理」这个结论毫无关联。
//
// 没配的话只能退回到断链，而那是有明显短板的：
//   - HTTP/1.1 路径拿得到 Hijacker，连接直接断掉，探测方只看到空响应
//   - HTTP/2 路径拿不到 Hijacker，只能回一个规整的 400/407
//
// 换句话说，不配 masquerade 时，一次 h2 的普通 GET 就足以判定这里是代理。
// 构造时会为此发一条警告。
func (h *NaiveInbound) reject(w http.ResponseWriter, r *http.Request, status int) {
	if h.masquerade != nil {
		h.masquerade.ServeHTTP(w, r)
		return
	}
	rejectNaive(w, status)
}

func rejectNaive(w http.ResponseWriter, status int) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		w.WriteHeader(status)
		return
	}
	conn, _, err := hijacker.Hijack()
	if err != nil {
		w.WriteHeader(status)
		return
	}
	// 走到这里说明是 TLS 连接，Cast 通常拿不到裸 TCPConn，
	// 于是退化成正常的 FIN 关闭。保留这段是因为明文（h2c）
	// 场景下它确实能发出 RST。
	if tcpConn, isTCP := common.Cast[*net.TCPConn](conn); isTCP {
		_ = tcpConn.SetLinger(0)
	}
	_ = conn.Close()
}
