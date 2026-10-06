# pdnd/kernel/
> L2 | 父级: /pdnd/CLAUDE.md

NativeCore：协议无关的数据面运行时
  - 每个入站拥有一个 route engine 和一代 outbound
  - 协议以 Adapter 注册进 AdapterRegistry，通过 DataPlane 拨号与监听，流量在此按用户计量
  - 能力矩阵是唯一事实源：--capabilities 打印它，selfcheck 校验它，面板拿它做编排前协商
  - 未在矩阵里的组合 fail closed，不回落到 core/ 的兼容内核。
  - 接客骨架：TCP 类入站的 acceptLoop 都走 accept_loop.go 的 runAcceptLoop，循环里只登记连接、起 goroutine，握手（TLS 限时 10 秒、REALITY 15 秒）一律在连接自己的 goroutine 里；Accept 出错 5ms 起翻倍退避、封顶 1 秒。HTTP 承载靠 http.Server.Serve 自带的退避，QUIC 监听只在关闭时出错；mieru 的 TCP 监听在上游 mux 里，由 core/mieru/listener.go 以同参退避包装
  - 连接失败观测链：适配器在失败点经 connErrorReporter 调 OnConnError（adapter.go 的统一跳过规则）→ NativeCore 把它接到 connerror_log.go 的限流日志出口；分类、脱敏、地址截网段在 connerror.go，QUIC 上游库的 Error 日志经 connerror_sing.go 桥进来

成员清单
runtime.go: 包文档所在。协议无关运行时，持有每入站的 route engine 与 outbound 代
nativecore.go: NativeCore 与 nativeInbound：按 InboundSpec 启动入站，routedDataPlane 把 DialTCP/ListenUDP 接到路由与出站（失败标成 upstream），swap 支持不重启的热更新
  - NewNativeCoreWithLogger 接 main 的 slog.Logger（NewNativeCore 用 slog.Default()），新起与回滚恢复两处 adapter.Start 共用 adapterHooks，OnConnError 都接到同一个 connErrorLogSink，Close 时补打抑制摘要
adapter.go: 协议适配器契约：InboundSpec、DataPlane、AdapterHooks、ConnError 与 Stage 常量、Adapter/AdapterFactory/AdapterRegistry；reportAdapterConnError(Addr) 统一跳过 io.EOF / net.ErrClosed / context.Canceled
connerror.go: 连接失败的中段：connErrorReporter（适配器 Start 时绑 hook + tag + 协议名，零值为空操作）、markConnError / deviceLimitError / admissionError 给错误钉分类（auth / limit / upstream / tls / timeout / truncated / reset / protocol）
  - sanitizeConnErrorReason 抹掉 UUID、IP、域名、长 hex/base64 后截到 160 字节；maskRemoteAddr 把对端截成 IPv4 /24、IPv6 /48（与面板 ByIPPrefix 同粒度），不落完整用户 IP
connerror_log.go: connErrorLogSink：按 (入站, 协议, 阶段, 分类) 固定窗口限流，缺省每分钟逐条 5 条，其余计数，到点 time.AfterFunc 打「已限流」摘要；无失败时不挂定时器，锁内只计数、日志写在锁外
connerror_sing.go: sing logger.Logger 桥：TUIC / Hysteria2 的 nativewire 服务端只会 logger.Error，桥接成 ConnError，滤掉错误码 0 的 QUIC 应用层关闭与空闲超时
accept_loop.go: runAcceptLoop 与 acceptBackoff，各 TCP 类入站与 RealityListener 共用的 Accept 循环
  - 为何存在：握手若在 Accept 循环里同步跑，一条只连不说话的连接让整个入站 10 秒接不进新连接；Accept 出错（EMFILE）不退避则空转跑满一核，出错即退则 RealityListener 永久停止接客
  - 规则：handle 必须立刻返回；done 关或 net.ErrClosed 即退出，其余错误一律退避重试，不区分 Temporary
capabilities.go: 能力矩阵：Capability、NativeCapabilities、NativeCapabilityFor、NativeProtocolNames、NativeCapabilityReport(For)；CI capability smoke 按字面量守 reality-h3-experimental 与 external-reality-xhttp-h3-unverified
selfcheck.go: 启动自检：ValidateNativeCapabilityMatrix 对照默认注册表，不开监听
proxy.go: socks 与 http 入站共用的 proxyAdapter：SOCKS4/4a/5 CONNECT、HTTP CONNECT 与正向 GET，认证绑定面板下发的用户 UUID
proxy_udp.go: SOCKS5 UDP ASSOCIATE：关联锁定首个来源 IP，每个目的地址一条经 DataPlane 路由的 PacketConn，上下行解耦，按用户计量
vless.go: VLESS 入站主体：各承载的监听与分派、TCP 转发；NewDefaultAdapterRegistry 也在这里
vless_request.go: VLESS 请求头解析 readVLESSRequest 与各入站共用的目的地址 vlessDestination
vless_users.go: VLESS 用户表、设备与流量计量
vless_flow.go: VLESS addons 编解码与 flow 协商
vless_mux.go: 原生 XUDP/mux 帧，刻意留在 NativeCore 内而不委托兼容内核
vless_udp.go: VLESS command=UDP：两字节长度帧的数据报经 DataPlane 路由并计量
vless_xhttp_packet.go: VLESS 在 XHTTP packet 模式下的收发
vision.go: XTLS Vision：VisionConn 与 padding/直通状态机
vmess.go: VMess 入站主体：各承载的监听与分派，serveAccepted 把 TLS 握手放进每条连接的 goroutine（TCP、mKCP、WebSocket、HTTP Upgrade、原生 gRPC 的 h2c 与 TLS+h2、XHTTP stream 与 packet-up/reconnect），AuthID 防重放，按命令分派 TCP / UDP / mux；command=UDP 的上下行经 udp_relay.go 解耦
vmess_request.go: VMess AEAD 请求头解析与候选用户定位
vmess_codec.go: VMess 正文分块读写（明文 / AEAD）、KDF 与响应头
vmess_mux.go: VMess 原生 mux 子流，与 vless_mux.go 同一思路留在 NativeCore 内
vmess_users.go: VMess 用户表（热更新时重算命令密钥）、设备与流量计量
vmess_xhttp_packet.go: VMess 在 XHTTP packet 模式下的收发
trojan.go: Trojan 入站 TCP 路径
trojan_udp.go: Trojan UDP ASSOCIATE：地址/长度/CRLF 帧按目的地址建路由 PacketConn，上下行解耦并计量
udp_relay.go: UDP 中继收尾骨架 relayUDPDirections：上行、下行各一个 goroutine 阻塞读，任一方向结束即取消并关连接打断另一方向，SOCKS5、Trojan、VMess command=UDP 共用
  - 为何存在：下行若与上行同一循环轮询（等上行读超时或等上行来包），回程被限成几包每秒，游戏、语音、QUIC 卡死；udp_relay_test.go 用"只发一包、回 1000 包"的突发回归守住
shadowsocks.go: Shadowsocks AEAD 入站：方法表、TCP 请求与按用户试解、AEAD 分块流
shadowsocks_udp.go: Shadowsocks AEAD UDP：逐包按用户主密钥试解定位用户，经 DataPlane 路由并计量
shadowsocks2022.go: Shadowsocks 2022 入站：方法解析、TCP 请求（多用户身份头逐层校验）与用户表
shadowsocks2022_udp.go: Shadowsocks 2022 UDP：按客户端会话 ID 维护会话并定期回收
shadowsocks2022_stream.go: Shadowsocks 2022 的 PSK / 会话密钥派生（blake3）与 TCP AEAD 流
shadowtls.go: ShadowTLS 组合入站，外层 v3 伪装握手在认证判定前按 inboundHandshakeTimeout 限时（判定后的诱饵中继不限时），超时按 tls-handshake 上报
hysteria2.go: Hysteria2 入站（QUIC）
tuic.go: TUIC 入站（QUIC）
juicity.go: Juicity 原生 QUIC/认证/TCP/UDP 数据面
anytls.go: AnyTLS 入站
naive.go: Naive 入站
mieru.go: mieru 接入 NativeCore 的薄层（TCP/UDP）
reality.go: REALITY 服务端配置解析：dest、xver、短 ID、私钥、超时
reality_listener.go: REALITY 监听器与会话上下文，握手错误上报钩子
  - acceptHandoff 走 runAcceptLoop，每条原始连接一个 15 秒截止的握手 worker；Close 关掉握手中的连接，被它打断的握手不上报
reality_client.go: REALITY 客户端配置解析 ParseRealityClientConfig，目前只有单测消费（自检与 cmd/pandora-h3-probe 都不调用它）
inbound_tls.go: 普通 TLS 入站证书加载，inboundHandshakeTimeout（10 秒）、withHandshakeDeadline 与 serverTLSHandshake（vless / vmess / trojan 共用，anytls 用前者）
xhttp.go: XHTTP 配置与模式解析，请求元数据编解码
xhttp_server.go: XHTTP 会话与双工连接，HTTP/1、2、3 承载
xhttp_packet.go: XHTTP packet 队列：乱序入队、ResumeFrom 重连续传
grpc_stream.go: gRPC 双工流连接，接受 gzip 请求、以 identity 帧响应
websocket_netconn.go: WebSocket 承载的 net.Conn
httpupgrade_netconn.go: HTTP Upgrade 承载的 net.Conn
native_transport_server.go: WebSocket 与 HTTP Upgrade 的服务端分派；newInboundHTTPServer 是全部 HTTP 承载（含 gRPC、XHTTP、Naive）共用的 http.Server 构造，ReadHeaderTimeout 给 TLS 握手与请求头限时
mkcp_transport.go: mKCP 传输：配置与掩码解析、监听、MTU 校验、socket 缓冲调优
uot_bridge.go: UDP-over-TCP 桥：把 uot 数据报接到路由后的 PacketConn
*_test.go: 各协议单测，nativecore_test.go 跨协议契约测试；accept_loop_test.go 钉住退避序列、八个 TCP 类入站在 EMFILE 下不空转、RealityListener 在 EMFILE 后照常接客与 VMess over mKCP 能起能停，accept_silent_test.go 钉住静默连接不堵 vmess / vless / trojan 的 TLS 接客、Close 关掉握手中的连接与握手限时；shadowtls_test.go 钉住 v3 回环（含认证后静置超过握手超时仍可收发）、适配器取 inboundHandshakeTimeout、静默连接按 tls-handshake / timeout 关闭并上报；connerror_*_test.go 钉住分类 / 脱敏 / 限流、NativeCore 生产接线，以及 13 个协议各至少一条失败路径真的到达 OnConnError（client 文件用真实 QUIC / mieru 客户端）。互操作门分三个构建标签，都不进默认套件：
  - `interop`（xhttp_external、mkcp_external 的外部 Xray 客户端，与 anytls_client 的 sing-anytls 客户端——后者内部自带 closeLocally/Write 数据竞争（版本见测试文件注释），所以不进 race 套件，CI 以非 race 方式跑）
  - `interop_mihomo`（mihomo_* 系列，需 MIHOMO_BIN 与 MIHOMO_SHA256）
  - `interop_external`（external_clients：sing-box VLESS TLS Vision、Juicity、Naive，各需钉住哈希的外部二进制）
  - CI 只跑 `interop` 里的 Xray XHTTP 与 AnyTLS 两组

法则: 成员完整·一行一文件·父级链接·技术词前置
